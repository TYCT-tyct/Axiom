package payments

import (
	"fmt"
	"testing"
	"time"
	"uuid"

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

//nolint:cyclop // the dispute lifecycle and its ledger assertions are one test
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
	for range 2 {
		if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_late", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
			t.Fatal(err)
		}
	}
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatalf("late open moved a won dispute to %s", state)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.order_disputes WHERE provider_id='dp_late' AND status='won'"); got != 1 {
		t.Fatalf("a stale open overwrote the won status: %d rows still won", got)
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

func TestDisputeTimestampsUseTheLedgerDayAcrossMidnight(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	// Put the payment's batch on the previous UTC day, then make the chargeback and its reversal post just before
	// today's UTC midnight. The clock in this process is not the ledger's clock: the dispute row must take the
	// transaction time, or the two days' reconciliation each see a different day for the same cash.
	old := time.Now().UTC().AddDate(0, 0, -1).Truncate(24 * time.Hour)
	w.exec(fmt.Sprintf("UPDATE billing.credit_batches SET created_at='%s'::timestamptz WHERE id='%s'", old.Format(time.RFC3339), uuid.UUID(o.ID)))
	w.exec(`CREATE FUNCTION billing.previous_dispute_day() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind IN ('chargeback','chargeback_reverse') THEN NEW.created_at := date_trunc('day',clock_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 microsecond'; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER previous_dispute_day BEFORE INSERT ON billing.transactions FOR EACH ROW EXECUTE FUNCTION billing.previous_dispute_day()`)

	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_midnight", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, `SELECT count(*) FROM payments.order_disputes d JOIN billing.transactions t
		ON t.request_id=d.order_id AND t.attempt_seq=0 AND t.kind='chargeback' AND t.revision=d.chargeback_revision
		WHERE d.provider_id='dp_midnight' AND d.applied_at=t.created_at`); got != 1 {
		t.Fatalf("the applied time does not match the chargeback transaction: %d", got)
	}
	var day time.Time
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT date_trunc('day',applied_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' FROM payments.order_disputes WHERE provider_id='dp_midnight'`).Scan(&day)
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.p.reconcileMerchant(t.Context(), merchant.ID, day); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state='open'"); got != 0 {
		t.Fatalf("the chargeback's ledger day opened %d discrepancies", got)
	}

	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_midnight", o.TradeNo, o.Total, DisputeWon, "won")); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, `SELECT count(*) FROM payments.order_disputes d JOIN billing.transactions t
		ON t.request_id=d.order_id AND t.attempt_seq=0 AND t.kind='chargeback_reverse' AND t.revision=d.chargeback_revision
		WHERE d.provider_id='dp_midnight' AND d.reversed_at=t.created_at`); got != 1 {
		t.Fatalf("the reversed time does not match the reversal transaction: %d", got)
	}
	if err := w.p.reconcileMerchant(t.Context(), merchant.ID, day); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state='open'"); got != 0 {
		t.Fatalf("the reversal's ledger day opened %d discrepancies", got)
	}
}
