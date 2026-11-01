package payments

import (
	"testing"

	"axiom/internal/platform/pg"
)

func disputeWorld(t *testing.T) (*world, Merchant, Order) {
	t.Helper()
	w := newTestWorld(t, fakePlugin{create: goodCheckout, caps: CapRefund | CapRefundKey})
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA)
	in.RefundEnabled, in.UserRefund = true, true
	merchant := w.mustSave(siteScope(w.siteA), in)
	o := w.mustPlace(1000, "dispute")
	w.receive(receipt(o))
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatalf("order state = %s", state)
	}
	return w, merchant, o
}

func Dispute(provider, id, intent string, amount Money, action DisputeAction, status string) Notice {
	return Notice{Dispute: &DisputeNotice{
		Provider: provider, ID: id, PaymentIntent: intent, Amount: amount, Action: action, Status: status,
	}, Raw: "evt " + id}
}

func TestDisputeAppliesOnceAndWinsBack(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	before := int64(w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", o.AccountID))
	n := Dispute("stripe", "dp_1", o.TradeNo, o.Total, DisputeOpen, "needs_response")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), n); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := w.stored(o.ID); state != "charged_back" {
		t.Fatalf("state after chargeback = %s", state)
	}
	if got := int64(w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", o.AccountID)); got != before-o.Credits {
		t.Fatalf("balance = %d, want %d", got, before-o.Credits)
	}
	var state string
	var credits, cash, fee, revision int64
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT state, credits, cash, fee, chargeback_revision FROM payments.order_disputes WHERE provider_id = 'dp_1'").Scan(&state, &credits, &cash, &fee, &revision)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "applied" || credits != o.Credits || cash != o.Total.Minor || fee != 0 || revision != 1 {
		t.Fatalf("dispute row = %s %d %d %d %d", state, credits, cash, fee, revision)
	}

	// The same event is idempotent: it neither moves money nor writes another order event.
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), n); err != nil {
		t.Fatal(err)
	}
	if got := int64(w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", o.AccountID)); got != before-o.Credits {
		t.Fatalf("duplicate changed balance to %d", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.order_events WHERE order_id = $1 AND kind = 'dispute'", o.ID); got != 1 {
		t.Fatalf("dispute events = %d", got)
	}

	won := Dispute("stripe", "dp_1", o.TradeNo, o.Total, DisputeWon, "won")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), won); err != nil {
		t.Fatal(err)
	}
	if got := int64(w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", o.AccountID)); got != before {
		t.Fatalf("balance after win = %d, want %d", got, before)
	}
	if state, _, _ := w.stored(o.ID); state != "charged_back" {
		t.Fatalf("a won dispute changed the order to %s", state)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.order_disputes WHERE provider_id = 'dp_1' AND state = 'reversed'"); got != 1 {
		t.Fatalf("dispute row state query = %d", got)
	}
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), won); err != nil {
		t.Fatalf("a repeated win: %v", err)
	}
}

func TestDisputeWarningThenOpen(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	warning := Dispute("stripe", "dp_2", o.TradeNo, o.Total, DisputeWarning, "warning_needs_response")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), warning); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatalf("warning changed the order to %s", state)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.order_disputes WHERE provider_id = 'dp_2' AND state = 'recorded'"); got != 1 {
		t.Fatalf("warning row state query = %d", got)
	}
	// A warning alone never charges; the later needs_response does.
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_2", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := w.stored(o.ID); state != "charged_back" {
		t.Fatalf("state after open = %s", state)
	}
}

func TestDisputeUnmatchedAndAmountMismatchBecomeDiscrepancies(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_x", "pi_unknown", o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state = 'open'"); got == 0 {
		t.Fatal("an unmatched dispute made no discrepancy")
	}
	before := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state = 'open'")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_y", o.TradeNo, Money{Currency: o.Total.Currency, Minor: o.Total.Minor + 1}, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state = 'open'"); got != before+1 {
		t.Fatalf("amount mismatch discrepancies = %d, want %d", got, before+1)
	}
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatalf("a mismatched amount changed the order to %s", state)
	}
}

func TestDailyCashReconcilesChargebackAndReversal(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	day := w.accountingDay(o.ID)
	// The chargeback and the payment land on the same accounting day, so expected cash is paid total minus the
	// chargeback; a missing subtraction would open a ledger discrepancy here.
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_cash", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if err := w.p.reconcileMerchant(t.Context(), merchant.ID, day); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state = 'open'"); got != 0 {
		t.Fatalf("a chargeback opened %d discrepancies", got)
	}
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_cash", o.TradeNo, o.Total, DisputeWon, "won")); err != nil {
		t.Fatal(err)
	}
	if err := w.p.reconcileMerchant(t.Context(), merchant.ID, day); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state = 'open'"); got != 0 {
		t.Fatalf("a won chargeback opened %d discrepancies", got)
	}
}

func TestWonDisputeBeforeOpenDoesNotBookMoney(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	// Stripe can deliver the settlement before the event that opened the dispute. The win is recorded, and the late
	// open must not take money the win already refused.
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_late", o.TradeNo, o.Total, DisputeWon, "won")); err != nil {
		t.Fatal(err)
	}
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_late", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatalf("late open moved a won dispute to %s", state)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM billing.transactions WHERE kind='chargeback'"); got != 0 {
		t.Fatalf("late open booked %d chargebacks", got)
	}
}

func TestSecondDisputeOnOnePaymentNeedsAPerson(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_first", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_second", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM billing.transactions WHERE kind='chargeback'"); got != 1 {
		t.Fatalf("a second dispute booked %d chargebacks", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state='open' AND detail->>'kind'='dispute_second'"); got != 1 {
		t.Fatalf("a second dispute opened %d discrepancies", got)
	}
}
