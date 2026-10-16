package gateway

import (
	"context"
	"errors"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// settleAttempt is how long one try of a settlement may take.
const settleAttempt = 2 * time.Second

// settle writes the call's settlement, retrying with the same outcome until until (README.md「结束与结算」): a retry
// whose first try committed with its acknowledgement lost gets the Final billing stored, and charges nothing more.
// Each try takes the account's gate, of the completion class.
func (x *Executor) settle(ctx context.Context, c *Call, o billing.Outcome, until time.Time) (f billing.Final, err error) {
	err = x.bg.retry(until, permanent, func() error {
		ctx, cancel := SettleContext(ctx, settleAttempt)
		defer cancel()
		return x.withFunds(ctx, c, func(ctx context.Context) (err error) {
			f, err = x.Ledger.Settle(ctx, c.Hold, o)
			return err
		})
	})
	return f, err
}

// withFunds runs one of the call's money writes behind its account's gate, of the completion class.
func (x *Executor) withFunds(ctx context.Context, c *Call, write func(context.Context) error) error {
	release, err := x.Limits.Funds.Complete(ctx, accountKey(c.caller))
	if err != nil {
		return err
	}
	defer release()
	return write(ctx)
}

// settled follows the settlement up, and reports whether the call's slots are now the background's to give back.
// A settlement written lets the journal drop the request. One that may still be written is retried in the background
// with the same outcome, until SettleBackground. One that never will be, or is given up on, is the cleanup's: its
// slots go once the journal has written all the request journalled, which the cleanup settles by.
func (x *Executor) settled(ctx context.Context, c *Call, o billing.Outcome, err error) bool {
	id := platform.RequestID(c.ID)
	if err == nil {
		x.Upstreams.Settled(id)
		return false
	}
	logger := x.Logger.With("request_id", c.ID.String())
	if permanent(err) {
		logger.Error("a settlement cannot be written; the cleanup takes it", "err", err)
	} else {
		logger.Warn("a settlement was not written; retrying in the background", "err", err)
	}
	x.bg.Go(func() {
		defer c.Done()
		if !permanent(err) {
			_, err := x.settle(ctx, c, o, time.Now().Add(x.Limits.cfg.SettleBackground))
			if err == nil {
				x.Upstreams.Settled(id)
				return
			}
			logger.Error("a settlement was not written in time; the cleanup takes it", "err", err)
		}
		select {
		case <-x.Upstreams.JournalFlushed(id):
		case <-x.bg.stopped: // the node is going away: its slots go with it
		}
	})
	return true
}

// permanent is a settlement no retry will write: one billing cannot price, one whose reservation is not there, or one
// the database refuses for what it writes (a data or integrity error, or the scope's row-level security) rather than
// for being unable to now.
func permanent(err error) bool {
	if errors.Is(err, billing.ErrNoAttempt) || errors.Is(err, pg.ErrNoRows) {
		return true
	}
	switch class := pg.SQLState(err); {
	case len(class) < 2:
		return false
	case class[:2] == "22", class[:2] == "23", class[:2] == "42":
		return true
	}
	return false
}

// result is what billing takes of an attempt once it is over. The usage is the upstream's report with every item it did
// not report estimated, and the cost is ours of that usage. An attempt known not to have run used nothing and cost
// nothing.
func (a *attempt) result(receipt suppliers.AttemptReceipt) {
	s := a.s
	rec := &s.ends[a.i]
	rec.Key = receipt.Key()
	if rec.Result == billing.NotExecuted {
		return
	}
	d := routing.DemandOf(&s.c.Request)
	d.TextBytes = tokensOf(d.TextBytes, inputBytesPerToken)
	input, _ := billing.InputBound(d, a.entry.Limits)
	rec.Segments = estimated(a.usage, a.seen, input)
	usage := make([]catalog.Usage, len(rec.Segments))
	for i, seg := range rec.Segments {
		usage[i] = seg.Usage
	}
	cost, err := s.x.Costs.Cost(receipt, usage)
	if err != nil {
		// Ours to know, not the user's to pay: reconciliation sets it from the supplier's record, and a settlement
		// held back for it would leave a call that can be priced to the cleanup.
		s.logger.Error("an attempt's cost could not be priced; it is booked as 0", "err", err)
	}
	rec.Cost = cost
}

// estimated is an attempt's usage as billing charges it (docs/domains/billing.md「用量来源与暂估」): every item the
// upstream reported is kept, and the reply's items it did not are estimated, which billing marks: the input by input,
// the first sampling's input with its text at inputBytesPerToken, unless any input-side item was reported; the output
// by the text, reasoning and tool input read at outputBytesPerToken, never below a count reported so far; the web
// searches by the events seen. Fixed ratios stand in for the estimates by model the docs want, which have no data yet;
// each is at most the bound the reservation covers, and a supplier's record corrects it.
func estimated(u protocol.Usage, p protocol.Progress, input int64) []billing.Segment {
	segments := make([]billing.Segment, max(len(u.Segments), 1))
	for i, seg := range u.Segments {
		segments[i] = billing.Segment{Usage: seg.Usage, Reported: seg.Reported}
	}
	reply := &segments[0]
	counts := map[catalog.Item]int64{}
	for it := range catalog.NumItems {
		counts[it] = reply.Usage.Count(it)
	}
	if reply.Reported&inputSide == 0 {
		counts[catalog.ItemInput] = max(counts[catalog.ItemInput], input)
	}
	if !reply.Reported.Has(catalog.ItemOutput) {
		counts[catalog.ItemOutput] = max(reply.Usage.OutputTotal(), tokensOf(p.ContentBytes, outputBytesPerToken)) - counts[catalog.ItemReasoning]
	}
	if !reply.Reported.Has(catalog.ItemWebSearch) {
		counts[catalog.ItemWebSearch] = max(counts[catalog.ItemWebSearch], p.Searches)
	}
	for it, n := range counts {
		counts[it] = min(n, maxCount)
	}
	usage, err := catalog.NewUsage(counts, reply.Usage.ServiceTier(), reply.Usage.Unknown())
	if err != nil {
		panic(err) // every count is within 0 and maxCount, and the tier and items are the usage's own
	}
	reply.Usage = usage
	return segments
}

// The bytes per token of the interim estimate (docs/domains/billing.md「用量来源与暂估」): English runs about 4 bytes a
// token, and output is taken at 3 to lean the guess up, since no record may come to correct it.
const (
	inputBytesPerToken  = 4
	outputBytesPerToken = 3
)

// tokensOf is bytes at perToken bytes a token, rounded up: a token begun is charged.
func tokensOf(bytes, perToken int64) int64 {
	n := bytes / perToken
	if bytes%perToken != 0 {
		n++
	}
	return n
}

// maxCount is the largest count catalog.NewUsage takes: what an estimate is capped at. The bytes of a stream, which an
// output estimate counts, have no bound of their own.
const maxCount = 1e10

// inputSide are the items reported before the reply starts, together: when one of them was, the input is known.
const inputSide = catalog.ItemSet(1<<catalog.ItemInput | 1<<catalog.ItemCacheRead | 1<<catalog.ItemCacheWrite |
	1<<catalog.ItemCacheWrite1h)
