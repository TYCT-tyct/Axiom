package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// had been settled one by one. A plan's writes are all in this transaction: a work error rolls the whole batch back and
// comes back as *BatchWorkError with the item's index, and the caller retries the items that are left. An error without
// an index is the transaction's own; every item is retried.
func (b *Billing) SettleBatch(ctx context.Context, scope pg.Scope, items []Settlement) (finals []Final, afters []func(), err error) {
	if len(items) == 0 {
		return nil, nil, nil
	}
	if items[0].Hold == nil {
		return nil, nil, errors.New("billing: settle a batch without a hold")
	}
	account, site := items[0].Hold.admitted.Account, items[0].Hold.admitted.Site
	for i, it := range items {
		if it.Hold == nil || it.Hold.admitted.Account != account {
			return nil, nil, fmt.Errorf("billing: settlement %d is not of the account's batch", i)
		}
	}

	plans := make([]settlementPlan, len(items))
	err = b.db.Tx(ctx, scope, func(tx pg.Tx) error {
		balance, err := lockAccount(ctx, tx, account)
		if err != nil {
			return err
		}
		rows, err := lockSettlements(ctx, tx, items)
		if err != nil {
			return err
		}

		for i, it := range items {
			if rows[i].SiteID != site || rows[i].AccountID != account {
				return &BatchWorkError{Index: i, Err: fmt.Errorf("billing: reservation %s is not of the account's batch", it.Hold.id)}
			}
			c := &locked{b: b, id: it.Hold.id, row: rows[i], balance: balance, quotes: it.Hold.quotes, charged: rows[i].Charged}
			plan, err := c.planSettlement(ctx, tx, it.Outcome)
			if err != nil {
				return &BatchWorkError{Index: i, Err: err}
			}
			plan.c = c
			plans[i] = plan
			if plan.end != nil {
				balance.Balance = addClamp(balance.Balance, -plan.end.spent)
				balance.Reserved = addClamp(balance.Reserved, plan.end.reserved)
			}
		}
		return b.writeSettlements(ctx, tx, plans)
	})
	if err != nil {
		return nil, nil, err
	}
	finals, afters = make([]Final, len(items)), make([]func(), len(items))
	for i, p := range plans {
		finals[i] = p.final
		c := p.c
		afters[i] = func() { b.alert(c.seen); b.bear(ctx, c.settlement, c.owed, c.charged) }
	}
	return finals, afters, nil
}

// settlementPlan is one reservation's settlement as its result and the writes that make it true, all planned under the
// balance lock. A single settlement executes its plan at once; a batch writes the plans together.
type settlementPlan struct {
	c       *locked
	final   Final
	posts   []ledgerPost
	records []suppliers.AttemptRecord
	end     *reservationEnd
}

// ledgerPost is one ledger event with the parameters that post it, kept so a batch can post every event in order.
type ledgerPost struct {
	event  event
	params PostLedgerParams
}

// reservationEnd is the EndReservation of one plan, its counters in the plan's order for the next item's cap, and the
// database time the batch reads back.
type reservationEnd struct {
	c        *locked
	index    int
	w        written
	reserved int64
	spent    int64
	at       time.Time
}

// planSettlement is settle's branch on the reservation's state, with the writes kept instead of executed. It is
// separate from settle so that SettleBatch can write a batch's plans together while a single settlement keeps its
// existing path and allocations.
func (c *locked) planSettlement(ctx context.Context, tx pg.Tx, o Outcome) (settlementPlan, error) {
	var p settlementPlan
	switch c.row.State {
	case "settled", "released":
		f, err := c.stored()
		p.final = f
		return p, err
	}
	if len(o.Attempts) == 0 {
		switch c.row.State {
		case "pending":
			c.costKnown = true // nothing ran, so the cost is a known 0 and so is the profit
			p.final = Final{Profit: c.profit()}
			p.end = c.planRelease("released", c.row.Amount)
		case "cleanup_released":
			// The call never reached an upstream after all, so nothing can be charged to it: cleanup's record of it as
			// lost is answered, and no charge a supplier reports later is looked for in it.
			if err := c.b.suppliers.ResolveLostRequest(ctx, tx, c.id); err != nil {
				return p, fmt.Errorf("billing: resolve the lost request: %w", err)
			}
			f, err := c.stored()
			p.final = f
			return p, err
		default:
			f, err := c.stored()
			p.final = f
			return p, err
		}
		return p, nil
	}
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
	if c.row.State == "pending" { // held: amount is still reserved
		end, err := c.planEnd(pr, last)
		if err != nil {
			return p, err
		}
		p.end = end
		p.posts = append(p.posts, ledgerPost{event: c.settlement, params: ledgerParams(c.settlement, charge(pr.price, c.charged))})
	} else { // cleanup_settled or cleanup_released: replace what cleanup charged, at revision -1
		end, posts, err := c.planLate(pr, last)
		if err != nil {
			return p, err
		}
		p.end = end
		p.posts = append(p.posts, posts...)
	}
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
func (c *locked) attemptWrites(o Outcome) ([]ledgerPost, []suppliers.AttemptRecord) {
	c.costKnown = true
	records := make([]suppliers.AttemptRecord, len(o.Attempts))
	posts := make([]ledgerPost, 0, len(o.Attempts))
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
		posts = append(posts, ledgerPost{event: e, params: ledgerParams(e, []line{{bookSupplierCost, at.Cost, siteCredits}, {bookSupplierPrepaid, -at.Cost, siteCredits}})})
	}
	c.entries = attemptEntries(o.Attempts)
	return posts, records
}

// planRelease is release with its write kept.
func (c *locked) planRelease(state string, held int64) *reservationEnd {
	return &reservationEnd{c: c, w: c.kept(state), reserved: -max(held, 0)}
}

// planEnd is end with its write kept: the charge is capped by the balance the earlier items of the batch leave.
func (c *locked) planEnd(p priced, last AttemptResult) (*reservationEnd, error) {
	held := c.row.Amount - c.row.Charged
	c.owed, c.charged = p.price, min(p.price, c.cap(held))
	c.settlement = c.event("settle", 0)
	w, err := result("settled", p, last)
	if err != nil {
		return nil, err
	}
	return &reservationEnd{c: c, w: w, reserved: -held, spent: c.charged}, nil
}

// planLate is late with its writes kept: the refund and the new charge, and the difference as the spent counter.
func (c *locked) planLate(p priced, last AttemptResult) (*reservationEnd, []ledgerPost, error) {
	c.owed, c.charged = p.price, min(p.price, add(c.row.Charged, c.cap(0)))
	reversalEvent := c.event("reverse", lateRevision)
	c.settlement = c.event("settle", lateRevision)
	w, err := result("settled", p, last)
	if err != nil {
		return nil, nil, err
	}
	posts := []ledgerPost{
		{event: reversalEvent, params: ledgerParams(reversalEvent, reversal(c.row.Price, c.row.Charged))},
		{event: c.settlement, params: ledgerParams(c.settlement, charge(p.price, c.charged))},
	}
	return &reservationEnd{c: c, w: w, spent: c.charged - c.row.Charged}, posts, nil
}

// writeSettlements writes every plan of a batch: the ledger events in one statement, the suppliers attempts in one,
// the reservation ends in one pipeline, and the ended requests after the times that pipeline returned. A failure that
// names an item comes back as *BatchWorkError; the others are the transaction's, for every item.
func (b *Billing) writeSettlements(ctx context.Context, tx pg.Tx, plans []settlementPlan) error {
	if err := b.postSettlementLedger(ctx, tx, plans); err != nil {
		return err
	}
	var records []suppliers.AttemptRecord
	for _, p := range plans {
		records = append(records, p.records...)
	}
	if len(records) > 0 {
		if err := b.suppliers.RecordAttempts(ctx, tx, records); err != nil {
			return fmt.Errorf("billing: record the attempts: %w", err)
		}
	}
	ends, err := endSettlementReservations(ctx, tx, plans)
	if err != nil {
		return err
	}
	var ending []suppliers.EndedRequest
	for _, e := range ends {
		if terminal[e.w.state] {
			ending = append(ending, suppliers.EndedRequest{Request: e.c.id, Site: e.c.row.SiteID, Account: e.c.row.AccountID, At: e.at})
		}
	}
	if len(ending) > 0 {
		if err := b.suppliers.EndRequests(ctx, tx, ending); err != nil {
			return fmt.Errorf("billing: end the requests: %w", err)
		}
	}
	return nil
}

// postSettlementLedger posts every event of the plans in three statements: the transactions, their entries, and one
// balance move. The statements share the transaction and the connection, so the entries see their transactions and the
// balance sees every entry. A duplicate event violates the transactions' unique key and rolls the batch back, which is
// what the reservation state says cannot happen; the gateway then retries the items one at a time and stores the one
// that had already committed.
func (b *Billing) postSettlementLedger(ctx context.Context, tx pg.Tx, plans []settlementPlan) error {
	var events []ledgerPost
	for _, p := range plans {
		events = append(events, p.posts...)
	}
	if len(events) == 0 {
		return nil
	}
	first := plans[0].c.row
	transactions := PostLedgerTransactionsParams{SiteID: first.SiteID, AccountID: first.AccountID}
	entries := PostLedgerEntriesParams{SiteID: first.SiteID, AccountID: first.AccountID}
	var total int64
	for _, ev := range events {
		transactions.Ids = append(transactions.Ids, ev.params.ID)
		transactions.Kinds = append(transactions.Kinds, ev.params.Kind)
		transactions.RequestIds = append(transactions.RequestIds, ev.params.RequestID)
		transactions.AttemptSeqs = append(transactions.AttemptSeqs, ev.params.AttemptSeq)
		transactions.Revisions = append(transactions.Revisions, ev.params.Revision)
		total = addClamp(total, ev.params.UserDebit)
		for j := range ev.params.Books {
			entries.TransactionIds = append(entries.TransactionIds, ev.params.ID)
			entries.Books = append(entries.Books, ev.params.Books[j])
			entries.Amounts = append(entries.Amounts, ev.params.Amounts[j])
			entries.Currencies = append(entries.Currencies, ev.params.Currencies[j])
		}
	}
	statements := []pg.Statement{
		{SQL: postLedgerTransactions, Args: []any{transactions.Ids, transactions.SiteID, transactions.AccountID, transactions.Kinds, transactions.RequestIds, transactions.AttemptSeqs, transactions.Revisions}},
		{SQL: postLedgerEntries, Args: []any{entries.TransactionIds, entries.SiteID, entries.AccountID, entries.Books, entries.Amounts, entries.Currencies}},
	}
	move := total != 0
	if move {
		statements = append(statements, pg.Statement{SQL: moveBalance, Args: []any{total, first.AccountID}})
	}
	return tx.Batch(ctx, statements, func(batch pg.BatchResults) error {
		tag, err := batch.Exec()
		if err != nil {
			return fmt.Errorf("billing: post the settlement ledger: %w", err)
		}
		if got := int(tag.RowsAffected()); got != len(events) {
			return fmt.Errorf("billing: posted %d of %d settlement ledger events", got, len(events))
		}
		tag, err = batch.Exec()
		if err != nil {
			return fmt.Errorf("billing: post the settlement entries: %w", err)
		}
		if got := int(tag.RowsAffected()); got != len(entries.Books) {
			return fmt.Errorf("billing: posted %d of %d settlement ledger entries", got, len(entries.Books))
		}
		if !move {
			return nil
		}
		tag, err = batch.Exec()
		if err != nil {
			return fmt.Errorf("billing: move the settlement balance: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("billing: moved the balance of %d accounts", tag.RowsAffected())
		}
		return nil
	})
}

// endSettlementReservations ends every plan's reservation in one statement and reads the database time of each. The
// statement moves the balance's and each window's counters by their sums, so the batch's counters end exactly where the
// items' would one by one.
func endSettlementReservations(ctx context.Context, tx pg.Tx, plans []settlementPlan) ([]*reservationEnd, error) {
	var ends []*reservationEnd
	for i, p := range plans {
		if p.end == nil {
			continue
		}
		p.end.index = i
		ends = append(ends, p.end)
	}
	if len(ends) == 0 {
		return nil, nil
	}
	params := EndReservationBatchParams{AccountID: ends[0].c.row.AccountID}
	for _, e := range ends {
		p := e.params()
		chargedSeq, entry := int32(-1), int32(-1)
		if p.ChargedSeq != nil {
			chargedSeq = *p.ChargedSeq
		}
		if p.Entry != nil {
			entry = *p.Entry
		}
		finalUsage, finalSet := "", p.FinalUsage != nil
		if finalSet {
			finalUsage = string(p.FinalUsage)
		}
		attemptEntries, attemptSet := "", p.AttemptEntries != nil
		if attemptSet {
			b, err := json.Marshal(p.AttemptEntries)
			if err != nil {
				return nil, &BatchWorkError{Index: e.index, Err: fmt.Errorf("billing: encode the attempts' entries: %w", err)}
			}
			attemptEntries = string(b)
		}
		profit, profitSet := int64(0), p.Profit != nil
		if profitSet {
			profit = *p.Profit
		}
		params.RequestIds = append(params.RequestIds, p.RequestID)
		params.States = append(params.States, p.State)
		params.ChargedSeqs = append(params.ChargedSeqs, chargedSeq)
		params.Entries = append(params.Entries, entry)
		params.Charged = append(params.Charged, p.Charged)
		params.Prices = append(params.Prices, p.Price)
		params.Waived = append(params.Waived, p.Waived)
		params.Estimated = append(params.Estimated, p.Estimated)
		params.FinalUsages = append(params.FinalUsages, finalUsage)
		params.FinalUsageSet = append(params.FinalUsageSet, finalSet)
		params.AttemptEntries = append(params.AttemptEntries, attemptEntries)
		params.AttemptEntriesSet = append(params.AttemptEntriesSet, attemptSet)
		params.Profits = append(params.Profits, profit)
		params.ProfitSet = append(params.ProfitSet, profitSet)
		params.PricingTiers = append(params.PricingTiers, p.PricingTiers)
		params.Reserved = append(params.Reserved, p.Reserved)
		params.Spent = append(params.Spent, p.Spent)
		params.WorkspaceIds = append(params.WorkspaceIds, p.WorkspaceID)
		params.WorkspaceWindows = append(params.WorkspaceWindows, p.WorkspaceWindow)
		params.KeyIds = append(params.KeyIds, p.KeyID)
		params.KeyWindows = append(params.KeyWindows, p.KeyWindow)
	}
	rows, err := queries.EndReservationBatch(ctx, tx, params)
	if err != nil {
		return nil, fmt.Errorf("billing: end the reservations: %w", err)
	}
	at := make(map[uuid.UUID]time.Time, len(rows))
	for _, row := range rows {
		at[row.RequestID] = row.EndedAt
	}
	for _, e := range ends {
		t, ok := at[e.c.id]
		if !ok {
			return nil, &BatchWorkError{Index: e.index, Err: fmt.Errorf("billing: the reservation %s did not end", e.c.id)}
		}
		e.at = t
	}
	return ends, nil
}

// params is the EndReservation of e, with the reservation's current charged amount and the plan's counters.
func (e *reservationEnd) params() EndReservationParams {
	c := e.c
	return EndReservationParams{
		Profit: c.profit(), Reserved: e.reserved, Spent: e.spent, AccountID: c.row.AccountID,
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
		Profit: r.Profit, PricingTiers: r.PricingTiers,
	}
}
