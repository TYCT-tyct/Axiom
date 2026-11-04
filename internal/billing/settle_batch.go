package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// Settlement is one call of a completion batch: the hold its admission wrote and how the call ended.
type Settlement struct {
	Hold    *Hold
	Outcome Outcome
}

// SettleBatch settles every item in one transaction of scope, in order, under one lock of the account's balance row. It
// commits before returning and returns each item's Final and post-commit report in the same order. The batch posts the
// ledger events in three statements sent as one pipeline and ends the reservations in one statement, so the network
// round trips of a settlement no longer multiply by the batch size; the balance row still serializes every money write of the account.
//
// Each item is planned in order with the balance the earlier items leave, so a charge is capped exactly as if the items
// had been settled one by one. A plan's write error rolls the whole batch back and comes back as *BatchWorkError with
// the item's index, and the caller retries the items that are left.
//
// A set-based statement's SQL error cannot name an item: the transaction rolls back and the items are run through the
// single path in order to find the one the database refused. The ones that succeed commit; the first failure comes back
// as *BatchWorkError. An error with no SQL state (a connection, a cancelled context, the transaction's own) is returned
// unchanged, so every item retries.
func (b *Billing) SettleBatch(ctx context.Context, scope pg.Scope, items []Settlement) ([]Final, []func(), error) {
	if len(items) == 0 {
		return nil, nil, nil
	}
	if err := sameAccount(items); err != nil {
		return nil, nil, err
	}
	var plans []settlementPlan
	err := b.db.Tx(ctx, scope, func(tx pg.Tx) error {
		var err error
		plans, err = b.planSettlements(ctx, tx, items)
		if err != nil {
			return err
		}
		return writeSettlements(ctx, tx, b.suppliers, plans)
	})
	if err != nil {
		var work *BatchWorkError
		if errors.As(err, &work) || pg.SQLState(err) == "" {
			return nil, nil, err
		}
		return b.settleBatchOneByOne(ctx, items)
	}
	finals, afters := make([]Final, len(items)), make([]func(), len(items))
	for i, p := range plans {
		finals[i] = p.final
		c := p.c
		afters[i] = func() { b.alert(c.seen); b.bear(ctx, c.settlement, c.owed, c.charged) }
	}
	return finals, afters, nil
}

// sameAccount checks that every item has a hold of the first item's account.
func sameAccount(items []Settlement) error {
	if items[0].Hold == nil {
		return errors.New("billing: settle a batch without a hold")
	}
	for i, it := range items {
		if it.Hold == nil || it.Hold.admitted.Account != items[0].Hold.admitted.Account {
			return fmt.Errorf("billing: settlement %d is not of the account's batch", i)
		}
	}
	return nil
}

// planSettlements locks the account's balance row and the items' reservations and plans each item in order, with the
// balance the earlier items leave.
func (b *Billing) planSettlements(ctx context.Context, tx pg.Tx, items []Settlement) ([]settlementPlan, error) {
	account, site := items[0].Hold.admitted.Account, items[0].Hold.admitted.Site
	balance, err := lockAccount(ctx, tx, account)
	if err != nil {
		return nil, err
	}
	rows, err := lockSettlements(ctx, tx, items)
	if err != nil {
		return nil, err
	}
	plans := make([]settlementPlan, len(items))
	for i, it := range items {
		if rows[i].SiteID != site || rows[i].AccountID != account {
			return nil, &BatchWorkError{Index: i, Err: fmt.Errorf("billing: reservation %s is not of the account's batch", it.Hold.id)}
		}
		c := &locked{b: b, id: it.Hold.id, row: rows[i], balance: balance, quotes: it.Hold.quotes, charged: rows[i].Charged}
		plan, err := c.planSettlement(ctx, tx, it.Outcome)
		if err != nil {
			return nil, &BatchWorkError{Index: i, Err: err}
		}
		plan.c = c
		plans[i] = plan
		if plan.end != nil {
			// The next item's cap follows the wallet entries this one actually posts: a plan charge moves none, while a
			// plan hold cleanup released pays its late settlement from the wallet and must lower the balance here too.
			balance.Balance = addClamp(balance.Balance, -plan.walletDebit())
			balance.Reserved = addClamp(balance.Reserved, plan.end.balanceReserved)
		}
	}
	return plans, nil
}

// settleBatchOneByOne names the item a set-based SQL error could not name. The failed batch has rolled back; each item
// here is its own transaction, as the caller of RunBatch would retry them. A success commits and runs its report at
// once, so the batch has no report left; the first failure is returned with its index, and the caller retries the rest
// and the failed item alone.
func (b *Billing) settleBatchOneByOne(ctx context.Context, items []Settlement) ([]Final, []func(), error) {
	finals := make([]Final, len(items))
	var failed *BatchWorkError
	for i, it := range items {
		f, err := b.Settle(ctx, it.Hold, it.Outcome)
		if err != nil {
			if failed == nil {
				failed = &BatchWorkError{Index: i, Err: err}
			}
			continue
		}
		finals[i] = f
	}
	if failed != nil {
		return nil, nil, failed
	}
	return finals, make([]func(), len(items)), nil
}

// settlementPlan is one reservation's settlement as its result and the writes that make it true, all planned under the
// balance lock. A single settlement executes its plan at once; a batch writes the plans together.
type settlementPlan struct {
	c       *locked
	final   Final
	posts   []PostLedgerParams
	records []suppliers.AttemptRecord
	end     *reservationEnd
}

// walletDebit is the net amount this plan's ledger events take from the user's balance, exactly what the balance
// statement will apply for them.
func (p settlementPlan) walletDebit() int64 {
	var total platform.Exact
	for _, ev := range p.posts {
		total = sum(total, platform.AmountExact(ev.UserDebit))
	}
	return total.CeilAmount()
}

// reservationEnd is the EndReservation of one plan, its counters in the plan's order for the next item's cap, and the
// database time the batch reads back.
type reservationEnd struct {
	c               *locked
	index           int
	w               written
	balanceReserved int64
	spendReserved   int64
	spent           int64
	window          *windowSettle // the plan window's move, nil for a wallet hold or a late settlement
	at              time.Time
}

// planSettlement is settle's branch on the reservation's state, with the writes kept instead of executed. It is
// separate from settle so that SettleBatch can write a batch's plans together while a single settlement keeps its
// existing path and allocations.
func (c *locked) planSettlement(ctx context.Context, tx pg.Tx, o Outcome) (settlementPlan, error) {
	switch {
	case c.row.State == "settled" || c.row.State == "released":
		return c.planStored()
	case len(o.Attempts) == 0:
		return c.planUnattempted(ctx, tx)
	}
	return c.planCharge(ctx, tx, o)
}

// planStored answers a reservation that is already settled with what it recorded.
func (c *locked) planStored() (settlementPlan, error) {
	f, err := c.stored()
	return settlementPlan{final: f}, err
}

// planUnattempted settles a call that reached no upstream.
func (c *locked) planUnattempted(ctx context.Context, tx pg.Tx) (settlementPlan, error) {
	switch c.row.State {
	case "pending":
		c.costKnown = true // nothing ran, so the cost is a known 0 and so is the profit
		return settlementPlan{final: Final{Profit: c.profit()}, end: c.planRelease("released", c.row.Amount)}, nil
	case "cleanup_released":
		// The call never reached an upstream after all, so nothing can be charged to it: cleanup's record of it as
		// lost is answered, and no charge a supplier reports later is looked for in it.
		if err := c.b.suppliers.ResolveLostRequest(ctx, tx, c.id); err != nil {
			return settlementPlan{}, fmt.Errorf("billing: resolve the lost request: %w", err)
		}
	}
	return c.planStored()
}

// planCharge books the attempts and charges the last one.
func (c *locked) planCharge(ctx context.Context, tx pg.Tx, o Outcome) (settlementPlan, error) {
	var p settlementPlan
	p.posts, p.records = c.attemptWrites(o)
	last := charged(o.Attempts)
	if c.row.State == "cleanup_released" { // the usage arrived after all: the request is no longer lost
		if err := c.b.suppliers.ResolveLostRequest(ctx, tx, c.id); err != nil {
			return p, fmt.Errorf("billing: resolve the lost request: %w", err)
		}
	}
	pr, err := c.price(last)
	if err != nil {
		return p, err
	}
	if err := c.noteAttempts(ctx, tx, o); err != nil {
		return p, err
	}
	// A held reservation still reserves its amount; cleanup_settled and cleanup_released replace what cleanup
	// charged, at revision -1.
	plan := c.planLate
	if c.row.State == "pending" {
		plan = c.planEnd
	}
	end, posts, err := plan(pr, last)
	if err != nil {
		return p, err
	}
	p.end = end
	p.posts = append(p.posts, posts...)
	p.final = Final{
		PriceVersion: lockedVersion(c.row.PriceVersions, last.Entry), Segments: last.Segments,
		Charged: platform.AmountExact(c.charged), ServerToolCost: pr.serverToolPtr(), CacheDiscount: pr.cacheDiscount,
		Estimated: pr.estimated, Waived: pr.waived, Profit: c.profit(),
	}
	return p, nil
}

// attemptWrites books each attempt's cost and keeps the suppliers records, in this transaction: the row of an attempt
// and the ledger's cost of it are both there or neither. The entries the attempts bound are kept on the reservation, for
// the next settlement to restore.
func (c *locked) attemptWrites(o Outcome) ([]PostLedgerParams, []suppliers.AttemptRecord) {
	c.costKnown = true
	records := make([]suppliers.AttemptRecord, len(o.Attempts))
	posts := make([]PostLedgerParams, 0, len(o.Attempts))
	for i, at := range o.Attempts {
		c.costKnown = c.costKnown && at.CostKnown
		c.cost = sum(c.cost, platform.AmountExact(at.Cost))
		records[i] = suppliers.AttemptRecord{
			Request: platform.RequestID(c.id), Seq: platform.AttemptSeq(at.Seq), //nolint:gosec // at most platform.MaxAttemptSeq
			Site: c.row.SiteID, Account: c.row.AccountID,
			Receipt: at.Receipt, Usage: usages(at.Segments), Cost: at.Estimate, FinishedAt: at.FinishedAt,
		}
		e := c.event("attempt_cost", 0)
		e.seq = *int32p(at.Seq)
		posts = append(posts, ledgerParams(e, []line{{bookSupplierCost, at.Cost, siteCredits}, {bookSupplierPrepaid, -at.Cost, siteCredits}}))
	}
	c.entries = attemptEntries(o.Attempts)
	return posts, records
}

// planRelease is release with its write kept.
func (c *locked) planRelease(state string, held int64) *reservationEnd {
	if c.plan() {
		return &reservationEnd{c: c, w: c.kept(state), spendReserved: -max(held, 0), window: &windowSettle{held: held}}
	}
	return &reservationEnd{c: c, w: c.kept(state), balanceReserved: -max(held, 0), spendReserved: -max(held, 0)}
}

// planEnd is end with its writes kept: the charge is capped by the balance the earlier items of the batch leave.
func (c *locked) planEnd(p priced, last AttemptResult) (*reservationEnd, []PostLedgerParams, error) {
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
	w, err := result(p, last)
	if err != nil {
		return nil, nil, err
	}
	posts := []PostLedgerParams{ledgerParams(c.settlement, lines)}
	if c.plan() {
		return &reservationEnd{
			c: c, w: w, spendReserved: -c.row.Amount, spent: c.charged,
			window: &windowSettle{held: c.row.Amount, charged: c.charged},
		}, posts, nil
	}
	return &reservationEnd{c: c, w: w, balanceReserved: -held, spendReserved: -held, spent: c.charged}, posts, nil
}

// planLate is late with its writes kept: the refund and the new charge, and the difference as the spent counter.
func (c *locked) planLate(p priced, last AttemptResult) (*reservationEnd, []PostLedgerParams, error) {
	c.owed, c.charged = p.price, min(p.price, add(c.row.Charged, c.cap(0)))
	reversalEvent := c.event("reverse", lateRevision)
	c.settlement = c.event("settle", lateRevision)
	w, err := result(p, last)
	if err != nil {
		return nil, nil, err
	}
	posts := []PostLedgerParams{
		ledgerParams(reversalEvent, reversal(c.row.Price, c.row.Charged)),
		ledgerParams(c.settlement, charge(p.price, c.charged)),
	}
	// A plan hold cleanup released has no window left to charge: the usage is the wallet's to pay, as late does. The
	// row must say so before a correction reads it as a plan charge.
	w.wallet = true
	return &reservationEnd{c: c, w: w, spent: c.charged - c.row.Charged}, posts, nil
}
