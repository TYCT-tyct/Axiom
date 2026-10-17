package billing

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// credited is a bank in which one credit of 500, expiring in an hour, was made.
func credited(t *testing.T) (*bank, Credit) {
	t.Helper()
	b := newBank(t, 0, 0, Config{})
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	c := Credit{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Amount: 500, ExpiresAt: &expires,
		Actor: audit.System("billing.test"), Reason: "a gift",
	}
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error { return AddCredit(t.Context(), tx, c) }); err != nil {
		t.Fatal(err)
	}
	return b, c
}

func (b *bank) readCredit(scope pg.Scope, id uuid.UUID) (batch CreditBatch, ok bool) {
	b.t.Helper()
	if err := b.app.ReadTx(b.t.Context(), scope, func(tx pg.Tx) (err error) {
		batch, ok, err = ReadCredit(b.t.Context(), tx, id)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return batch, ok
}

// TestReadCredit: the batch a credit made is read back with its amount, expiry and the time it was made; a batch of
// nobody's is not found.
func TestReadCredit(t *testing.T) {
	t.Parallel()
	b, c := credited(t)
	got, ok := b.readCredit(b.platform, c.ID)
	if !ok || got.ID != c.ID || got.Site != c.Site || got.Account != c.Account || got.Amount != 500 || got.ExpiresAt == nil ||
		!got.ExpiresAt.Equal(*c.ExpiresAt) || time.Since(got.CreatedAt) > time.Minute {
		t.Errorf("the batch: %+v %v", got, ok)
	}
	if _, ok := b.readCredit(b.platform, uuid.NewV7()); ok {
		t.Error("a batch nobody made was found")
	}
}

// TestReadCreditIsTenantScoped: an account's own scope reads its batch, and another account's cannot.
func TestReadCreditIsTenantScoped(t *testing.T) {
	t.Parallel()
	b, c := credited(t)
	if _, ok := b.readCredit(b.admitted.Scope, c.ID); !ok {
		t.Error("the account's own scope cannot read its batch")
	}
	if _, ok := b.readCredit(pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()}), c.ID); ok {
		t.Error("another account's scope read the batch")
	}
}
