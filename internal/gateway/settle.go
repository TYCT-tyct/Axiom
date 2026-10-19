package gateway

import (
	"context"
	"errors"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
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

// settled follows the settlement up, and reports whether the call's slots are now the background's to give back. One
// that may still be written is retried in the background with the same outcome, until SettleBackground; one that never
// will be, or is given up on, is the cleanup's, which records the request as lost when its reservation runs out. The
// supplier's side of the attempts is written in the settlement's own transaction, so there is nothing else to wait for.
func (x *Executor) settled(ctx context.Context, s *session, o billing.Outcome, err error) bool {
	if err == nil {
		return false
	}
	c, trail := s.c, s.trail()
	logger := x.Logger.With("request_id", c.ID.String())
	if permanent(err) {
		logger.Error("a settlement cannot be written; the cleanup takes it", "err", err)
		return false
	}
	logger.Warn("a settlement was not written; retrying in the background", "err", err)
	x.bg.Go(func() {
		defer c.Done()
		f, err := x.settle(ctx, c, o, time.Now().Add(x.Limits.cfg.SettleBackground))
		if err != nil {
			logger.Error("a settlement was not written in time; the cleanup takes it", "err", err)
			return
		}
		x.log(ctx, trail, o, f, time.Now())
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
// not report estimated, and the cost is ours of that usage in credits and in the supplier's own unit; the receipt ties
// the two to the endpoint the attempt called, and billing writes them to suppliers with the ledger's cost. An attempt
// known not to have run used nothing and cost nothing.
func (a *attempt) result(receipt suppliers.AttemptReceipt) {
	receipt = receipt.WithExchange(a.entry.Exchange)
	s := a.s
	rec := &s.ends[a.i]
	rec.Receipt, rec.FinishedAt = receipt, time.Now()
	if rec.Result == billing.NotExecuted {
		rec.CostKnown = true
		rec.Estimate.Conversion = suppliers.ConversionNotRun
		rec.Estimate.Currency = a.entry.Price.Schedule.Currency()
		return
	}
	rec.Segments = estimated(a.usage, a.seen, routing.DemandOf(&s.c.Request), a.entry.Limits, a.meter.OutputCap())
	usage := make([]catalog.Usage, len(rec.Segments))
	for i, seg := range rec.Segments {
		usage[i] = seg.Usage
	}
	c, credits, known, err := s.x.Costs.Credits(receipt, usage, a.entry.Price.Schedule.Currency())
	if err != nil {
		// Ours to know, not the user's to pay: the attempt is booked at what came back, and the supplier's own record of
		// it is what reconciliation compares that against. Holding the settlement back for it would leave a call that
		// can be priced to the cleanup.
		s.logger.Error("an attempt's cost could not be priced; it is booked at what came back", "err", err)
		c.Flag = suppliers.CostUnpriced // zero for a failure is not a cost known to be zero
	}
	c.Flag = costFlag(rec.Segments, c.Flag)
	// Profit needs the whole cost: one the usage did not fully support (none reported, or a guessed or unpriced item) is
	// not a cost known to be what the supplier charged, whatever it converted to.
	rec.CostKnown = known && err == nil && c.Flag == suppliers.CostComplete
	rec.Cost, rec.Estimate = credits, c
}

// costFlag is what the supplier's row says of how far its estimate can be trusted, decided here, where the source of the
// usage is known (suppliers' Cost sees counts only): reconciliation judges a price only against a complete estimate. An
// attempt whose upstream reported no usage at all has none, whatever the estimates put in its place; one with some
// counts the upstream did not report, or that the schedule did not price, is unpriced; priced is the flag suppliers gave.
func costFlag(segments []billing.Segment, priced suppliers.CostFlag) suppliers.CostFlag {
	reported, guessed := false, false
	for _, seg := range segments {
		reported = reported || seg.Reported != 0
		for it := range catalog.NumItems {
			guessed = guessed || (seg.Usage.Count(it) > 0 && !seg.Reported.Has(it))
		}
	}
	switch {
	case !reported:
		return suppliers.CostNoUsage
	case priced != suppliers.CostComplete:
		return priced
	case guessed:
		return suppliers.CostUnpriced
	}
	return suppliers.CostComplete
}

// estimated is an attempt's usage as billing charges it (docs/domains/billing.md「用量来源与暂估」): every item the
// upstream reported is kept, and the reply's items it did not are estimated, which billing marks: the input by the
// first sampling's estimate (billing.InputEstimate: the text at 4 bytes a token, an image at 1600 tokens, a file at
// 4096, each held to its bound), unless any input-side item was reported; the
// output by the text, reasoning and tool input read at outputBytesPerToken, never below a count reported so far and
// never above the output cap the attempt was quoted at (outputCap, the meter's own, which metering holds the output
// to); the web searches by the events seen. The ratios stand in for the estimates by model the docs want, which have no
// data yet; the bounds and the cap keep an estimate within what the reservation covers. Nothing corrects an estimate
// after the charge in M1: the supplier's record is the platform's cost, not the user's bill.
func estimated(u protocol.Usage, p protocol.Progress, d billing.Demand, m billing.Model, outputCap int64) []billing.Segment {
	input := billing.InputEstimate(d, m)
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
		out := min(tokensOf(p.ContentBytes, outputBytesPerToken), outputCap)
		counts[catalog.ItemOutput] = max(reply.Usage.OutputTotal(), out) - counts[catalog.ItemReasoning]
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

// The bytes per output token of the interim estimate (docs/domains/billing.md「用量来源与暂估」): English runs about 4
// bytes a token, and output is taken at 3 to lean the guess up, since no record may come to correct it. The input's is
// billing.InputBytesPerToken.
const outputBytesPerToken = 3

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
