package billing

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

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
