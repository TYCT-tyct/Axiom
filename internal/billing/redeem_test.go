package billing

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

func (b *bank) redeem(c RedeemCredit) error {
	b.t.Helper()
	c.Site, c.Account, c.Actor, c.Reason = b.admitted.Site, b.admitted.Account, audit.System("billing.test"), "a redeem code"
	return b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error { return CreditRedeem(b.t.Context(), tx, c) })
}

// TestCreditRedeem: a redeemed code is a batch of its own source, booked as the gift expense every code is, with the
// expiry the code carried; the same credit again changes nothing, and one of another amount is a conflict.
func TestCreditRedeem(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	c := RedeemCredit{ID: uuid.NewV7(), Amount: 250, ExpiresAt: &expires}
	if err := b.redeem(c); err != nil {
		t.Fatal(err)
	}
	got, ok := b.readCredit(b.platform, c.ID)
	if !ok || got.Source != "redeem" || got.Amount != 250 || got.Payment != nil || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("the batch: %+v %v", got, ok)
	}
	if balance := b.balanceOf(b.admitted.Account); balance != 250 {
		t.Fatalf("balance %d, want 250", balance)
	}
	if entries := b.count("SELECT count(*) FROM billing.entries e JOIN billing.transactions t ON t.id = e.transaction_id WHERE t.request_id = '" + c.ID.String() + "'"); entries != 2 {
		t.Fatalf("%d ledger entries, want the gift expense and the balance", entries)
	}
	if err := b.redeem(c); err != nil {
		t.Fatalf("the same credit again: %v", err)
	}
	if balance := b.balanceOf(b.admitted.Account); balance != 250 {
		t.Fatalf("the same credit again left %d, want 250", balance)
	}
	c.Amount = 251
	if err := b.redeem(c); !errors.Is(err, ErrCreditConflict) {
		t.Fatalf("another amount under the same ID: %v, want ErrCreditConflict", err)
	}
}
