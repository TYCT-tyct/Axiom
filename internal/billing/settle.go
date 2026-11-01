package billing

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// Suppliers is what billing needs of suppliers, written in billing's transaction: a request's attempts with what each
// cost and the key its charge reconciles by, a lost request with the route it was frozen with, the resolution of one,
// and the end of a request. cmd/ adapts suppliers' functions to it; tests use a double.
type Suppliers interface {
	RecordAttempts(ctx context.Context, tx pg.Tx, attempts []suppliers.AttemptRecord) error
	// RecordLostRequest notes a request whose node was lost, with the route it was frozen with: the endpoints a charge
	// of it would be from, in route order.
	RecordLostRequest(ctx context.Context, tx pg.Tx, request, site, account uuid.UUID, route []uuid.UUID) error
	ResolveLostRequest(ctx context.Context, tx pg.Tx, request uuid.UUID) error
	// EndRequest records that the request reached a terminal state at the database's time at.
	EndRequest(ctx context.Context, tx pg.Tx, request, site, account uuid.UUID, at time.Time) error
	// EndRequests is EndRequest for a batch, in one statement.
	EndRequests(ctx context.Context, tx pg.Tx, ends []suppliers.EndedRequest) error
}

// Outcome is how a call ended: its attempts in order, of which the last is charged. A call whose client left has the
// leaving in the charged attempt's Finish (FinishCancelled), not here: an attempt that failed before the client left is
// judged by how it failed. A call with no attempt is one that never reached an upstream.
type Outcome struct {
	Attempts []AttemptResult
}

// AttemptResult is one attempt: the route entry it bound, its cost to the platform in billionths of a credit
// (suppliers' Cost estimates it), its usage by sampling, how it ended, and the supplier's side of it, which billing
// writes with the cost.
type AttemptResult struct {
	CostKnown  bool
	Seq, Entry int
	Cost       int64
	Segments   []Segment
	Result     Result
	Finish     FinishReason
	// Receipt, Estimate and FinishedAt are what suppliers stores of the attempt: the schedule, endpoint and credential
	// that priced it with the supplier's own charge reference, its cost in the supplier's unit, and when it ended.
	Receipt suppliers.AttemptReceipt
	// Receipts are every supplier request behind a loop attempt, in order; Receipt is the last of them.
	Receipts []suppliers.AttemptReceipt
	// Hops are the same supplier requests with the usage and cost of that hop, so each can be stored and reconciled
	// on its own before the attempt is summarized.
	Hops       []AttemptHop
	Estimate   suppliers.AttemptCost
	FinishedAt time.Time
}

// AttemptHop is one supplier request inside a gateway attempt: a sampling of a model-side loop, or the single request
// of an ordinary attempt.
type AttemptHop struct {
	Segments   []Segment
	Receipt    suppliers.AttemptReceipt
	Estimate   suppliers.AttemptCost
	FinishedAt time.Time
}

// Segment is the usage of one sampling and which of its items the upstream reported; the rest were estimated.
type Segment struct {
	Usage    catalog.Usage
	Reported catalog.ItemSet
}

// Result is what reached the client of an attempt (docs/domains/billing.md「免单规则」). The zero value is the one
// that is charged in full, so a forgotten result never waives a charge.
type Result uint8

// The results.
const (
	Committed          Result = iota // content reached the client
	Aggregated                       // non-streaming, its content returned with the response or the error
	FailedBeforeCommit               // a 2xx that failed before anything was forwarded
	Unknown                          // written, then no headers or usage, or a status that is not a refusal
	NotExecuted                      // known not to have run: not fully written, or refused before execution
)

// FinishReason is why the last attempt stopped; the zero value is none reported.
type FinishReason uint8

// The finish reasons.
const (
	FinishNone FinishReason = iota
	FinishStop
	FinishLength
	FinishToolUse
	FinishRefusal
	FinishError
	FinishTruncated
	FinishTimeout
	FinishCancelled
)

// Final is what the call ends with: the charged attempt's usage, what the user was charged (capped at the floor) and
// whether any of it was estimated or waived.
type Final struct {
	PriceVersion *uuid.UUID
	// Profit is nil if any attempt's converted cost is unknown, or the difference cannot fit an amount.
	Profit   *int64
	Segments []Segment
	Charged  platform.Exact
	// ServerToolCost is the sale price of the per-use items the call ran, nil when none ran. It is a price, not our
	// cost: usage.cost_details.server_tool_cost is what the client paid for tools.
	ServerToolCost *platform.Exact
	// CacheDiscount is what cache reads saved against the input price, never negative.
	CacheDiscount platform.Exact
	Estimated     bool
	Waived        bool // the waiver rules made the call free of its model usage; per-use items may still be charged
}

// ErrNoAttempt is an outcome whose last attempt names no kept entry: nothing can price it.
var ErrNoAttempt = errors.New("billing: the outcome names no attempt on a kept entry")

// Settle charges the call's last attempt and ends its reservation, in one transaction of the admission's scope
// (README.md「结算与账本」): under the balance row lock, the attempts and their costs, the charge by 免单规则 capped at
// the floor with the platform bearing the rest, the counters, the reservation's state, and EndRequest when the state
// is terminal. A retry, or a Settle after cleanup took over, charges at most once: it returns the Final stored on the
// reservation, or settles the difference by the late-settlement rules. An outcome with no attempt is a call that never
// reached an upstream: its reservation is released.
//
// Settle is the single-request settlement. It keeps the reports here rather than going through SettleIn, whose
// per-batch report closure would allocate on a path bench-compare measures.
func (b *Billing) Settle(ctx context.Context, h *Hold, o Outcome) (f Final, err error) {
	a := h.admitted
	var c *locked
	err = b.db.Tx(ctx, a.Scope, func(tx pg.Tx) error {
		var err error
		c, err = b.lock(ctx, tx, h.id, a.Account)
		if err != nil {
			return err
		}
		c.quotes = h.quotes
		f, err = c.settle(ctx, tx, o)
		return err
	})
	if err == nil {
		b.alert(c.seen)
		b.bear(ctx, c.settlement, c.owed, c.charged)
	}
	return f, err
}

// settle branches on what the reservation became (README.md「预留的状态」). A cleanup's states are not among the
// ones that answer with what they stored: cleanup charged nothing, so the usage a late Settle brings is still to be
// charged, and that is what late does.
func (c *locked) settle(ctx context.Context, tx pg.Tx, o Outcome) (Final, error) {
	switch c.row.State {
	case "settled", "released":
		return c.stored()
	}
	if len(o.Attempts) == 0 {
		switch c.row.State {
		case "pending":
			c.costKnown = true // nothing ran, so the cost is a known 0 and so is the profit
			return Final{Profit: c.profit()}, c.release(ctx, tx, "released", c.row.Amount)
		case "cleanup_released":
			// The call never reached an upstream after all, so nothing can be charged to it: cleanup's record of it as
			// lost is answered, and no charge a supplier reports later is looked for in it.
			if err := c.b.suppliers.ResolveLostRequest(ctx, tx, c.id); err != nil {
				return Final{}, fmt.Errorf("billing: resolve the lost request: %w", err)
			}
		}
		return c.stored() // cleanup has it, and a Settle with nothing to price has nothing to add
	}
	if err := c.recordAttempts(ctx, tx, o); err != nil {
		return Final{}, err
	}
	last := o.Attempts[len(o.Attempts)-1]
	if c.row.State == "cleanup_released" { // the usage arrived after all: the request is no longer lost
		if err := c.b.suppliers.ResolveLostRequest(ctx, tx, c.id); err != nil {
			return Final{}, fmt.Errorf("billing: resolve the lost request: %w", err)
		}
	}
	p, err := c.price(last)
	if err == nil {
		err = c.noteAttempts(ctx, tx, o)
	}
	if err != nil {
		return Final{}, err
	}
	if c.row.State == "pending" { // held: amount is still reserved
		err = c.end(ctx, tx, p, last)
	} else { // cleanup_settled or cleanup_released: replace what cleanup charged, at revision -1
		err = c.late(ctx, tx, p, last)
	}
	return Final{
		PriceVersion: lockedVersion(c.row.PriceVersions, last.Entry), Segments: last.Segments,
		Charged: platform.AmountExact(c.charged), ServerToolCost: p.serverToolPtr(), CacheDiscount: p.cacheDiscount,
		Estimated: p.estimated, Waived: p.waived, Profit: c.profit(),
	}, err
}

// recordAttempts books each attempt's cost and writes the attempts to suppliers, in this transaction: the row of an
// attempt and the ledger's cost of it are both there or neither. The entries the attempts bound are kept on the
// reservation, for the next settlement to restore.
func (c *locked) recordAttempts(ctx context.Context, tx pg.Tx, o Outcome) error {
	c.costKnown = true
	records := make([]suppliers.AttemptRecord, 0, len(o.Attempts))
	for _, at := range o.Attempts {
		c.costKnown = c.costKnown && at.CostKnown
		c.cost = sum(c.cost, platform.AmountExact(at.Cost))
		records = append(records, attemptRecords(c, at)...)
		e := c.event("attempt_cost", 0)
		e.seq = *int32p(at.Seq)
		if _, err := post(ctx, tx, e, line{bookSupplierCost, at.Cost, siteCredits}, line{bookSupplierPrepaid, -at.Cost, siteCredits}); err != nil {
			return err
		}
	}
	if err := c.b.suppliers.RecordAttempts(ctx, tx, records); err != nil {
		return fmt.Errorf("billing: record the attempts: %w", err)
	}
	c.entries = attemptEntries(o.Attempts)
	return nil
}

// attemptEntries is attempt_entries: the entry each attempt bound, by attempt number from 1, -1 for a number not among
// attempts.
func attemptEntries(attempts []AttemptResult) []int32 {
	var entries []int32
	for _, at := range attempts {
		for len(entries) < at.Seq {
			entries = append(entries, -1)
		}
		entries[at.Seq-1] = *int32p(at.Entry)
	}
	return entries
}

// priced is what 免单规则 charges: the price, whether it rests on usage the upstream did not report or only a fallback
// price covers, and whether only the per-use items were charged.
type priced struct {
	price             int64
	serverTool        platform.Exact
	cacheDiscount     platform.Exact
	hasServerTool     bool
	estimated, waived bool
	tiers             int16
}

// price applies 免单规则 to the outcome at the locked price of the last attempt's entry. A waivable call is waived, with
// no limit on how many an account may have: the rules are what decides, and nothing else.
func (c *locked) price(last AttemptResult) (priced, error) {
	if last.Result == NotExecuted {
		return priced{tiers: -1}, nil
	}
	s, f, err := c.pricing(last.Entry)
	if err != nil {
		return priced{}, err
	}
	waived := waivable(last)
	p, err := priceSegments(s, f, last.Segments, waived, c.quotes[last.Entry].tiers)
	p.waived = waived
	p.tiers = int16(c.quotes[last.Entry].tiers) //nolint:gosec // a model declares only the four catalog service tiers
	return p, err
}

// waivable: an attempt the platform may not charge for its model usage. One that ended because the client left never
// is, whether or not anything was delivered; the judgement is of the charged attempt, so an attempt that failed first
// and was followed by the client leaving is judged by its failure. One whose content never reached the client is; one
// that did is only when the upstream reported no output and an empty or error finish (the zero-output waiver).
func waivable(last AttemptResult) bool {
	delivered := last.Result == Committed || last.Result == Aggregated
	return last.Finish != FinishCancelled && (!delivered || zeroOutput(last))
}

// deliveredOutputs are the items that show a reply reached the client as billable output: the token outputs and the
// pieces and seconds an image or speech endpoint delivers instead of tokens.
const deliveredOutputs = catalog.OutputSide | 1<<catalog.ItemImageOutPiece | 1<<catalog.ItemAudioOutSecond

// zeroOutput is OpenRouter's zero-completion condition: every sampling reported its output, which with reasoning, audio
// and images, in tokens and in pieces and seconds, adds up to zero, and the finish was empty or an error. An output
// count the upstream never reported is a default zero, not a reported one, so it proves nothing.
func zeroOutput(last AttemptResult) bool {
	var output int64
	for _, s := range last.Segments {
		if !s.Reported.Has(catalog.ItemOutput) {
			return false
		}
		for it := range catalog.NumItems {
			if deliveredOutputs.Has(it) {
				output += s.Usage.Count(it)
			}
		}
	}
	return len(last.Segments) > 0 && output == 0 && (last.Finish == FinishNone || last.Finish == FinishError)
}

// perUseItems are the items charged even when a call is waived (「按次计费的项目照常收费」), the ones a fallback row must
// price for every currency (parseFallbacks), and the ones the listing gate leaves out because admission fills them in
// (Unpriced). Adding an item here adds it to all three. A piece, second or character is not one: it is output the call
// delivers, so a waived call does not charge it.
const perUseItems catalog.ItemSet = 1<<catalog.ItemWebSearch | 1<<catalog.ItemCodeExecution

func chargedItems(perUseOnly bool) catalog.ItemSet {
	if perUseOnly {
		return perUseItems
	}
	return ^catalog.ItemSet(0)
}

func unknownCharged(tokens bool, charged catalog.ItemSet) bool {
	if tokens {
		return charged&catalog.TokenItems != 0
	}
	return charged&perUseItems != 0
}

// priceSegments prices each sampling at its own band, sums them exactly and rounds up once; perUseOnly charges only
// the per-use items, still at the band the whole sampling falls in, as the cost of the sampling less its cost without
// them. s is the schedule admission filled with the per-use defaults. Charged items with usage the upstream did not
// report or the schedule cannot price, or any usage protocol could not map, make the charge estimated; usage protocol
// could not map, and an item the schedule prices nowhere, are charged at their fallback prices
// (docs/domains/billing.md「价目表缺项」).
func priceSegments(s catalog.Schedule, f fallbacks, segments []Segment, perUseOnly bool, tiers catalog.ServiceTierSet) (priced, error) {
	var p priced
	total := platform.Exact{}
	charged := chargedItems(perUseOnly)
	var err error
	for _, seg := range segments {
		if total, err = p.addSegment(total, s, f, seg, perUseOnly, tiers, charged); err != nil {
			return priced{}, fmt.Errorf("billing: price the usage: %w", err)
		}
	}
	request := requestChargeOf(s, f, segments, tiers)
	p.estimated = p.estimated || request.fallback
	if total, err = total.Add(request.amount); err != nil {
		return priced{}, fmt.Errorf("billing: price the request: %w", err)
	}
	p.price = ceilAmount(total)
	return p, nil
}

// addSegment prices one sampling and adds it to the total: the usage at its band, then the per-use items the client's
// usage shows, the unknown usage and the items the schedule prices nowhere, each at its fallback, and reports that the
// charge is estimated when any of it was guessed.
func (p *priced) addSegment(total platform.Exact, s catalog.Schedule, f fallbacks, seg Segment, perUseOnly bool, tiers catalog.ServiceTierSet, charged catalog.ItemSet) (platform.Exact, error) {
	cost, missing, err := segmentCost(s, seg.Usage, perUseOnly)
	if undeclaredTier(tiers, seg.Usage.ServiceTier()) {
		cost, missing, err = platform.Exact{}, ^catalog.ItemSet(0), nil
		p.estimated = true
	}
	if err == nil {
		err = p.addExtras(s, f, seg, tiers, perUseOnly)
	}
	if err == nil {
		cost, err = addFallbacks(s, f, cost, seg.Usage.Unknown(), charged)
	}
	if err == nil {
		cost, err = addMissing(s, f, cost, seg.Usage, missing, charged)
	}
	if err != nil {
		return platform.Exact{}, err
	}
	p.estimated = p.estimated || guessed(seg, charged, missing)
	return total.Add(cost)
}

// addExtras computes the two amounts the client's usage shows beyond its token counts: the sale price of the per-use
// items and what cache reads saved against the input price. Both use the same rate a charge item would, including the
// fallback rate of a tier the schedule does not declare.
func (p *priced) addExtras(s catalog.Schedule, f fallbacks, seg Segment, tiers catalog.ServiceTierSet, waived bool) error {
	var err error
	for it := range catalog.NumItems {
		if !perUseItems.Has(it) {
			continue
		}
		n := seg.Usage.Count(it)
		if n == 0 {
			continue
		}
		p.hasServerTool = true
		if p.serverTool, err = p.serverTool.Add(itemRate(s, f, seg, tiers, it).Mul(n)); err != nil {
			return err
		}
	}
	cached := seg.Usage.Count(catalog.ItemCacheRead)
	if cached == 0 || waived {
		return nil
	}
	input := itemRate(s, f, seg, tiers, catalog.ItemInput)
	cache := itemRate(s, f, seg, tiers, catalog.ItemCacheRead)
	saved, err := input.Mul(cached).Add(cache.Mul(cached).Neg())
	if err != nil || saved.Cmp(platform.Exact{}) <= 0 {
		return err
	}
	p.cacheDiscount, err = p.cacheDiscount.Add(saved)
	return err
}

// itemRate is the rate a charge item uses: the schedule's rate when the tier is declared and priced, else the fallback.
func itemRate(s catalog.Schedule, f fallbacks, seg Segment, tiers catalog.ServiceTierSet, it catalog.Item) platform.UnitPrice {
	rate, _, ok := s.Rate(seg.Usage, it)
	if !ok || undeclaredTier(tiers, seg.Usage.ServiceTier()) {
		rate = missingRate(s, f, it)
	}
	return rate
}

// guessed: the sampling's charge rests on usage the upstream did not report, an item without a price, or usage
// protocol could not map.
func guessed(seg Segment, charged, missing catalog.ItemSet) bool {
	if len(seg.Usage.Unknown()) > 0 {
		return true
	}
	for it := range catalog.NumItems {
		if charged.Has(it) && seg.Usage.Count(it) > 0 && (!seg.Reported.Has(it) || missing.Has(it)) {
			return true
		}
	}
	return false
}

// addFallbacks adds the fallback price of unknown usage within the same charged classes as known items.
func addFallbacks(s catalog.Schedule, f fallbacks, cost platform.Exact, unknown []catalog.UnknownUsage, charged catalog.ItemSet) (platform.Exact, error) {
	for _, u := range unknown {
		price, tokens := unknownCost(s, f, u)
		if !unknownCharged(tokens, charged) {
			continue
		}
		var err error
		if cost, err = cost.Add(price); err != nil {
			return platform.Exact{}, err
		}
	}
	return cost, nil
}

// addMissing adds to cost the fallback price of each charged item the schedule prices nowhere, which Cost leaves out of
// its total rather than failing: an upstream that reports a service tier no price list has, or a price list that leaves
// an item out, is charged at the fallback prices instead of nothing (docs/domains/billing.md「价目表缺项」). A token is
// charged at tokenPrice, the highest price of a token anywhere; a per-use item at its default, the price admission would
// have filled in every tier the schedule offers. Only items with usage are in missing, and charged is what a waiver
// leaves chargeable.
func addMissing(s catalog.Schedule, f fallbacks, cost platform.Exact, u catalog.Usage, missing, charged catalog.ItemSet) (platform.Exact, error) {
	charged &= missing // nothing short of it: the schedule priced what it could and it was all priced
	if charged == 0 {
		return cost, nil
	}
	for it := range catalog.NumItems {
		if !charged.Has(it) {
			continue
		}
		rate := missingRate(s, f, it)
		var err error
		if cost, err = cost.Add(rate.Mul(u.Count(it))); err != nil {
			return platform.Exact{}, err
		}
	}
	return cost, nil
}

// segmentCost is one sampling's cost, of its per-use items alone when perUseOnly, and the items without a price.
func segmentCost(s catalog.Schedule, u catalog.Usage, perUseOnly bool) (platform.Exact, catalog.ItemSet, error) {
	cost, missing, err := s.Cost(u)
	if err != nil || !perUseOnly {
		return cost, missing, err
	}
	rest, err := costWithout(s, u, perUseItems)
	if err == nil {
		cost, err = cost.Add(rest.Neg())
	}
	return cost, missing, err
}

// costWithout is the cost of u with items left out; no per-use item counts toward the band, so the band stays.
func costWithout(s catalog.Schedule, u catalog.Usage, items catalog.ItemSet) (platform.Exact, error) {
	counts := map[catalog.Item]int64{}
	for it := range catalog.NumItems {
		if !items.Has(it) {
			counts[it] = u.Count(it)
		}
	}
	rest, err := u.Recount(counts, nil)
	if err != nil {
		return platform.Exact{}, err
	}
	cost, _, err := s.Cost(rest)
	return cost, err
}
