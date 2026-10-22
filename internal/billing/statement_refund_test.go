package billing

import (
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestStatementRefundEvents(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(1000)
	b.charge(100)
	f.Amount = 300
	b.freeze(f)
	f.Amount = 100
	b.finishRefund(f, false, true)
	f.Amount = 200
	b.finishRefund(f, false, false)
	// Revision is cumulative per kind, not the amount of the next freeze/unfreeze.
	f.Amount = 50
	b.freeze(f)
	b.finishRefund(f, false, true)
	f.Amount = 70
	b.freeze(f) // leave one unresolved hold in the latest balance
	rows, _ := b.statement(b.admitted, 50, nil)
	want := []struct {
		kind                                  StatementKind
		amount, frozenChange, balance, frozen int64
	}{
		{StatementFreeze, 0, 70, 700, 70},
		{StatementUnfreeze, 0, -50, 700, 0},
		{StatementFreeze, 0, 50, 700, 50},
		{StatementRefund, -200, -200, 700, 0},
		{StatementUnfreeze, 0, -100, 900, 200},
		{StatementFreeze, 0, 300, 900, 300},
		{StatementSpend, -100, 0, 900, 0},
		{StatementTopUp, 1000, 0, 1000, 0},
	}
	if len(rows) != len(want) {
		t.Fatalf("events = %+v, want %d", rows, len(want))
	}
	for i, w := range want {
		r := rows[i]
		if r.Kind != w.kind || r.Amount != w.amount || r.FrozenChange != w.frozenChange || r.Balance != w.balance || r.Frozen != w.frozen {
			t.Errorf("row %d = %+v, want %+v", i, r, w)
		}
		if r.Kind != StatementSpend && r.Kind != StatementReversal && r.Reference != f.Batch {
			t.Errorf("row %d lost its paid batch/order reference", i)
		}
	}
	b.checkStatementRefundPaging(f, len(rows))
}

func (b *bank) checkStatementRefundPaging(f Freeze, count int) {
	b.t.Helper()
	// Every possible small page size must preserve both columns, even when a new freeze commits above it.
	for size := 1; size <= count; size++ {
		first, next := b.statement(b.admitted, size, nil)
		if size < count {
			b.freeze(Freeze{Account: f.Account, Batch: f.Batch, Amount: 1})
		}
		all := first
		for next != nil {
			var page []StatementRow
			page, next = b.statement(b.admitted, size, next)
			all = append(all, page...)
		}
		checkStatementChain(b.t, all)
	}
}

// Allocation deliberately stops on reversals (#277); this read-side fixture posts the reversal only after all
// allocation-dependent writes, and then reads without trying PrepareRefund on an unsupported ledger.
func TestStatementReversal(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, Config{})
	b.charge(100)
	b.reverseStatementCharge(40)
	rows, next := b.statement(b.admitted, 50, nil)
	if len(rows) != 3 || next != nil || rows[0].Kind != StatementReversal || rows[0].Amount != 40 || rows[0].Balance != 940 {
		t.Fatalf("reversal statement = %+v, %v", rows, next)
	}
	if rows[0].Reference != rows[1].Reference {
		t.Fatal("reversal does not refer to its original charge")
	}
	checkStatementChain(t, rows)
}

func (b *bank) reverseStatementCharge(amount int64) {
	b.t.Helper()
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		if _, err := lockAccount(b.t.Context(), tx, b.admitted.Account); err != nil {
			return err
		}
		var request uuid.UUID
		if err := tx.QueryRow(b.t.Context(), "SELECT request_id FROM billing.transactions WHERE account_id = $1 AND kind = 'settle' ORDER BY seq DESC LIMIT 1", b.admitted.Account).Scan(&request); err != nil {
			return err
		}
		_, err := post(b.t.Context(), tx, event{site: b.admitted.Site, account: b.admitted.Account, kind: "reverse", request: request, revision: 1}, reversal(amount, amount)...)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
}

func TestStatementRefundAfterExpiry(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(1000)
	f.Amount = 300
	b.freeze(f)
	b.sql("UPDATE billing.credit_batches SET expires_at = clock_timestamp() - interval '1 second'")
	b.expire()
	b.finishRefund(f, true, false)
	rows, _ := b.statement(b.admitted, 50, nil)
	if len(rows) != 4 || rows[0].Kind != StatementRefund || rows[0].Amount != -300 || rows[0].Balance != 0 || rows[0].Frozen != 0 || rows[1].Amount != -700 {
		t.Fatalf("refund and expiry = %+v", rows)
	}
	// Another tenant cannot read these events even when it names the victim account.
	other := b.accountWithCredit()
	if err := b.app.ReadTx(t.Context(), other.Scope, func(tx pg.Tx) error {
		got, next, err := ListStatement(t.Context(), tx, StatementQuery{Account: f.Account, PageSize: 50})
		if err == nil && (len(got) != 0 || next != nil) {
			t.Errorf("another account read %+v, %v", got, next)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
