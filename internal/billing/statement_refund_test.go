package billing

import (
	"testing"

	"axiom/internal/platform/pg"
)

func TestStatementRefundEvents(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(1000)
	b.charge(100)
	b.reverse(40)
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
		{StatementFreeze, 0, 70, 740, 70},
		{StatementUnfreeze, 0, -50, 740, 0},
		{StatementFreeze, 0, 50, 740, 50},
		{StatementRefund, -200, -200, 740, 0},
		{StatementUnfreeze, 0, -100, 940, 200},
		{StatementFreeze, 0, 300, 940, 300},
		{StatementReversal, 40, 0, 940, 0},
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
	// Every possible small page size must preserve both columns, even when a new freeze commits above it.
	for size := 1; size <= len(rows); size++ {
		first, next := b.statement(b.admitted, size, nil)
		if size < len(rows) {
			b.freeze(Freeze{Account: f.Account, Batch: f.Batch, Amount: 1})
		}
		all := first
		for next != nil {
			var page []StatementRow
			page, next = b.statement(b.admitted, size, next)
			all = append(all, page...)
		}
		for i := 1; i < len(all); i++ {
			if all[i-1].Balance-all[i-1].Amount != all[i].Balance || all[i-1].Frozen-all[i-1].FrozenChange != all[i].Frozen {
				t.Fatalf("page size %d, row %d breaks the opening + changes = closing identity", size, i)
			}
		}
		last := all[len(all)-1]
		if last.Balance-last.Amount != 0 || last.Frozen-last.FrozenChange != 0 {
			t.Fatalf("page size %d does not reconcile to zero opening funds: %+v", size, last)
		}
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
