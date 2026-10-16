package gateway

import (
	"context"
	"errors"
	"time"

	"axiom/internal/billing"
	"axiom/internal/protocol"
)

// errCut is the cause an attempt is cancelled for when its reservation could not be extended.
var errCut = errors.New("gateway: the reservation could not be extended")

// metered meters what the relay has seen and acts on what the meter says (README.md「计量与追加」): it starts the
// extension asked for, takes the outcome of one that came back, and when the approved segments are spent it stops
// reading, idle timer included, until the extension in flight returns. It returns the error of an extension that
// failed, which cuts the stream short. done is the upstream's normal end, after which there is nothing more to stop
// reading for.
func (a *attempt) metered(ctx context.Context, p protocol.Progress, done bool, idle *time.Timer, idleFor time.Duration) error {
	for {
		if came, err := a.landed(false); came && err != nil {
			return err
		}
		step := a.meter.Observe(billing.Progress{
			ContentBytes: p.ContentBytes, PartialBytes: p.PartialBytes, Searches: p.Searches,
			Reported: p.Reported.Usage, Final: p.Final,
		})
		if step.Extend {
			a.extend(ctx, step.Request)
		}
		a.s.setWriteFor(a.writeFor(step.Left))
		if !step.Exhausted || done {
			return nil
		}
		idle.Stop()
		_, err := a.landed(true) // Observe asks whenever none is in flight, so one is now
		idle.Reset(idleFor)
		if err != nil {
			return err
		}
		// The new segment may not cover the read that spent the last one: meter it again.
	}
}

// extend asks for the extension in a goroutine of its own, on a context of its own that neither the client leaving nor
// the attempt ending ends, so the owner reads on meanwhile; the outcome waits in a channel it never blocks on.
func (a *attempt) extend(ctx context.Context, r billing.ExtendRequest) {
	s := a.s
	landing := make(chan error, 1)
	a.asked, a.landing = r, landing
	go func() {
		ctx, cancel := SettleContext(ctx, s.x.Limits.cfg.ExtendWait)
		defer cancel()
		landing <- s.x.withFunds(ctx, s.c, func(ctx context.Context) error { return s.x.Ledger.Extend(ctx, s.c.Hold, r) })
	}()
}

// landed takes the outcome of the extension in flight, if there is one: at once if it has come, or waiting for it.
// It reports whether one was taken and its error; the meter learns of it either way.
func (a *attempt) landed(wait bool) (came bool, err error) {
	if a.landing == nil {
		return false, nil
	}
	if wait {
		err = <-a.landing
	} else {
		select {
		case err = <-a.landing:
		default:
			return false, nil
		}
	}
	a.landing = nil
	a.meter.Extended(a.asked.Seq, err)
	return true, err
}

// writeFor is how long one write to the client may block while the upstream generates into the approved segments:
// no longer than their remaining output takes at the model's fastest, so that what it makes while the owner is not
// reading stays within them and the allowance (README.md「计量与追加」).
func (a *attempt) writeFor(left int64) time.Duration {
	limit := a.s.x.Limits.cfg.WriteTimeout
	speed := a.entry.Limits.OutputSpeed // tokens a second
	if speed <= 0 {
		return limit
	}
	d := float64(time.Second) * float64(left) / float64(speed) // a deadline, not money: a float is exact enough
	if d >= float64(limit) {
		return limit
	}
	return time.Duration(d)
}

// cut is how a call ends whose reservation could not be extended: a source that cannot pay says which, and when it is
// a limit that resets, when to come back, as a 402 does; anything else is billing being unavailable
// (docs/domains/gateway.md「错误」).
func cut(err error) ended {
	ins, ok := errors.AsType[*billing.Insufficient](err)
	if !ok {
		return ended{err: protocol.Error{Type: protocol.BillingUnavailable}}
	}
	e := ended{err: protocol.Error{
		Type:    protocol.CreditsInsufficient,
		Message: "The credits available cannot cover the rest of this response, so it was cut short.",
		Details: `,"limit_source":"` + limitSources[ins.Source] + `"`,
	}}
	if ins.Transient {
		e.after = maxRetryAfter
	}
	return e
}
