package billing

import (
	"context"
	"fmt"
	"math"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// attemptRecords turns one gateway attempt into the supplier rows billing must keep. A loop attempt has one row per
// supplier request; other attempts keep the single row they always had. The ledger still posts one aggregate cost for
// the gateway attempt, but each supplier charge now has its own row to reconcile against.
func attemptRecords(c *locked, at AttemptResult) []suppliers.AttemptRecord {
	if len(at.Hops) == 0 {
		return []suppliers.AttemptRecord{{
			Request: platform.RequestID(c.id), Seq: platform.AttemptSeq(at.Seq), //nolint:gosec // at most platform.MaxAttempts
			Site: c.row.SiteID, Account: c.row.AccountID,
			Receipt: at.Receipt, Usage: usages(at.Segments), Cost: at.Estimate, FinishedAt: at.FinishedAt,
		}}
	}
	rows := make([]suppliers.AttemptRecord, 0, len(at.Hops))
	for i, hop := range at.Hops {
		rows = append(rows, suppliers.AttemptRecord{
			Request: platform.RequestID(c.id), Seq: platform.AttemptSeq(at.Seq), Hop: int16(i), //nolint:gosec // at most platform.MaxAttempts and 30 hops
			Site: c.row.SiteID, Account: c.row.AccountID,
			Receipt: hop.Receipt, Usage: usages(hop.Segments), Cost: hop.Estimate, FinishedAt: hop.FinishedAt,
		})
	}
	return rows
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
	c.owed, c.charged = p.price, min(p.price, c.cap(held))
	c.settlement = c.event("settle", 0)
	if _, err := post(ctx, tx, c.settlement, charge(p.price, c.charged)...); err != nil {
		return err
	}
	w, err := result(p, last)
	if err != nil {
		return err
	}
	return c.write(ctx, tx, w, -held, c.charged)
}

// late replaces what cleanup charged with what the full outcome charges: a refund, or a makeup capped at the floor.
func (c *locked) late(ctx context.Context, tx pg.Tx, p priced, last AttemptResult) error {
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
	return c.write(ctx, tx, w, 0, c.charged-c.row.Charged)
}

// release ends a reservation in state, giving back what it held and keeping what was charged.
func (c *locked) release(ctx context.Context, tx pg.Tx, state string, held int64) error {
	return c.write(ctx, tx, c.kept(state), -max(held, 0), 0)
}

// written is what write records on the reservation.
type written struct {
	state             string
	seq, entry        *int32 // nil keeps the recorded ones
	price             int64
	waived, estimated bool
	usage             []byte
	tiers             int16
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
func (c *locked) write(ctx context.Context, tx pg.Tx, w written, reserved, spent int64) error {
	at, err := queries.EndReservation(ctx, tx, EndReservationParams{
		Profit: c.profit(), Reserved: reserved, Spent: spent, AccountID: c.row.AccountID,
		WorkspaceID: c.row.WorkspaceID, WorkspaceWindow: c.row.WorkspaceWindow, KeyID: c.row.KeyID, KeyWindow: c.row.KeyWindow,
		RequestID: c.id, State: w.state, ChargedSeq: w.seq, Entry: w.entry, Charged: c.charged, Price: w.price,
		Waived: w.waived, Estimated: w.estimated, FinalUsage: w.usage, AttemptEntries: c.entries,
		PricingTiers: w.tiers,
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
