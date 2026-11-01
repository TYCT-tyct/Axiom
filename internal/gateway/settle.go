package gateway

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	err = x.Outstanding.retry(until, permanent, func() error {
		ctx, cancel := x.Outstanding.settlementContext(ctx, settleAttempt)
		defer cancel()
		return x.withSettlement(ctx, c, o, &f)
	})
	return f, err
}

// completionItem is one money write in a completion batch. work writes it on the batch's transaction and returns the
// reports to run after the commit; after holds them until then.
type completionItem struct {
	gate         gateItem
	scope        pg.Scope
	work         func(context.Context, pg.Tx) (func(), error)
	after        func()
	settlement   billing.Settlement // valid when isSettlement
	isSettlement bool
	final        *billing.Final
}

// withFunds runs one of the call's money writes behind its account's gate, of the completion class. The write joins
// the account's completions in one transaction (README.md#账户闸): one begin, one commit and one connection for the
// batch, with the reservation and ledger rows still making every retry charge at most once.
func (x *Executor) withFunds(ctx context.Context, c *Call, write func(context.Context, pg.Tx) (func(), error)) error {
	item := &completionItem{scope: c.Admitted.Scope, work: write}
	item.gate.done, item.gate.payload = make(chan struct{}), item
	if err := x.Limits.Funds.Complete(ctx, accountKey(c.caller), &item.gate, x.runCompletions); err != nil {
		return err
	}
	return item.gate.err
}

// runCompletions writes a batch of the account's completions in one transaction, then runs their reports. One write's
// error rolls the transaction back, so none commits; the gateway retries every request in the batch, and the rows make
// the retry idempotent. A report is run only after the commit, because it describes what the ledger now holds.
func (x *Executor) runCompletions(ctx context.Context, batch []*gateItem) error {
	// One settlement uses the single path: its writes are already one order, and the batch's set-based statements
	// cost more than the round trips they save at one item.
	if len(batch) > 1 {
		if items, ok := settlementBatch(batch); ok {
			return runSettlementBatch(pg.CompletionContext(ctx), batch, items, x.Ledger.SettleBatch)
		}
	}
	return runCompletionBatch(pg.CompletionContext(ctx), batch, x.Ledger.RunBatch)
}

// withSettlement queues a settlement behind the account's gate. Its work is the single-settlement path, which the
// batch uses when it cannot take the whole batch through SettleBatch; the batch path itself writes every item at once
// and leaves the Final in item.final. The result is returned only after the write commits.
func (x *Executor) withSettlement(ctx context.Context, c *Call, o billing.Outcome, f *billing.Final) error {
	item := &completionItem{
		scope:        c.Admitted.Scope,
		settlement:   billing.Settlement{Hold: c.Hold, Outcome: o},
		isSettlement: true,
		final:        f,
	}
	item.work = func(ctx context.Context, tx pg.Tx) (func(), error) {
		final, after, err := x.Ledger.SettleIn(ctx, tx, c.Hold, o)
		if err == nil {
			*f = final
		}
		return after, err
	}
	item.gate.done, item.gate.payload = make(chan struct{}), item
	if err := x.Limits.Funds.Complete(ctx, accountKey(c.caller), &item.gate, x.runCompletions); err != nil {
		return err
	}
	return item.gate.err
}

// settlementBatch is the batch's settlements when every item is one: an extension or an orphan in the batch keeps the
// single-transaction path, whose works each item already has.
func settlementBatch(batch []*gateItem) ([]billing.Settlement, bool) {
	items := make([]billing.Settlement, len(batch))
	for i, g := range batch {
		item := g.payload.(*completionItem)
		if !item.isSettlement {
			return nil, false
		}
		items[i] = item.settlement
	}
	return items, true
}

// runCompletionBatch writes the batch's works in one transaction of their account's scope, then runs each item's
// reports. A work that fails rolls the transaction back; its error goes only to that item, and the others are retried
// in a new transaction under their own deadlines. A transaction or commit error has no work to name, so it still
// applies to every item and the caller retries them. Reports run only after their transaction commits.
func runCompletionBatch(ctx context.Context, batch []*gateItem, run func(context.Context, pg.Scope, []func(pg.Tx) error) error) error {
	failed, err := writeCompletions(ctx, batch, run)
	if failed < 0 {
		return err
	}
	item := batch[failed]
	item.err = err
	if !item.closed {
		// Return the failed request before retrying the others: an extension's landing and cancel must not wait for a
		// slow settlement or extension in the rest of the batch. This runs after the rollback, so the failed request
		// has no committed write.
		item.closed = true
		close(item.done)
	}
	rest := slices.Delete(slices.Clone(batch), failed, failed+1) // the caller's batch still gives every item its result
	if len(rest) == 0 {
		return nil
	}
	// The next transaction keeps ctx's values but only the remaining requests' deadlines; the failed one's may already
	// have passed.
	retry, cancel := earliestDeadline(context.WithoutCancel(ctx), rest)
	defer cancel()
	return runCompletionBatch(retry, rest, run)
}

// writeCompletions runs the batch's works in one transaction and, once it commits, their reports. When a work's error
// rolled the transaction back it returns that work's index and error; otherwise -1 and the transaction's own error.
func writeCompletions(ctx context.Context, batch []*gateItem, run func(context.Context, pg.Scope, []func(pg.Tx) error) error) (int, error) {
	items := make([]*completionItem, len(batch))
	works := make([]func(pg.Tx) error, len(batch))
	for i, g := range batch {
		item := g.payload.(*completionItem)
		item.after, items[i] = nil, item
		works[i] = func(tx pg.Tx) error {
			after, err := item.work(ctx, tx)
			if err == nil {
				item.after = after
			}
			return err
		}
	}
	err := run(ctx, items[0].scope, works)
	if failed, ok := errors.AsType[*billing.BatchWorkError](err); ok && failed.Index >= 0 && failed.Index < len(batch) {
		return failed.Index, failed.Err
	}
	if err == nil {
		for _, item := range items {
			if item.after != nil {
				item.after()
			}
		}
	}
	return -1, err
}

// runSettlementBatch is runCompletionBatch for a batch of settlements written by SettleBatch. An error that names an
// item fails only that item and retries the rest; the others come back to every item, which retries as its own
// single settlement. Reports run only after SettleBatch commits.
func runSettlementBatch(ctx context.Context, batch []*gateItem, items []billing.Settlement, run func(context.Context, pg.Scope, []billing.Settlement) ([]billing.Final, []func(), error)) error {
	failed, err := writeSettlements(ctx, batch, items, run)
	if failed < 0 {
		return err
	}
	item := batch[failed]
	item.err = err
	if !item.closed {
		item.closed = true
		close(item.done)
	}
	rest := slices.Delete(slices.Clone(batch), failed, failed+1)
	if len(rest) == 0 {
		return nil
	}
	retry, cancel := earliestDeadline(context.WithoutCancel(ctx), rest)
	defer cancel()
	settlements, ok := settlementBatch(rest)
	if !ok {
		return errors.New("gateway: a settlement left the settlement batch")
	}
	return runSettlementBatch(retry, rest, settlements, run)
}

// writeSettlements runs the batch's settlements in one transaction and, after it commits, their reports. An error that
// names an item rolls the transaction back and returns that item's index; otherwise the error is the transaction's.
func writeSettlements(ctx context.Context, batch []*gateItem, items []billing.Settlement, run func(context.Context, pg.Scope, []billing.Settlement) ([]billing.Final, []func(), error)) (int, error) {
	for i, g := range batch {
		g.payload.(*completionItem).after = nil
		if items[i].Hold == nil {
			return i, errors.New("gateway: settlement without a hold")
		}
	}
	finals, afters, err := run(ctx, batch[0].payload.(*completionItem).scope, items)
	if failed, ok := errors.AsType[*billing.BatchWorkError](err); ok && failed.Index >= 0 && failed.Index < len(batch) {
		return failed.Index, failed.Err
	}
	if err != nil {
		return -1, err
	}
	if len(finals) != len(batch) || len(afters) != len(batch) {
		return -1, fmt.Errorf("gateway: settled %d items with %d finals and %d reports", len(batch), len(finals), len(afters))
	}
	for i, item := range batch {
		ci := item.payload.(*completionItem)
		*ci.final = finals[i]
		if afters[i] != nil {
			afters[i]()
		}
	}
	return -1, nil
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
	x.Outstanding.Go(func() {
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
	if len(a.hops) > 0 {
		// The model-side loop sampled the model once per hop; each hop's usage is its own segment (docs/domains/billing.md
		// 「预留与结算」).
		rec.Segments = a.hops
	} else {
		rec.Segments = estimated(a.usage, a.seen, a.demand(), a.entry.Limits, a.meter.OutputCap(),
			billing.TokenizerOf(a.entry.Target.Model))
	}
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

// addSearchUsage adds the call's external searches and file parses to the user's side of the charged attempt after the
// supplier's cost was computed from the supplier's own usage. The work is ours, not the supplier's: one explicit zero
// report cannot hide it, and a retry does not put it in another attempt's supplier price. Only the last attempt is
// charged (billing.Settle), so only that one carries the count.
func addSearchUsage(rec *billing.AttemptResult, searches int64) {
	if searches <= 0 || len(rec.Segments) == 0 {
		return
	}
	counts := make(map[catalog.Item]int64, catalog.NumItems)
	for it := range catalog.NumItems {
		counts[it] = rec.Segments[0].Usage.Count(it)
	}
	counts[catalog.ItemWebSearch] += searches
	rec.Segments[0].Usage = recountCapped(rec.Segments[0].Usage, counts)
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

// demand is the request as billing estimates it: what the request says, and its text counted in the tokens of the
// entry's model family when that family has a count and the reply reported none of the input, which is the only
// case the count is used in.
func (a *attempt) demand() billing.Demand {
	d := routing.DemandOf(&a.s.c.Request)
	if inputReported(a.usage) {
		return d
	}
	tok := billing.TokenizerOf(a.entry.Target.Model)
	if tok == billing.TokenizerNone {
		return d
	}
	cached := &a.s.textCounts[int(tok)-1]
	if !cached.counted {
		var err error
		r := &a.s.c.Request
		unseen := r.TokenInputs + tokensOf(r.RepeatedTextBytes, billing.InputBytesPerToken)
		cached.tokens, err = textTokens(a.s.c.Body, d, tok, unseen)
		cached.counted = true
		if err != nil {
			a.s.logger.Warn("input token count failed; using byte estimate", "err", err)
		}
	}
	d.TextTokens = cached.tokens
	return d
}

type textEstimate struct {
	tokens  int64
	counted bool
}

// inputReported keeps input/cache reports authoritative, including explicitly reported zero counts.
func inputReported(u protocol.Usage) bool {
	return len(u.Segments) > 0 && u.Segments[0].Reported&catalog.InputSide != 0
}

// maxCountBytes bounds per-family BPE and unquoting work, independently of a route's context window. The cached count
// can therefore serve another attempt of the same family with a different context limit.
const maxCountBytes = 256 << 10

// textTokens counts the body's strings in tok's tokens and adds unseen, the input tokens those strings do not show
// (protocol.Request.TokenInputs and RepeatedTextBytes).
func textTokens(body []byte, d billing.Demand, tok billing.Tokenizer, unseen int64) (int64, error) {
	c := billing.NewTextCount(tok, d.TextBytes)
	if c == nil {
		return 0, nil
	}
	remaining, err := protocol.Text(body, maxCountBytes, c.Add)
	if err != nil {
		return 0, fmt.Errorf("extract input text: %w", err)
	}
	count, err := c.Total()
	if err != nil {
		return 0, err
	}
	return min(d.TextBytes, count+tokensOf(remaining, billing.InputBytesPerToken)+unseen), nil
}

// estimated is an attempt's usage as billing charges it (docs/domains/billing.md「用量来源与暂估」): every item the
// upstream reported is kept, and the reply's items it did not are estimated, which billing marks: the input by the first
// sampling's estimate (billing.InputEstimate: the text at the count of the endpoint's model family where it has one
// and at 4 bytes a token otherwise, an image at 1600 tokens, a file at 4096, each held to its bound), unless
// any input-side item was reported, in which case the estimate stands in place of the items an invalid report kept
// rather than beside them; the output by the text, reasoning and tool input read, at the tokens per byte of their
// shape in tok's weights (billing.TextTokens) and at outputBytesPerToken for a family without, never below a count
// reported so far and never above the output cap the attempt was quoted at (outputCap, the meter's own, which metering
// holds the output to); the web searches by the events seen; and the pieces, seconds and characters by the request's own
// counts, since it was reserved for them. The bounds and the cap keep an estimate within what the reservation covers.
// Nothing corrects an estimate after the charge in M1: the supplier's record is the platform's cost, not the user's
// bill.
func estimated(u protocol.Usage, p protocol.Progress, d billing.Demand, m billing.Model, outputCap int64,
	tok billing.Tokenizer,
) []billing.Segment {
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
	if !inputReported(u) { // none of the input was reported, so it is estimated
		estimateInput(counts, input)
	}
	counts[catalog.ItemOutput] = estimateOutput(*reply, p, outputCap, tok)
	if !reply.Reported.Has(catalog.ItemWebSearch) {
		counts[catalog.ItemWebSearch] = max(counts[catalog.ItemWebSearch], p.Searches)
	}
	for it, n := range d.Counts {
		if catalog.CountedItems.Has(it) && !reply.Reported.Has(it) {
			counts[it] = max(counts[it], n)
		}
	}
	reply.Usage = recountCapped(reply.Usage, counts)
	if n, ok := reply.Usage.PhysicalInput(); ok && !inputReported(u) && input > n {
		// The prompt the estimate stands for is longer than the physical total the invalid report kept: the band and
		// the client's echo read the physical total, and leaving it short would price the estimate in a lower band.
		usage, err := reply.Usage.WithPhysicalInput(min(input, maxCount))
		if err != nil {
			panic(err) // the estimate is held to maxCount above
		}
		reply.Usage = usage
	}
	return segments
}

// estimateInput adds to the plain input what the kept input-side counts are short of the prompt's estimate: the estimate
// is the whole prompt, and a report accepted before a count went down keeps the counts it took, which are parts of one
// prompt, a cache read even overlapping the plain input. So the estimate stands in place of those counts rather than
// beside them (#339). The shortfall is over the items' sum, not InputTotal: the physical total an OpenAI report carries
// is the prompt's own length, and cache reads and writes within it may overlap each other and it.
func estimateInput(counts map[catalog.Item]int64, estimate int64) {
	kept := int64(0)
	for it := range catalog.NumItems {
		if catalog.InputSide.Has(it) {
			kept += counts[it]
		}
	}
	counts[catalog.ItemInput] += max(estimate-kept, 0)
}

// estimateOutput is the reply's ordinary output: the text, reasoning and tool input read, at the tokens per byte their
// shape takes in tok's weights or at outputBytesPerToken for a family without, never below a count reported so far and
// never above the output cap the attempt was quoted at (outputCap, the meter's own, which metering holds the output
// to). A report that carried it keeps it.
func estimateOutput(reply billing.Segment, p protocol.Progress, outputCap int64, tok billing.Tokenizer) int64 {
	if reply.Reported.Has(catalog.ItemOutput) {
		return reply.Usage.Count(catalog.ItemOutput)
	}
	content := max(p.ContentBytes, 0)
	out, ok := billing.TextTokens(tok, p.Text, content)
	if !ok {
		out = tokensOf(content, outputBytesPerToken)
	}
	out += min(max(p.ToolCalls, 0), maxCount) * billing.ToolCallTokens(tok)
	out = min(out, outputCap)
	return max(reply.Usage.OutputTotal(), out) - reply.Usage.Count(catalog.ItemReasoning) -
		reply.Usage.Count(catalog.ItemAudioOut) - reply.Usage.Count(catalog.ItemImageOut)
}

// recountCapped holds every count to maxCount, the largest catalog.NewUsage takes, and recounts the reply with its own
// tier and unknown counts. A count can arrive past the cap from the report itself or from an estimate set beside it, and
// the recount refuses one: the cap is where an unrepresentable count stops instead of failing the settlement.
func recountCapped(reply catalog.Usage, counts map[catalog.Item]int64) catalog.Usage {
	for it, n := range counts {
		counts[it] = min(n, maxCount)
	}
	usage, err := reply.Recount(counts, reply.Unknown())
	if err != nil {
		panic(err) // every count is within 0 and maxCount, and the tier and items are the usage's own
	}
	return usage
}

// The bytes per output token of the interim estimate for a family without weights (docs/domains/billing.md「用量来源与
// 暂估」): English runs about 4 bytes a token, and output is taken at 3 to lean the guess up, since no record may come
// to correct it. The input's is billing.InputBytesPerToken for a family whose tokenizer counted nothing.
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
