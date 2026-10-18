package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
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
	Seq, Entry int
	Cost       int64
	Segments   []Segment
	Result     Result
	Finish     FinishReason
	// Receipt, Estimate and FinishedAt are what suppliers stores of the attempt: the schedule, endpoint and credential
	// that priced it with the supplier's own charge reference, its cost in the supplier's unit, and when it ended.
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
	Segments  []Segment
	Charged   platform.Exact
	Estimated bool
	Waived    bool // the waiver rules made the call free of its model usage; per-use items may still be charged
}

// ErrNoAttempt is an outcome whose last attempt names no kept entry: nothing can price it.
var ErrNoAttempt = errors.New("billing: the outcome names no attempt on a kept entry")

// Settle charges the call's last attempt and ends its reservation, in one transaction of the admission's scope
// (README.md「结算与账本」): under the balance row lock, the attempts and their costs, the charge by 免单规则 capped at
// the floor with the platform bearing the rest, the counters, the reservation's state, and EndRequest when the state
// is terminal. A retry, or a Settle after cleanup took over, charges at most once: it returns the Final stored on the
// reservation, or settles the difference by the late-settlement rules. An outcome with no attempt is a call that never
// reached an upstream: its reservation is released.
func (b *Billing) Settle(ctx context.Context, h *Hold, o Outcome) (f Final, err error) {
	a := h.admitted
	var seen []UnknownItem
	var owed, charged int64
	err = b.db.Tx(ctx, a.Scope, func(tx pg.Tx) error {
		c, err := b.lock(ctx, tx, h.id, a.Account)
		if err != nil {
			return err
		}
		f, err = c.settle(ctx, tx, o)
		seen, owed, charged = c.seen, c.owed, c.charged
		return err
	})
	if err == nil {
		b.alert(seen)
		b.bear(ctx, h.id, owed, charged)
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
			return Final{}, c.release(ctx, tx, "released", c.row.Amount)
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
	return Final{Segments: last.Segments, Charged: platform.AmountExact(c.charged), Estimated: p.estimated, Waived: p.waived}, err
}

// recordAttempts books each attempt's cost and writes the attempts to suppliers, in this transaction: the row of an
// attempt and the ledger's cost of it are both there or neither. The entries the attempts bound are kept on the
// reservation, for the next settlement to restore.
func (c *locked) recordAttempts(ctx context.Context, tx pg.Tx, o Outcome) error {
	records := make([]suppliers.AttemptRecord, len(o.Attempts))
	for i, at := range o.Attempts {
		records[i] = suppliers.AttemptRecord{
			Request: platform.RequestID(c.id), Seq: platform.AttemptSeq(at.Seq), //nolint:gosec // at most platform.MaxAttempts
			Site: &c.row.SiteID, Account: &c.row.AccountID,
			Receipt: at.Receipt, Usage: usages(at.Segments), Cost: at.Estimate, FinishedAt: at.FinishedAt,
		}
		e := c.event("attempt_cost", 0)
		e.seq = *int32p(at.Seq)
		if _, err := post(ctx, tx, e, line{bookSupplierCost, at.Cost}, line{bookSupplierPrepaid, -at.Cost}); err != nil {
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
	estimated, waived bool
}

// price applies 免单规则 to the outcome at the locked price of the last attempt's entry. A waivable call is waived, with
// no limit on how many an account may have: the rules are what decides, and nothing else.
func (c *locked) price(last AttemptResult) (priced, error) {
	if last.Result == NotExecuted {
		return priced{}, nil
	}
	s, f, err := c.pricing(last.Entry)
	if err != nil {
		return priced{}, err
	}
	waived := waivable(last)
	p, err := priceSegments(s, f, last.Segments, waived)
	p.waived = waived
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

// zeroOutput is OpenRouter's zero-completion condition: every sampling reported its output, which with reasoning
// adds up to zero, and the finish was empty or an error. An output count the upstream never reported is a default
// zero, not a reported one, so it proves nothing.
func zeroOutput(last AttemptResult) bool {
	var output int64
	for _, s := range last.Segments {
		if !s.Reported.Has(catalog.ItemOutput) {
			return false
		}
		output += s.Usage.OutputTotal()
	}
	return len(last.Segments) > 0 && output == 0 && (last.Finish == FinishNone || last.Finish == FinishError)
}

// perUseItems are the items charged even when a call is waived (「按次计费的项目照常收费」).
const perUseItems catalog.ItemSet = 1<<catalog.ItemWebSearch | 1<<catalog.ItemCodeExecution

// priceSegments prices each sampling at its own band, sums them exactly and rounds up once; perUseOnly charges only
// the per-use items, still at the band the whole sampling falls in, as the cost of the sampling less its cost without
// them. s is the schedule admission filled with the per-use defaults. Charged items with usage the upstream did not
// report or the schedule cannot price, or any usage protocol could not map, make the charge estimated; usage protocol
// could not map is charged at its fallback price (docs/domains/billing.md「价目表缺项」).
func priceSegments(s catalog.Schedule, f fallbacks, segments []Segment, perUseOnly bool) (priced, error) {
	var p priced
	total := platform.Exact{}
	charged := ^catalog.ItemSet(0)
	if perUseOnly {
		charged = perUseItems
	}
	for _, seg := range segments {
		cost, missing, err := segmentCost(s, seg.Usage, perUseOnly)
		if err == nil {
			cost, err = addFallbacks(s, f, cost, seg.Usage.Unknown(), perUseOnly)
		}
		if err == nil {
			total, err = total.Add(cost)
		}
		if err != nil {
			return priced{}, fmt.Errorf("billing: price the usage: %w", err)
		}
		p.estimated = p.estimated || guessed(seg, charged, missing)
	}
	p.price = ceilAmount(total)
	return p, nil
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

// addFallbacks adds to cost the fallback price of each unknown usage. Tokens are model usage, which perUseOnly (a
// waiver) leaves out; anything else is a use, which a waiver still charges.
func addFallbacks(s catalog.Schedule, f fallbacks, cost platform.Exact, unknown []catalog.UnknownUsage, perUseOnly bool) (platform.Exact, error) {
	for _, u := range unknown {
		price, tokens := unknownCost(s, f, u)
		if tokens && perUseOnly {
			continue
		}
		var err error
		if cost, err = cost.Add(price); err != nil {
			return platform.Exact{}, err
		}
	}
	return cost, nil
}

// tokenItems are the items counted in tokens.
const tokenItems = ^perUseItems

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
	rest, err := catalog.NewUsage(counts, u.ServiceTier(), nil)
	if err != nil {
		return platform.Exact{}, err
	}
	cost, _, err := s.Cost(rest)
	return cost, err
}

// pricing is what admission locked for the entry: its schedule, filled with the per-use defaults, and its fallbacks.
func (c *locked) pricing(entry int) (catalog.Schedule, fallbacks, error) {
	r := c.row
	if entry < 0 || entry >= len(r.Prices) || r.Prices[entry] == "" || entry >= len(r.Fallbacks) || r.Fallbacks[entry] == "" {
		return catalog.Schedule{}, fallbacks{}, ErrNoAttempt
	}
	s, err := catalog.ParseSchedule([]byte(r.Prices[entry]))
	if err != nil {
		return catalog.Schedule{}, fallbacks{}, fmt.Errorf("billing: read the locked price: %w", err)
	}
	f, err := parseFallbacks(r.Fallbacks[entry])
	return s, f, err
}

// noteAttempts notes the unknown fields of every attempt that ran, not only the charged one: a model whose upstream
// reported one leaves routing however its attempt ended. An attempt known not to have run used nothing.
func (c *locked) noteAttempts(ctx context.Context, tx pg.Tx, o Outcome) error {
	for _, at := range o.Attempts {
		if at.Result == NotExecuted {
			continue
		}
		if err := c.noteUnknown(ctx, tx, at.Entry, at.Segments); err != nil {
			return err
		}
	}
	return nil
}

// noteUnknown records each field of segments protocol could not map against the entry's model, whichever attempt
// reported it. The first time the model shows a field it leaves routing (「价目表缺项」), and seen collects it for the
// alert once the transaction commits.
func (c *locked) noteUnknown(ctx context.Context, tx pg.Tx, entry int, segments []Segment) error {
	for _, seg := range segments {
		for _, u := range seg.Usage.Unknown() {
			if entry < 0 || entry >= len(c.row.Models) || entry >= len(c.row.Fallbacks) || c.row.Fallbacks[entry] == "" {
				return ErrNoAttempt // admission locks a model on every kept entry, and only kept entries are attempted
			}
			model := c.row.Models[entry]
			n, err := queries.NoteUnknownItem(ctx, tx, NoteUnknownItemParams{
				ModelID: model, Field: u.Field, RequestID: c.id, SiteID: c.row.SiteID, AccountID: c.row.AccountID,
			})
			if err != nil {
				return fmt.Errorf("billing: note the unknown item %s: %w", u.Field, err)
			}
			if n == 1 {
				c.seen = append(c.seen, UnknownItem{Model: catalog.ModelID(model), Field: u.Field, Request: c.id})
			}
		}
	}
	return nil
}

func int32p(n int) *int32 {
	i := int32(n) //nolint:gosec // G115: an attempt or route index, a few at most
	return &i
}

func usages(segments []Segment) []catalog.Usage {
	u := make([]catalog.Usage, len(segments))
	for i, s := range segments {
		u[i] = s.Usage
	}
	return u
}

// storedSegment is a segment as final_usage stores it: each item's count in item order, the service tier, the
// reported items and the usage protocol could not map.
type storedSegment struct {
	Counts   []int64                `json:"counts"`
	Tier     catalog.ServiceTier    `json:"tier"`
	Reported catalog.ItemSet        `json:"reported"`
	Unknown  []catalog.UnknownUsage `json:"unknown,omitzero"`
}

// encodeUsage is the segments as final_usage stores them, so a retried Settle returns the Final the first one gave.
func encodeUsage(segments []Segment) ([]byte, error) {
	stored := make([]storedSegment, len(segments))
	for i, s := range segments {
		counts := make([]int64, catalog.NumItems)
		for it := range catalog.NumItems {
			counts[it] = s.Usage.Count(it)
		}
		stored[i] = storedSegment{Counts: counts, Tier: s.Usage.ServiceTier(), Reported: s.Reported, Unknown: s.Usage.Unknown()}
	}
	b, err := jsonx.MarshalCanonical(stored)
	if err != nil {
		return nil, fmt.Errorf("billing: write the usage: %w", err)
	}
	return b, nil
}

func decodeUsage(b []byte) ([]Segment, error) {
	var stored []storedSegment
	if err := jsonx.UnmarshalKnown(b, &stored); err != nil {
		return nil, fmt.Errorf("billing: read the stored usage: %w", err)
	}
	segments := make([]Segment, len(stored))
	for i, s := range stored {
		counts := map[catalog.Item]int64{}
		for it, n := range s.Counts {
			counts[catalog.Item(it)] = n
		}
		u, err := catalog.NewUsage(counts, s.Tier, s.Unknown)
		if err != nil {
			return nil, fmt.Errorf("billing: read the stored usage: %w", err)
		}
		segments[i] = Segment{Usage: u, Reported: s.Reported}
	}
	return segments, nil
}

// locked is one reservation under its balance row lock, with what is charged once a branch has run.
type locked struct {
	b       *Billing
	id      uuid.UUID
	row     LockReservationRow
	balance LockBalanceRow
	charged int64
	owed    int64         // what this transaction's charge asked for before the floor capped it; 0 when it charged nothing
	seen    []UnknownItem // unknown items first seen in this transaction
	entries []int32       // the attempts' entries to write, nil to keep the recorded ones
}

// stored is the Final the reservation holds.
func (c *locked) stored() (Final, error) {
	segments, err := decodeUsage(c.row.FinalUsage)
	return Final{Segments: segments, Charged: platform.AmountExact(c.row.Charged), Estimated: c.row.Estimated, Waived: c.row.Waived}, err
}

// cap is what may be charged without passing the floor, keeping every other reservation covered: the balance less
// the floor, less what is reserved apart from what this reservation still holds. It is computed exactly, as Reserve's
// headroom is, and clamped to what an amount can hold.
func (c *locked) cap(held int64) int64 {
	b := c.balance
	left := sum(platform.AmountExact(b.Balance), platform.AmountExact(b.Floor).Neg(), platform.AmountExact(b.Reserved).Neg(),
		platform.AmountExact(held))
	if left.Cmp(platform.Exact{}) < 0 {
		return 0
	}
	return ceilAmount(left)
}

// ceilAmount is e in billionths, rounded up, at most math.MaxInt64. Valid settings can price past what an amount
// holds, such as 10¹⁰ unknown uses at a default of one credit each; what the account pays is capped by its balance
// anyway, so saturating loses nothing a charge could collect.
func ceilAmount(e platform.Exact) int64 {
	if e.Cmp(platform.AmountExact(math.MaxInt64)) > 0 {
		return math.MaxInt64
	}
	return e.CeilAmount()
}

func (c *locked) event(kind string, revision int64) event {
	return event{site: c.row.SiteID, account: c.row.AccountID, kind: kind, request: c.id, revision: revision}
}

// end settles a held reservation: price capped at the floor, the platform bearing the rest, and the rest of the
// reservation given back at once. An estimated charge is released like any other: nothing may correct it later, so
// holding part of the reservation for a record that will not come would only shrink what the account may spend.
func (c *locked) end(ctx context.Context, tx pg.Tx, p priced, last AttemptResult) error {
	held := c.row.Amount - c.row.Charged
	c.owed, c.charged = p.price, min(p.price, c.cap(held))
	if _, err := post(ctx, tx, c.event("settle", 0), charge(p.price, c.charged)...); err != nil {
		return err
	}
	w, err := result("settled", p, last)
	if err != nil {
		return err
	}
	if err := c.move(ctx, tx, -held, c.charged); err != nil {
		return err
	}
	return c.write(ctx, tx, w)
}

// late replaces what cleanup charged with what the full outcome charges: a refund, or a makeup capped at the floor.
func (c *locked) late(ctx context.Context, tx pg.Tx, p priced, last AttemptResult) error {
	c.owed, c.charged = p.price, min(p.price, add(c.row.Charged, c.cap(0)))
	if _, err := post(ctx, tx, c.event("reverse", lateRevision), reversal(c.row.Price, c.row.Charged)...); err != nil {
		return err
	}
	if _, err := post(ctx, tx, c.event("settle", lateRevision), charge(p.price, c.charged)...); err != nil {
		return err
	}
	if err := c.move(ctx, tx, 0, c.charged-c.row.Charged); err != nil {
		return err
	}
	w, err := result("settled", p, last)
	if err != nil {
		return err
	}
	return c.write(ctx, tx, w)
}

// release ends a reservation in state, giving back what it held and keeping what was charged.
func (c *locked) release(ctx context.Context, tx pg.Tx, state string, held int64) error {
	if err := c.move(ctx, tx, -max(held, 0), 0); err != nil {
		return err
	}
	return c.write(ctx, tx, c.kept(state))
}

func (c *locked) move(ctx context.Context, tx pg.Tx, reserved, spent int64) error {
	if err := queries.MoveCounters(ctx, tx, MoveCountersParams{
		Reserved: reserved, Spent: spent, AccountID: c.row.AccountID, WorkspaceID: c.row.WorkspaceID,
		WorkspaceWindow: c.row.WorkspaceWindow, KeyID: c.row.KeyID, KeyWindow: c.row.KeyWindow,
	}); err != nil {
		return fmt.Errorf("billing: move the counters: %w", err)
	}
	return nil
}

// written is what write records on the reservation.
type written struct {
	state             string
	seq, entry        *int32 // nil keeps the recorded ones
	price             int64
	waived, estimated bool
	usage             []byte
}

// result is what a charge for the last attempt writes.
func result(state string, p priced, last AttemptResult) (written, error) {
	usage, err := encodeUsage(last.Segments)
	return written{
		state: state, seq: int32p(last.Seq), entry: int32p(last.Entry), price: p.price, waived: p.waived,
		estimated: p.estimated, usage: usage,
	}, err
}

// kept moves the reservation to state and keeps what it records.
func (c *locked) kept(state string) written {
	r := c.row
	return written{
		state: state, price: r.Price, waived: r.Waived, estimated: r.Estimated, usage: r.FinalUsage,
	}
}

// terminal are the states nothing but a late Settle moves on from: suppliers may let the request's records go.
var terminal = map[string]bool{"settled": true, "released": true, "cleanup_settled": true, "cleanup_released": true}

// write records w on the reservation, and the end of the request when its state is terminal, at the same moment of
// the database's clock.
func (c *locked) write(ctx context.Context, tx pg.Tx, w written) error {
	at, err := queries.EndReservation(ctx, tx, EndReservationParams{
		RequestID: c.id, State: w.state, ChargedSeq: w.seq, Entry: w.entry, Charged: c.charged, Price: w.price,
		Waived: w.waived, Estimated: w.estimated, FinalUsage: w.usage, AttemptEntries: c.entries,
	})
	if err != nil {
		return fmt.Errorf("billing: end the reservation: %w", err)
	}
	if terminal[w.state] {
		if err := c.b.suppliers.EndRequest(ctx, tx, c.id, c.row.SiteID, c.row.AccountID, at); err != nil {
			return fmt.Errorf("billing: end the request: %w", err)
		}
	}
	return nil
}
