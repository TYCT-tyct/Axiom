package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
	"uuid"

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
	last := o.Attempts[len(o.Attempts)-1]
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
			Request: platform.RequestID(c.id), Seq: platform.AttemptSeq(at.Seq), //nolint:gosec // at most platform.MaxAttempts
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

// writeSettlements writes every plan of a batch: the ledger events in one pipeline, the suppliers attempts in one
// statement, the reservation ends in one, and the ended requests after the times that statement returned. A failure
// that names an item comes back as *BatchWorkError; the others are the transaction's, and SettleBatch falls back to the
// single path when they carry a SQL state.
func writeSettlements(ctx context.Context, tx pg.Tx, s Suppliers, plans []settlementPlan) error {
	if err := postSettlementLedger(ctx, tx, plans); err != nil {
		return err
	}
	var records []suppliers.AttemptRecord
	for _, p := range plans {
		records = append(records, p.records...)
	}
	if len(records) > 0 {
		if err := s.RecordAttempts(ctx, tx, records); err != nil {
			return fmt.Errorf("billing: record the attempts: %w", err)
		}
	}
	ends := settlementEnds(plans)
	// The source is rewritten while the reservation is still pending: the update policy only shows pending rows, and
	// once the row is settled its source is frozen with the rest of the terminal record.
	if err := markLateWallets(ctx, tx, ends); err != nil {
		return err
	}
	if err := endSettlementReservations(ctx, tx, ends); err != nil {
		return err
	}
	if err := settlePlanWindows(ctx, tx, ends); err != nil {
		return err
	}
	var ending []suppliers.EndedRequest
	for _, e := range ends {
		if terminal[e.w.state] {
			ending = append(ending, suppliers.EndedRequest{Request: e.c.id, Site: e.c.row.SiteID, Account: e.c.row.AccountID, At: e.at})
		}
	}
	if len(ending) > 0 {
		if err := s.EndRequests(ctx, tx, ending); err != nil {
			return fmt.Errorf("billing: end the requests: %w", err)
		}
	}
	return nil
}

// postSettlementLedger posts every event of the plans in three statements: the transactions, their entries, and the
// exact balance they leave. The statements share the transaction and the connection, so the entries see their
// transactions and the balance sees every entry. A duplicate event violates the transactions' unique key and rolls the
// batch back, which is what the reservation state says cannot happen; SettleBatch then retries the items one at a time
// and stores the one that had already committed.
func postSettlementLedger(ctx context.Context, tx pg.Tx, plans []settlementPlan) error {
	first := plans[0].c
	l, err := settlementLedger(plans)
	if err != nil || len(l.transactions.Ids) == 0 {
		return err
	}
	t, e := l.transactions, l.entries
	statements := []pg.Statement{
		{SQL: postLedgerTransactions, Args: []any{t.Ids, t.SiteID, t.AccountID, t.Kinds, t.RequestIds, t.AttemptSeqs, t.Revisions}},
		{SQL: postLedgerEntries, Args: []any{e.TransactionIds, e.SiteID, e.AccountID, e.Books, e.Amounts, e.Currencies}},
	}
	move := l.balance != first.balance.Balance
	if move {
		statements = append(statements, pg.Statement{SQL: setBalance, Args: []any{l.balance, first.row.AccountID}})
	}
	return tx.Batch(ctx, statements, func(batch pg.BatchResults) error {
		if err := execRows(batch, "post the settlement ledger", len(t.Ids)); err != nil {
			return err
		}
		if err := execRows(batch, "post the settlement entries", len(e.Books)); err != nil {
			return err
		}
		if !move {
			return nil
		}
		return execRows(batch, "set the settlement balance", 1)
	})
}

// ledgerBatch is the parameters of a batch's ledger statements and the balance they leave.
type ledgerBatch struct {
	transactions PostLedgerTransactionsParams
	entries      PostLedgerEntriesParams
	balance      int64
}

// settlementLedger gathers the plans' events. The balance is summed in platform.Exact and written as its final value:
// summing the user debits in int64 would clamp two individually legal charges once their total passes int64, leaving
// the balance and the entries inconsistent forever. A final value outside int64 is an error, not a clamp.
func settlementLedger(plans []settlementPlan) (ledgerBatch, error) {
	first := plans[0].c
	l := ledgerBatch{
		transactions: PostLedgerTransactionsParams{SiteID: first.row.SiteID, AccountID: first.row.AccountID},
		entries:      PostLedgerEntriesParams{SiteID: first.row.SiteID, AccountID: first.row.AccountID},
	}
	final := platform.AmountExact(first.balance.Balance)
	for _, p := range plans {
		for _, ev := range p.posts {
			t := &l.transactions
			t.Ids, t.Kinds = append(t.Ids, ev.ID), append(t.Kinds, ev.Kind)
			t.RequestIds, t.AttemptSeqs = append(t.RequestIds, ev.RequestID), append(t.AttemptSeqs, ev.AttemptSeq)
			t.Revisions = append(t.Revisions, ev.Revision)
			e := &l.entries
			for j := range ev.Books {
				e.TransactionIds, e.Books = append(e.TransactionIds, ev.ID), append(e.Books, ev.Books[j])
				e.Amounts, e.Currencies = append(e.Amounts, ev.Amounts[j]), append(e.Currencies, ev.Currencies[j])
			}
			var err error
			if final, err = final.Add(platform.AmountExact(ev.UserDebit).Neg()); err != nil {
				return l, fmt.Errorf("billing: sum the settlement balance: %w", err)
			}
		}
	}
	if final.Cmp(platform.AmountExact(math.MaxInt64)) > 0 || final.Cmp(platform.AmountExact(math.MinInt64)) < 0 {
		return l, fmt.Errorf("billing: the settlement balance %s is outside int64", final.AppendDecimal(nil))
	}
	l.balance = final.CeilAmount()
	return l, nil
}

// execRows reads the next statement of a batch and checks that it wrote want rows.
func execRows(batch pg.BatchResults, what string, want int) error {
	tag, err := batch.Exec()
	if err != nil {
		return fmt.Errorf("billing: %s: %w", what, err)
	}
	if got := int(tag.RowsAffected()); got != want {
		return fmt.Errorf("billing: %s wrote %d of %d rows", what, got, want)
	}
	return nil
}

// settlementEnds is the non-nil reservation ends of the batch, in item order.
func settlementEnds(plans []settlementPlan) []*reservationEnd {
	var ends []*reservationEnd
	for i, p := range plans {
		if p.end != nil {
			p.end.index = i
			ends = append(ends, p.end)
		}
	}
	return ends
}

// endSettlementReservations ends every plan's reservation in one statement and reads the database time of each. The
// statement moves the balance's and each window's counters by their sums, so the batch's counters end exactly where the
// items' would one by one.
func endSettlementReservations(ctx context.Context, tx pg.Tx, ends []*reservationEnd) error {
	if len(ends) == 0 {
		return nil
	}
	params := EndReservationBatchParams{AccountID: ends[0].c.row.AccountID}
	for _, e := range ends {
		params.add(e.params())
	}
	rows, err := queries.EndReservationBatch(ctx, tx, params)
	if err != nil {
		return fmt.Errorf("billing: end the reservations: %w", err)
	}
	at := make(map[uuid.UUID]time.Time, len(rows))
	for _, row := range rows {
		at[row.RequestID] = row.EndedAt
	}
	for _, e := range ends {
		t, ok := at[e.c.id]
		if !ok {
			return &BatchWorkError{Index: e.index, Err: fmt.Errorf("billing: the reservation %s did not end", e.c.id)}
		}
		e.at = t
	}
	return nil
}

// add appends one EndReservation to the batch's arrays. A NULL of the single statement, which keeps what the row has,
// is a -1 or a false set flag here: SQL arrays of the batch carry no NULLs.
func (params *EndReservationBatchParams) add(p EndReservationParams) {
	params.RequestIds = append(params.RequestIds, p.RequestID)
	params.States = append(params.States, p.State)
	params.ChargedSeqs = append(params.ChargedSeqs, orElse(p.ChargedSeq, -1))
	params.Entries = append(params.Entries, orElse(p.Entry, -1))
	params.Charged = append(params.Charged, p.Charged)
	params.Prices = append(params.Prices, p.Price)
	params.Waived = append(params.Waived, p.Waived)
	params.Estimated = append(params.Estimated, p.Estimated)
	params.FinalUsages = append(params.FinalUsages, string(p.FinalUsage))
	params.FinalUsageSet = append(params.FinalUsageSet, p.FinalUsage != nil)
	params.AttemptEntries = append(params.AttemptEntries, intsJSON(p.AttemptEntries))
	params.AttemptEntriesSet = append(params.AttemptEntriesSet, p.AttemptEntries != nil)
	params.Profits = append(params.Profits, orElse(p.Profit, 0))
	params.ProfitSet = append(params.ProfitSet, p.Profit != nil)
	params.PricingTiers = append(params.PricingTiers, p.PricingTiers)
	params.BalanceReserved = append(params.BalanceReserved, p.BalanceReserved)
	params.Reserved = append(params.Reserved, p.SpendReserved)
	params.Spent = append(params.Spent, p.Spent)
	params.WorkspaceIds = append(params.WorkspaceIds, p.WorkspaceID)
	params.WorkspaceWindows = append(params.WorkspaceWindows, p.WorkspaceWindow)
	params.KeyIds = append(params.KeyIds, p.KeyID)
	params.KeyWindows = append(params.KeyWindows, p.KeyWindow)
}

func orElse[T any](p *T, none T) T {
	if p == nil {
		return none
	}
	return *p
}

// intsJSON is xs as a JSON array of integers, the form the batch statement unpacks into attempt_entries.
func intsJSON(xs []int32) string {
	b := []byte{'['}
	for i, x := range xs {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, int64(x), 10)
	}
	return string(append(b, ']'))
}

// params is the EndReservation of e, with the reservation's current charged amount and the plan's counters.
func (e *reservationEnd) params() EndReservationParams {
	c := e.c
	return EndReservationParams{
		Profit: c.profit(), BalanceReserved: e.balanceReserved, SpendReserved: e.spendReserved, Spent: e.spent, AccountID: c.row.AccountID,
		WorkspaceID: c.row.WorkspaceID, WorkspaceWindow: c.row.WorkspaceWindow, KeyID: c.row.KeyID, KeyWindow: c.row.KeyWindow,
		RequestID: c.id, State: e.w.state, ChargedSeq: e.w.seq, Entry: e.w.entry, Charged: c.charged, Price: e.w.price,
		Waived: e.w.waived, Estimated: e.w.estimated, FinalUsage: e.w.usage, AttemptEntries: c.entries,
		PricingTiers: e.w.tiers,
	}
}

// lockSettlements reads every item's reservation in one statement, after the account's balance row is locked. It
// keeps the items' order and names a missing reservation's item.
func lockSettlements(ctx context.Context, tx pg.Tx, items []Settlement) ([]LockReservationRow, error) {
	ids := make([]uuid.UUID, len(items))
	for i, it := range items {
		ids[i] = it.Hold.id
	}
	found, err := queries.LockReservations(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("billing: read the reservations: %w", err)
	}
	byID := make(map[uuid.UUID]LockReservationRow, len(found))
	for _, r := range found {
		byID[r.RequestID] = r.lockRow()
	}
	rows := make([]LockReservationRow, len(items))
	for i, it := range items {
		row, ok := byID[it.Hold.id]
		if !ok {
			return nil, &BatchWorkError{Index: i, Err: fmt.Errorf("billing: the reservation %s is not there", it.Hold.id)}
		}
		rows[i] = row
	}
	return rows, nil
}

// lockRow is the batch row as the single reservation read's row; the two queries select the same columns.
func (r LockReservationsRow) lockRow() LockReservationRow {
	return LockReservationRow{
		SiteID: r.SiteID, AccountID: r.AccountID, WorkspaceID: r.WorkspaceID, KeyID: r.KeyID,
		WorkspaceWindow: r.WorkspaceWindow, KeyWindow: r.KeyWindow, Amount: r.Amount, State: r.State,
		Prices: r.Prices, PriceVersions: r.PriceVersions, ChargedSeq: r.ChargedSeq, Entry: r.Entry,
		Charged: r.Charged, Price: r.Price, Waived: r.Waived, Estimated: r.Estimated, FinalUsage: r.FinalUsage,
		Fallbacks: r.Fallbacks, Models: r.Models, AttemptEntries: r.AttemptEntries, Endpoints: r.Endpoints,
		Profit: r.Profit, SubscriptionID: derefUUID(r.SubscriptionID), WindowSeq: derefInt32(r.WindowSeq),
		PricingTiers: r.PricingTiers,
	}
}
