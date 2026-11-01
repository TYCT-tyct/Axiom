package billing

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
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

func (b *bank) paidBatch(credited, paid, fee int64) PaidCredit {
	b.t.Helper()
	p := b.paid(credited, Payment{Currency: currency(b.t, "USD"), Paid: paid, Fee: fee}, uuid.UUID{})
	if err := b.creditPaid(p); err != nil {
		b.t.Fatal(err)
	}
	return p
}

func TestChargebackTakesOnlyItsBatchAndReverses(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	first := b.paidBatch(500, 500, 0)
	second := b.paidBatch(500, 500, 0)
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

func TestChargebackRefusesRefundInFlight(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	p := b.paidBatch(500, 500, 0)
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
	p := b.paidBatch(1000, 1000, 0)
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
	p := b.paidBatch(1000, 1000, 0)
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
