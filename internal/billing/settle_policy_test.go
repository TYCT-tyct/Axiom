package billing

import (
	"fmt"
	"math"
	"testing"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// TestLedgerScope: a settlement writes its revenue and cost entries in its account's scope, and no other account's
// scope reads them.
func TestLedgerScope(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{})
	if _, err := b.billing.Settle(t.Context(), b.hold(100), outcome(Committed, FinishStop, used(t, 50, 20, 0))); err != nil {
		t.Fatal(err)
	}
	var n int64
	other := pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()})
	if err := b.app.Tx(t.Context(), other, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM billing.entries").Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("another account reads %d entries, %v", n, err)
	}
}

// TestSettleBalanceExtremes: the floor cap is computed exactly, so a balance at the top of int64 above a negative
// floor charges in full instead of wrapping to nothing.
func TestSettleBalanceExtremes(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{})
	h := b.hold(100)
	b.sql(fmt.Sprintf("UPDATE billing.balances SET balance = %d, floor = -1", int64(math.MaxInt64)))
	if f, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 50, 20, 0))); err != nil || f.Charged != platform.AmountExact(70) {
		t.Errorf("Settle = %+v, %v; want 70 charged", f, err)
	}
}

func TestLedgerPoliciesPassAudit(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	if err := b.app.Check(t.Context(), Migrations()); err != nil {
		t.Fatal(err)
	}
}
