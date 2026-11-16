package billing

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/pg"
)

func TestRefundBooks(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		cross, withFee bool
	}{
		{"same currency, keep fee", false, false},
		{"same currency, return fee", false, true},
		{"cross currency, keep fee", true, false},
		{"cross currency, return fee", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 0, 0, Config{})
			b.checkRefundBooks(tc.cross, tc.withFee)
		})
	}
}

func (b *bank) checkRefundBooks(cross, withFee bool) {
	b.t.Helper()
	p := b.paid(10_000_000_000, Payment{Currency: currency(b.t, "USD"), Paid: 1030, Fee: 30}, uuid.UUID{})
	if cross {
		p.Payment.Currency, p.Payment.Paid, p.Payment.Fee, p.RateID = currency(b.t, "CNY"), 7313, 213, b.rate().ID
	}
	if err := b.creditPaid(p); err != nil {
		b.t.Fatal(err)
	}
	f := Freeze{Account: p.Account, Batch: p.ID, Amount: p.Credited}
	b.freeze(f)
	amounts := b.finishRefund(f, withFee, false)
	wantCash, wantFee := p.Payment.Paid-p.Payment.Fee, int64(0)
	if withFee {
		wantCash, wantFee = p.Payment.Paid, p.Payment.Fee
	}
	if amounts != (RefundAmounts{Returned: wantCash, Fee: wantFee}) {
		b.t.Fatalf("quote = %+v, want cash %d fee %d", amounts, wantCash, wantFee)
	}
	want := map[string]int64{bookUserBalance + "@": p.Credited}
	in := ""
	if cross {
		in = "CNY"
		want[bookExchange+"@"] = -p.Credited
		want[bookExchange+"@CNY"] = (wantCash - wantFee) * 10_000_000
	}
	want[bookCash+"@"+in] = -wantCash * 10_000_000
	if wantFee != 0 {
		want[bookFeeIncome+"@"+in] = wantFee * 10_000_000
	}
	got := make(map[string]int64)
	ledger := b.ledgerOf(f.Batch)
	for _, e := range ledger[len(ledger)-1].Entries {
		got[e.Book+"@"+e.Currency] += e.Amount
	}
	if diff := cmp.Diff(want, got); diff != "" {
		b.t.Fatalf("refund books (-want +got):\n%s", diff)
	}
}

func TestRefundAmountSplits(t *testing.T) {
	t.Parallel()

	for seed := range uint64(200) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			checkRefundSplit(t, seed)
		})
	}
}

func checkRefundSplit(t *testing.T, seed uint64) {
	t.Helper()
	random := rand.New(rand.NewPCG(seed, ^seed)) //nolint:gosec // G404: reproducible property data
	paid := 1 + random.Int64N(1000)
	credit := CreditBatch{Amount: 1 + random.Int64N(500), Payment: &Payment{Currency: currency(t, "USD"), Paid: paid, Fee: random.Int64N(paid)}}
	withFee := seed%2 == 0
	var history RefundHistoryRow
	history.WithFee = withFee
	var refunded, cash, fees int64
	for refunded < credit.Amount {
		r := Refund{Amount: 1 + random.Int64N(min(10, credit.Amount-refunded)), WithFee: withFee}
		amounts, err := r.amounts(credit, refunded, history)
		if err != nil {
			t.Fatal(err)
		}
		if amounts.Returned < 0 || amounts.Fee < 0 || amounts.Fee > amounts.Returned {
			t.Fatalf("invalid parts: %+v", amounts)
		}
		cash += amounts.Returned
		fees += amounts.Fee
		if cash > paid || fees > credit.Payment.Fee {
			t.Fatalf("refunds exceeded payment: %d, %d", cash, fees)
		}
		refunded += r.Amount
		history.Returned, history.Fee = cash*10_000_000, fees*10_000_000
	}
	wantCash, wantFee := paid-credit.Payment.Fee, int64(0)
	if withFee {
		wantCash, wantFee = paid, credit.Payment.Fee
	}
	if cash != wantCash || fees != wantFee {
		t.Fatalf("full split refund cash %d fee %d, want %d %d", cash, fees, wantCash, wantFee)
	}
}

func TestRefundAmountLimits(t *testing.T) {
	t.Parallel()

	credit := CreditBatch{Amount: 1000, Payment: &Payment{Currency: currency(t, "USD"), Paid: 1030, Fee: 30}}
	for _, tc := range []struct {
		name    string
		history RefundHistoryRow
	}{
		{"cash exceeds net", RefundHistoryRow{Returned: 1001 * 10_000_000}},
		{"partial exceeds remaining cap", RefundHistoryRow{Returned: 990 * 10_000_000}},
		{"fee policy changed", RefundHistoryRow{WithFee: true}},
		{"cash not in minor units", RefundHistoryRow{Returned: 1}},
		{"fee was returned under keep policy", RefundHistoryRow{Returned: 50 * 10_000_000, Fee: 1 * 10_000_000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := (Refund{Amount: 400}).amounts(credit, 100, tc.history); err == nil {
				t.Fatal("invalid cash history accepted")
			}
		})
	}
	if got := mulDivFloor(math.MaxInt64, math.MaxInt64, math.MaxInt64); got != math.MaxInt64 {
		t.Fatalf("overflowed product: %d", got)
	}
}

func TestRefundPolicyAndRemainder(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{})
	p := b.paid(1000, Payment{Currency: currency(t, "USD"), Paid: 1031, Fee: 31}, uuid.UUID{})
	if err := b.creditPaid(p); err != nil {
		t.Fatal(err)
	}
	f := Freeze{Account: p.Account, Batch: p.ID, Amount: 1000}
	b.freeze(f)
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		_, err := QuoteRefund(t.Context(), tx, Refund{Freeze: f, WithFee: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	b.finishRefund(f, false, true)
	if b.count("SELECT count(*) FROM billing.credit_batches WHERE refund_with_fee IS NOT NULL") != 0 {
		t.Fatal("quote or release fixed fee policy")
	}
	f.Amount = 300
	b.freeze(f)
	first := b.finishRefund(f, true, false)
	if first != (RefundAmounts{Returned: 309, Fee: 9}) {
		t.Fatalf("first refund: %+v", first)
	}
	f.Amount = 700
	b.freeze(f)
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error { return SettleRefund(t.Context(), tx, Refund{Freeze: f}) }); err == nil {
		t.Fatal("changed a successful refund policy")
	}
	last := b.finishRefund(f, true, false)
	if last != (RefundAmounts{Returned: 722, Fee: 22}) {
		t.Fatalf("final refund did not take remainder: %+v", last)
	}
}

func TestConcurrentRefundAmounts(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{})
	p := b.paid(1000, Payment{Currency: currency(t, "USD"), Paid: 1030, Fee: 30}, uuid.UUID{})
	if err := b.creditPaid(p); err != nil {
		t.Fatal(err)
	}
	f := Freeze{Account: p.Account, Batch: p.ID, Amount: 1000}
	b.freeze(f)
	f.Amount = 125
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() {
			errs[i] = b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error { return SettleRefund(t.Context(), tx, Refund{Freeze: f, WithFee: true}) })
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if b.count("SELECT -sum(e.amount) FROM billing.entries e JOIN billing.transactions t ON e.transaction_id=t.id WHERE t.kind='refund' AND e.book='cash'") != 1030*10_000_000 {
		t.Fatal("concurrent refunds did not return exactly the payment")
	}
	if b.count("SELECT sum(e.amount) FROM billing.entries e JOIN billing.transactions t ON e.transaction_id=t.id WHERE t.kind='refund' AND e.book='fee_income'") != 30*10_000_000 {
		t.Fatal("concurrent refunds did not reverse exactly the fee")
	}
	b.checkStored(f.Account)
	b.balanced()
}
