package billing

import (
	"errors"
	"os"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func (b *bank) chargeback(batch uuid.UUID, cash int64) (ChargebackAmounts, error) {
	b.t.Helper()
	var amounts ChargebackAmounts
	err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		amounts, err = ChargeBack(b.t.Context(), tx, Chargeback{Batch: batch, Cash: cash})
		return err
	})
	return amounts, err
}

func (b *bank) reverseChargeback(amounts ChargebackAmounts) error {
	b.t.Helper()
	return b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		return ReverseChargeBack(b.t.Context(), tx, amounts)
	})
}

func (b *bank) paidBatch(credited, paid int64) PaidCredit {
	b.t.Helper()
	p := b.paid(credited, Payment{Currency: currency(b.t, "USD"), Paid: paid, Fee: 0}, uuid.UUID{})
	if err := b.creditPaid(p); err != nil {
		b.t.Fatal(err)
	}
	return p
}

//nolint:cyclop // the test walks the chargeback, its ledger and its reversal in one story
func TestChargebackTakesOnlyItsBatchAndReverses(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	first := b.paidBatch(500, 500)
	second := b.paidBatch(500, 500)
	if got := b.balanceOf(b.admitted.Account); got != 1000 {
		t.Fatalf("balance = %d, want 1000", got)
	}

	amounts, err := b.chargeback(first.ID, 500)
	if err != nil {
		t.Fatal(err)
	}
	if amounts.Batch != first.ID || amounts.Credits != 500 || amounts.Returned != 500 || amounts.Revision != 1 {
		t.Fatalf("chargeback amounts = %+v", amounts)
	}
	if got := b.balanceOf(b.admitted.Account); got != 500 {
		t.Fatalf("balance after chargeback = %d, want 500", got)
	}
	if got := b.count("SELECT charged_back FROM billing.credit_batches WHERE id = '" + first.ID.String() + "'"); got != 500 {
		t.Fatalf("charged_back = %d", got)
	}
	if got := b.count("SELECT charged_back FROM billing.credit_batches WHERE id = '" + second.ID.String() + "'"); got != 0 {
		t.Fatalf("the other batch was charged %d", got)
	}
	b.balanced()

	if err := b.reverseChargeback(amounts); err != nil {
		t.Fatal(err)
	}
	if got := b.balanceOf(b.admitted.Account); got != 1000 {
		t.Fatalf("balance after reversal = %d, want 1000", got)
	}
	if got := b.count("SELECT charged_back FROM billing.credit_batches WHERE id = '" + first.ID.String() + "'"); got != 0 {
		t.Fatalf("charged_back after reversal = %d", got)
	}
	b.balanced()
	if err := b.reverseChargeback(amounts); err == nil {
		t.Fatal("the same chargeback reversed twice")
	}

	// A later dispute of the same payment is a new revision, not the first one replayed.
	again, err := b.chargeback(first.ID, 500)
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != 2 {
		t.Fatalf("second revision = %d, want 2", again.Revision)
	}
	b.balanced()
}

// A charged-back batch can hold a negative remainder. It must still count in the balance, but it must not absorb a
// later charge by making another batch's consumed amount larger. That mismatch between the stored cache and a replay
// was the bug this test pins: the negative batch was not marked touched and was never saved.
func TestChargebackNegativeBatchDoesNotFundSpend(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	first := b.paidBatch(100, 100)
	b.charge(50) // the user spent half before the chargeback took the whole payment back
	if _, err := b.chargeback(first.ID, 100); err != nil {
		t.Fatal(err)
	}
	if got := b.balanceOf(b.admitted.Account); got != -50 {
		t.Fatalf("balance after chargeback = %d, want -50", got)
	}
	second := b.paidBatch(200, 200)
	b.charge(10)
	if ready, err := AdvanceCreditBatches(b.t.Context(), b.app, b.admitted.Scope, b.admitted.Account, 8); err != nil || !ready {
		t.Fatalf("advance the allocation = %v, %v", ready, err)
	}
	if got := b.balanceOf(b.admitted.Account); got != 140 {
		t.Fatalf("balance after spending = %d, want 140", got)
	}
	if got := b.count("SELECT consumed FROM billing.credit_batches WHERE id = '" + first.ID.String() + "'"); got != 50 {
		t.Fatalf("the negative batch consumed %d, want its original 50", got)
	}
	if got := b.count("SELECT amount - consumed - expired - refunded - charged_back FROM billing.credit_batches WHERE id = '" + first.ID.String() + "'"); got != -50 {
		t.Fatalf("the negative batch remaining = %d, want -50", got)
	}
	if got := b.count("SELECT consumed FROM billing.credit_batches WHERE id = '" + second.ID.String() + "'"); got != 10 {
		t.Fatalf("the later batch consumed %d, want 10", got)
	}
	b.checkStored(b.admitted.Account)
}

func TestChargebackRefusesRefundInFlight(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	p := b.paidBatch(500, 500)
	f := Freeze{Account: p.Account, Batch: p.ID, Amount: 100}
	b.freeze(f)
	if _, err := b.chargeback(p.ID, 500); !errors.Is(err, ErrRefundInFlight) {
		t.Fatalf("chargeback with a refund in flight = %v", err)
	}
	if got := b.count("SELECT charged_back FROM billing.credit_batches WHERE id = '" + p.ID.String() + "'"); got != 0 {
		t.Fatalf("charged_back = %d", got)
	}
}

func TestChargebackAfterPartialRefundAndWrongCash(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	p := b.paidBatch(1000, 1000)
	f := Freeze{Account: p.Account, Batch: p.ID, Amount: 400}
	b.freeze(f)
	b.finishRefund(f, false, false)
	if _, err := b.chargeback(p.ID, 599); !errors.Is(err, ErrChargebackCash) {
		t.Fatalf("wrong cash = %v, want ErrChargebackCash", err)
	}
	amounts, err := b.chargeback(p.ID, 600)
	if err != nil {
		t.Fatal(err)
	}
	if amounts.Credits != 600 || amounts.Returned != 600 {
		t.Fatalf("partial chargeback = %+v", amounts)
	}
	if got := b.balanceOf(b.admitted.Account); got != 0 {
		t.Fatalf("balance = %d, want 0", got)
	}
	b.balanced()
}

func TestChargebackCanMakeTheBalanceNegative(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	p := b.paidBatch(1000, 1000)
	// Spend most of the credit; the chargeback still takes the whole paid credit back, so the balance may be negative.
	h := b.holdOn(b.exact, 900)
	b.settleWith(h, outcome(Committed, FinishStop, used(t, 900, 0, 1)))
	if _, err := b.chargeback(p.ID, 1000); err != nil {
		t.Fatal(err)
	}
	if got := b.balanceOf(b.admitted.Account); got != -1000 {
		t.Fatalf("balance = %d, want -1000", got)
	}
	// The account is now below its floor; that is an admission policy, not a ledger inconsistency.
	if got := b.count("SELECT charged_back FROM billing.credit_batches WHERE id = '" + p.ID.String() + "'"); got != 1000 {
		t.Fatalf("charged_back = %d", got)
	}
}

func TestChargebackAfterTheBatchExpired(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	p := b.paidBatch(500, 500)
	b.sql("UPDATE billing.credit_batches SET expires_at = now() - interval '1 day' WHERE id = '" + p.ID.String() + "'")
	if wrote := b.expire(); wrote != 500 {
		t.Fatalf("expiry wrote off %d, want 500", wrote)
	}
	amounts, err := b.chargeback(p.ID, 500)
	if err != nil {
		t.Fatal(err)
	}
	if amounts.Credits != 500 || amounts.Returned != 500 {
		t.Fatalf("expired chargeback = %+v", amounts)
	}
	if got := b.balanceOf(b.admitted.Account); got != -500 {
		t.Fatalf("balance after expired chargeback = %d, want -500", got)
	}
	// The account is below its floor now; the ledger itself must still replay and balance.
	if err := b.reverseChargeback(amounts); err != nil {
		t.Fatal(err)
	}
	if got := b.balanceOf(b.admitted.Account); got != 0 {
		t.Fatalf("balance after reversal = %d, want 0", got)
	}
	b.balanced()
}

func TestChargebackMigrationReruns(t *testing.T) {
	db := pgtest.New(t, Migrations())
	raw, err := os.ReadFile("migrations/00034_chargeback.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, ok := strings.Cut(string(raw), "-- +goose Down")
	if !ok {
		t.Fatal("the migration has no Down section")
	}
	// The first add of the two constraints can fail after an earlier statement committed (the file is NO TRANSACTION).
	// Replaying the Up must replace the constraints rather than meet them by name.
	for range 2 {
		if err := pgtest.Run(t.Context(), db.Superuser, up); err != nil {
			t.Fatalf("replay the chargeback migration: %v", err)
		}
	}
}
