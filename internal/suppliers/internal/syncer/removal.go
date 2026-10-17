package syncer

import (
	"context"
	"errors"
	"strings"

	"axiom/internal/suppliers/internal/adapter"
)

// Hooks are what a removal needs from outside the engine.
type Hooks interface {
	// InFlight is how many attempts are still running against the supplier.
	InFlight(ctx context.Context) (int, error)
	// DeleteSecrets deletes the sealed credentials and keys. It may be called again after it has succeeded.
	DeleteSecrets(ctx context.Context) error
}

// Remove advances the removal of the supplier as far as it can and reports whether it is complete. Every step is stored
// before the next begins and can be repeated, so a removal that stops anywhere (an error, a restart, a wait) is taken up
// again by calling Remove. The steps: leave the routing snapshot and void rounds in progress; wait for attempts in
// flight (up to DrainLimit); read the charge records; wait Grace for late ones and read once more; delete the sealed
// credentials; done. At the end an event lists the supplier's keys, which a person has to revoke there: M1 never writes
// at the supplier.
func (e *Engine) Remove(ctx context.Context, h Hooks) (bool, error) {
	st, err := e.store.Load(ctx)
	if err != nil {
		return false, err
	}
	for st.Status != Removed {
		wait, err := e.advance(ctx, &st, h)
		if err != nil || wait {
			return false, err
		}
	}
	return true, nil
}

// advance does the step the state is at, and commits the state of the next. It returns true when the step has to wait.
func (e *Engine) advance(ctx context.Context, st *State, h Hooks) (wait bool, err error) {
	c := fresh(st)
	switch st.Removal {
	case StepNone:
		c.State.Status, c.State.Removal, c.State.ConfigGen = Removing, StepLeaving, st.ConfigGen+1
		c.State.RescanFrom, c.State.RemovalAt = e.now().Add(-e.set.Grace), e.now()
		c.Events = append(c.Events, Event{Kind: RemovalStarted})
	case StepLeaving:
		c.State.Removal, c.State.GraceUntil = StepDraining, e.now().Add(e.set.DrainLimit)
	case StepDraining:
		wait, err = e.drain(ctx, &c, h)
	case StepFinalScan, StepLateScan:
		wait, err = e.finalScan(ctx, st, &c)
	case StepGrace:
		if e.now().Before(st.GraceUntil) {
			return true, nil
		}
		c.State.Removal = StepLateScan
		if r, ok := e.mgr.(adapter.Rescanner); ok {
			c.State.Scan = r.Rescan(st.RescanFrom) // read again from before the grace period, not from the watermark
		}
	case StepDelete:
		err = finish(ctx, &c, h)
	case StepDone:
		c.State.Status = Removed
	}
	if err != nil || wait {
		return wait, err
	}
	return false, e.commit(ctx, st, &c)
}

// drain waits for the attempts in flight, up to the limit set when the removal began.
func (e *Engine) drain(ctx context.Context, c *Commit, h Hooks) (wait bool, err error) {
	n, err := h.InFlight(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 && e.now().Before(c.State.GraceUntil) {
		return true, nil
	}
	c.State.Removal = StepFinalScan
	return false, nil
}

// finish deletes the credentials and ends the removal with the list of keys to revoke by hand.
func finish(ctx context.Context, c *Commit, h Hooks) error {
	if err := h.DeleteSecrets(ctx); err != nil {
		return err
	}
	refs := make([]string, len(c.State.Keys))
	for i, k := range c.State.Keys {
		refs[i] = string(k.Ref)
	}
	c.State.Removal, c.State.Status = StepDone, Removed
	c.Events = append(c.Events, Event{Kind: RemovalDone, Why: "revoke these keys at the supplier: " + strings.Join(refs, ",")})
	return nil
}

// finalScan reads the charge records. The first read goes on, pass after pass, until the adapter says the records are
// read up to where the removal began: an adapter reads a backlog window by window, and one pass may end long before the
// present. One call reads at most MaxPages passes of at most MaxPages pages each, so the pages it may read are bounded
// by MaxPages², not by MaxPages; how long the read takes before that has no bound: it is the backlog divided by the
// window one pass covers, at the supplier's rate limits. A supplier that no longer lets us in has nothing more to give:
// that is noted and the removal goes on. Then the next step: after the first read the grace period, after the second the
// deletion.
func (e *Engine) finalScan(ctx context.Context, st *State, c *Commit) (wait bool, err error) {
	switch err = e.readToCutoff(ctx, st, c); {
	case errors.Is(err, adapter.ErrBucketsFull):
		return e.waitForCatchUp(ctx, st, c)
	case errors.Is(err, adapter.ErrAuthRejected):
		c.Events = append(c.Events, Event{Kind: NeedsAttentionEvent, Why: "the last charge records could not be read: the credentials are rejected"})
		c.State.Scan, c.State.ScanOpen = nil, false
	case err != nil:
		return false, err
	case !e.caughtUp(st):
		return e.waitForCatchUp(ctx, st, c) // the next call reads on from the position this one committed
	}
	if st.Removal == StepFinalScan {
		c.State.Removal, c.State.GraceUntil = StepGrace, e.now().Add(e.set.Grace)
	} else {
		c.State.Removal = StepDelete
	}
	return false, nil
}

// waitForCatchUp ends this call and, once, tells a person that the removal has been waiting on the charge records for
// longer than CatchUpAlert. The event is committed here rather than with the step: a step that only waits is not
// committed by its caller.
func (e *Engine) waitForCatchUp(ctx context.Context, st *State, c *Commit) (bool, error) {
	if !st.CatchUpNotified && e.now().Sub(st.RemovalAt) > e.set.CatchUpAlert {
		c.State.CatchUpNotified = true
		c.Events = append(c.Events, Event{Kind: RemovalSlow, Why: "the charge records are still being read"})
		if err := e.commit(ctx, st, c); err != nil {
			return false, err
		}
	}
	return true, nil
}

// readToCutoff reads passes until the records are read up to the removal's start, or this call's passes are used.
func (e *Engine) readToCutoff(ctx context.Context, st *State, c *Commit) error {
	for range e.set.MaxPages {
		if err := e.scan(ctx, st, c); err != nil {
			return err
		}
		if e.caughtUp(st) || st.Removal != StepFinalScan {
			return nil
		}
	}
	return nil
}

// caughtUp reports whether the last pass ended and the adapter says the records are read to the removal's start. An
// adapter that does not say has not caught up: the removal reads on instead of taking a backlog for read (#109).
func (e *Engine) caughtUp(st *State) bool {
	if st.ScanOpen {
		return false
	}
	return st.Removal != StepFinalScan || !st.Through.Before(st.RemovalAt.Add(-e.set.CatchUp))
}
