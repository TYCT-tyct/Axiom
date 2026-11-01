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
	records := make([]suppliers.AttemptRecord, len(o.Attempts))
	for i, at := range o.Attempts {
		c.costKnown = c.costKnown && at.CostKnown
		c.cost = sum(c.cost, platform.AmountExact(at.Cost))
		records[i] = suppliers.AttemptRecord{
			Request: platform.RequestID(c.id), Seq: platform.AttemptSeq(at.Seq), //nolint:gosec // at most platform.MaxAttempts
			Site: c.row.SiteID, Account: c.row.AccountID,
			Receipt: at.Receipt, Usage: usages(at.Segments), Cost: at.Estimate, FinishedAt: at.FinishedAt,
		}
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

func int32p(n int) *int32 {
	i := int32(n) //nolint:gosec // G115: an attempt or route index, a few at most
	return &i
}

// locked is one reservation under its balance row lock, with what is charged once a branch has run.
type locked struct {
	quotes     []quote // only Settle brings usage, always with the original Hold, including after cleanup
	cost       platform.Exact
	costKnown  bool
	b          *Billing
	id         uuid.UUID
	row        LockReservationRow
	balance    LockBalanceRow
	charged    int64
	owed       int64         // what this transaction's charge asked for before the floor capped it; 0 when it charged nothing
	settlement event         // the exact ledger event used by the post-commit loss notice
	seen       []UnknownItem // unknown items first seen in this transaction
	entries    []int32       // the attempts' entries to write, nil to keep the recorded ones
}

// stored is the Final the reservation holds.
func (c *locked) stored() (Final, error) {
	segments, err := decodeUsage(c.row.FinalUsage)
	f := Final{
		PriceVersion: settledVersion(c.row.PriceVersions, c.row.Entry), Segments: segments,
		Charged: platform.AmountExact(c.row.Charged), Estimated: c.row.Estimated, Waived: c.row.Waived, Profit: c.row.Profit,
	}
	if err != nil || c.row.State != "settled" || c.row.Entry == nil || c.row.PricingTiers < 0 {
		return f, err
	}
	s, fbs, err := c.pricing(int(*c.row.Entry))
	if err != nil {
		return f, err
	}
	p, err := priceSegments(s, fbs, segments, c.row.Waived, catalog.ServiceTierSet(c.row.PricingTiers)) // stored CHECK bounds tiers
	if err != nil {
		return f, err
	}
	f.ServerToolCost, f.CacheDiscount = p.serverToolPtr(), p.cacheDiscount
	return f, nil
}

func (c *locked) profit() *int64 {
	if !c.costKnown {
		return nil
	}
	difference := sum(platform.AmountExact(c.charged), c.cost.Neg())
	if difference.Cmp(platform.AmountExact(math.MinInt64)) < 0 || difference.Cmp(platform.AmountExact(math.MaxInt64)) > 0 {
		return nil
	}
	n := difference.CeilAmount()
	return &n
}

// cap is what may be charged without passing the floor, keeping every other reservation covered: the balance less
// the floor and frozen credit, less what is reserved apart from what this reservation still holds. Like admission,
// it computes headroom exactly and clamps it to what an amount can hold.
func (c *locked) cap(held int64) int64 { return room(c.balance, held) }

// room is how far the balance may fall while covering frozen credit and reservations other than held. Charges use
// the floor; refunds and expiry use zero, so they cannot put the account in debt.
func room(b LockBalanceRow, held int64) int64 {
	left := sum(platform.AmountExact(b.Balance), platform.AmountExact(b.Floor).Neg(), platform.AmountExact(b.Reserved).Neg(),
		platform.AmountExact(b.Frozen).Neg(), platform.AmountExact(held))
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
	c.settlement = c.event("settle", 0)
	var lines []line
	if c.plan() { // the window froze the whole hold and pays what was charged, up to it
		c.owed, c.charged = p.price, min(p.price, c.row.Amount)
		lines = planCharge(p.price, c.charged)
	} else {
		c.owed, c.charged = p.price, min(p.price, c.cap(held))
		lines = charge(p.price, c.charged)
	}
	if _, err := post(ctx, tx, c.settlement, lines...); err != nil {
		return err
	}
	w, err := result(p, last)
	if err != nil {
		return err
	}
	if c.plan() {
		return c.write(ctx, tx, w, 0, -c.row.Amount, c.charged, &windowSettle{held: c.row.Amount, charged: c.charged})
	}
	return c.write(ctx, tx, w, -held, -held, c.charged, nil)
}

// late replaces what cleanup charged with what the full outcome charges: a refund, or a makeup capped at the floor.
func (c *locked) late(ctx context.Context, tx pg.Tx, p priced, last AttemptResult) error {
	// A plan hold cleanup released has no window left to charge: its credit went back and may have lapsed, so the
	// usage is the wallet's to pay, as a wallet hold's late settlement is (docs/domains/billing.md「订阅套餐」).
	c.owed, c.charged = p.price, min(p.price, add(c.row.Charged, c.cap(0)))
	if _, err := post(ctx, tx, c.event("reverse", lateRevision), reversal(c.row.Price, c.row.Charged)...); err != nil {
		return err
	}
	c.settlement = c.event("settle", lateRevision)
	if _, err := post(ctx, tx, c.settlement, charge(p.price, c.charged)...); err != nil {
		return err
	}
	w, err := result(p, last)
	if err != nil {
		return err
	}
	w.wallet = true // the window's hold is gone and its credit may have lapsed: the wallet pays, and the row says so
	return c.write(ctx, tx, w, 0, 0, c.charged-c.row.Charged, nil)
}

// release ends a reservation in state, giving back what it held and keeping what was charged.
func (c *locked) release(ctx context.Context, tx pg.Tx, state string, held int64) error {
	if c.plan() {
		if held > 0 {
			if err := moveWindowReserved(ctx, tx, *c.row.SubscriptionID, *c.row.WindowSeq, -held); err != nil {
				return err
			}
		}
		return c.write(ctx, tx, c.kept(state), 0, -max(held, 0), 0, nil)
	}
	return c.write(ctx, tx, c.kept(state), -max(held, 0), -max(held, 0), 0, nil)
}

// plan reports whether the reservation's money was frozen in a subscription window.
func (c *locked) plan() bool { return c.row.Funding == "plan" }

// windowSettle is what a plan settlement moves in its frozen window: the hold goes back and the charge is used.
type windowSettle struct{ held, charged int64 }

// written is what write records on the reservation.
type written struct {
	state             string
	seq, entry        *int32 // nil keeps the recorded ones
	price             int64
	waived, estimated bool
	usage             []byte
	tiers             int16
	wallet            bool // the settlement paid from the wallet though the reservation was frozen in a plan window
}

// result is what a charge for the last attempt writes: the reservation is settled.
func result(p priced, last AttemptResult) (written, error) {
	usage, err := encodeUsage(last.Segments)
	return written{
		state: "settled", seq: int32p(last.Seq), entry: int32p(last.Entry), price: p.price, waived: p.waived,
		estimated: p.estimated, usage: usage, tiers: p.tiers,
	}, err
}

// kept moves the reservation to state and keeps what it records.
func (c *locked) kept(state string) written {
	r := c.row
	return written{
		state: state, price: r.Price, waived: r.Waived, estimated: r.Estimated, usage: r.FinalUsage, tiers: r.PricingTiers,
	}
}

// terminal are the states nothing but a late Settle moves on from: suppliers may let the request's records go.
var terminal = map[string]bool{"settled": true, "released": true, "cleanup_settled": true, "cleanup_released": true}

// write records w on the reservation, and the end of the request when its state is terminal, at the same moment of
// the database's clock.
func (c *locked) write(ctx context.Context, tx pg.Tx, w written, balanceReserved, spendReserved, spent int64, win *windowSettle) error {
	if w.wallet {
		if err := markLateWallets(ctx, tx, []*reservationEnd{{c: c, w: w}}); err != nil {
			return err
		}
	}
	at, err := queries.EndReservation(ctx, tx, EndReservationParams{
		Profit: c.profit(), BalanceReserved: balanceReserved, SpendReserved: spendReserved, Spent: spent, AccountID: c.row.AccountID,
		WorkspaceID: c.row.WorkspaceID, WorkspaceWindow: c.row.WorkspaceWindow, KeyID: c.row.KeyID, KeyWindow: c.row.KeyWindow,
		RequestID: c.id, State: w.state, ChargedSeq: w.seq, Entry: w.entry, Charged: c.charged, Price: w.price,
		Waived: w.waived, Estimated: w.estimated, FinalUsage: w.usage, AttemptEntries: c.entries,
		PricingTiers: w.tiers,
	})
	if err != nil {
		return fmt.Errorf("billing: end the reservation: %w", err)
	}
	if win != nil {
		if err := moveSettledWindow(ctx, tx, *c.row.SubscriptionID, *c.row.WindowSeq, *win); err != nil {
			return err
		}
	}
	if terminal[w.state] {
		if err := c.b.suppliers.EndRequest(ctx, tx, c.id, c.row.SiteID, c.row.AccountID, at); err != nil {
			return fmt.Errorf("billing: end the request: %w", err)
		}
	}
	return nil
}

func settledVersion(versions []uuid.UUID, entry *int32) *uuid.UUID {
	if entry == nil {
		return nil
	}
	return lockedVersion(versions, int(*entry))
}

func lockedVersion(versions []uuid.UUID, entry int) *uuid.UUID {
	if entry < 0 || entry >= len(versions) || versions[entry] == uuid.Nil() {
		return nil
	}
	return &versions[entry]
}
