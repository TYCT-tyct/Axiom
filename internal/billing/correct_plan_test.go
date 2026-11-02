package billing

import (
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/suppliers"
)

// planEstimate grants b a one-window plan and settles one estimated plan charge of 100, returning the hold and the
// subscription.
func planEstimate(t *testing.T, b *bank) (*Hold, Subscription) {
	t.Helper()
	sub := b.subscribe(b.makePlan(300, 3600), uuid.NewV7())
	h := b.holdOn(b.exact, 100)
	f := b.settleWith(h, outcome(Committed, FinishStop, used(t, 100, 0, 0, in, catalog.ItemOutput, webSearch)))
	if !f.Estimated || f.Charged.CeilAmount() != 100 {
		t.Fatalf("estimate = %+v, want an estimated 100", f)
	}
	return h, sub
}

// planCorrectionEvent is the supplier's complete record of the same call at 50.
func planCorrectionEvent(t *testing.T, h *Hold) suppliers.ChargeEvent {
	t.Helper()
	return suppliers.ChargeEvent{
		ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1,
		Usage: usageOf(t, map[catalog.Item]int64{in: 50}), Reported: 1<<in | 1<<catalog.ItemOutput | 1<<webSearch,
	}
}

// TestCorrectionOfPlanCharges: a correction follows the source that actually paid. A plan charge is corrected against
// its window and liability without pretending the wallet moved; a written-off plan draws the difference from expired
// income; and a plan hold cleanup released, whose late settlement paid from the wallet, is corrected against the wallet.
func TestCorrectionOfPlanCharges(t *testing.T) {
	t.Parallel()
	t.Run("a plan charge keeps the wallet allocation untouched", correctionNormalPlan)
	t.Run("a written-off plan draws the difference from expired income", correctionWrittenOffPlan)
	t.Run("a late wallet charge is corrected against the wallet", correctionLateWallet)
}

func correctionNormalPlan(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, fallbackBankConfig)
	h, _ := planEstimate(t, b)
	c := &corrector{db: b.app, scope: b.platform}
	if borne, err := c.correct(t.Context(), planCorrectionEvent(t, h)); err != nil || borne != 0 {
		t.Fatalf("correct = %d borne, %v", borne, err)
	}
	if n := b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); n != 50 {
		t.Errorf("charged %d after the correction, want 50", n)
	}
	if w := b.window(1); w.Used != 50 {
		t.Errorf("window 1 used %d after the correction, want 50", w.Used)
	}
	if n := b.book(bookPlanLiability); n != -250 {
		t.Errorf("plan liability %d after the correction, want -250", n)
	}
	if n := b.balanceOf(b.admitted.Account); n != 1000 {
		t.Errorf("balance %d after a plan correction, want 1000", n)
	}
	b.balanced()
}

func correctionWrittenOffPlan(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, fallbackBankConfig)
	h, sub := planEstimate(t, b)
	b.sql("UPDATE billing.subscriptions SET starts_at = now() - interval '2 hours', ends_at = now() - interval '1 hour' "+
		"WHERE id = '"+sub.ID.String()+"'",
		"UPDATE billing.subscription_windows SET starts_at = now() - interval '2 hours', ends_at = now() - interval '1 hour' "+
			"WHERE subscription_id = '"+sub.ID.String()+"'")
	if w := b.advance(); w != nil {
		t.Fatalf("a window of an ended term funded a request: %+v", w)
	}
	b.writeOff()
	if b.subscribed().ClosedAt == nil {
		t.Fatal("the subscription did not close")
	}
	c := &corrector{db: b.app, scope: b.platform}
	if borne, err := c.correct(t.Context(), planCorrectionEvent(t, h)); err != nil || borne != 0 {
		t.Fatalf("correct = %d borne, %v", borne, err)
	}
	if n := b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); n != 50 {
		t.Errorf("charged %d after the correction, want 50", n)
	}
	if w := b.window(1); w.Used != 50 {
		t.Errorf("window 1 used %d after the correction, want 50", w.Used)
	}
	if n := b.book(bookPlanLiability); n != 0 {
		t.Errorf("plan liability %d after a closed plan's correction, want 0", n)
	}
	if n := b.book(bookCreditExpired); n != -250 {
		t.Errorf("expired credit %d, want -250: 200 written off and 50 returned", n)
	}
	if n := b.balanceOf(b.admitted.Account); n != 1000 {
		t.Errorf("balance %d after a plan correction, want 1000", n)
	}
	b.balanced()
}

func correctionLateWallet(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, fallbackBankConfig)
	b.subscribe(b.makePlan(300, 3600), uuid.NewV7())
	h := b.holdOn(b.exact, 100)
	b.sql("UPDATE billing.reservations SET deadline = now() - interval '1 minute' WHERE request_id = '" + h.id.String() + "'")
	if n, err := b.billing.Cleanup(t.Context(), 10); err != nil || n != 1 {
		t.Fatalf("Cleanup = %d, %v; want the hold released", n, err)
	}
	f := b.settleWith(h, outcome(Committed, FinishStop, used(t, 100, 0, 0, in, catalog.ItemOutput, webSearch)))
	if !f.Estimated || f.Charged.CeilAmount() != 100 {
		t.Fatalf("late settlement = %+v, want an estimated 100", f)
	}
	if source := b.text("SELECT funding FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); source != "wallet" {
		t.Fatalf("the late settlement records source %q, want wallet", source)
	}
	c := &corrector{db: b.app, scope: b.platform}
	if borne, err := c.correct(t.Context(), planCorrectionEvent(t, h)); err != nil || borne != 0 {
		t.Fatalf("correct = %d borne, %v", borne, err)
	}
	b.wantReservation(h, "settled", 50, false)
	if n := b.balanceOf(b.admitted.Account); n != 950 {
		t.Errorf("balance %d after correcting the wallet charge, want 950", n)
	}
	if w := b.window(1); w.Used != 0 || w.Reserved != 0 {
		t.Errorf("window 1 is %+v after a wallet-paid late settlement, want untouched", w)
	}
	if n := b.book(bookPlanLiability); n != -300 {
		t.Errorf("plan liability %d after a wallet correction, want the untouched grant -300", n)
	}
	b.balanced()
	b.replayed(b.admitted.Account)
}

// TestCorrectionAfterAFullRefundBooksNothingButTheCounters: a refund already reversed the call's revenue and left no
// expired income. A later correction changes what the reservation and window report, but must not invent a
// credit_expired entry for a window that was voided rather than expired.
func TestCorrectionAfterAFullRefundBooksNothingButTheCounters(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, fallbackBankConfig)
	grant := uuid.NewV7()
	sub := b.subscribePaid(b.makePlan(300, 3600), grant, 100)
	h := b.holdOn(b.exact, 100)
	f := b.settleWith(h, outcome(Committed, FinishStop, used(t, 100, 0, 0, in, catalog.ItemOutput, webSearch)))
	if !f.Estimated || f.Charged.CeilAmount() != 100 {
		t.Fatalf("estimate = %+v, want an estimated 100", f)
	}
	if _, err := b.refund(SubscriptionRefundRequest{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Grant: grant, SiteCurrency: usd,
		Credits: 1_000_000_000, Reason: "full", Actor: audit.System("billing.test"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := b.subscriptionByID(sub.ID); got.State != "expired" || got.ClosedAt == nil {
		t.Fatalf("subscription after refund = %+v, want expired and closed", got)
	}
	c := &corrector{db: b.app, scope: b.platform}
	if borne, err := c.correct(t.Context(), planCorrectionEvent(t, h)); err != nil || borne != 0 {
		t.Fatalf("correct = %d borne, %v", borne, err)
	}
	if n := b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); n != 50 {
		t.Errorf("charged %d after the correction, want 50", n)
	}
	if n := b.book(bookPlanLiability); n != 0 {
		t.Errorf("plan liability = %d, want 0", n)
	}
	if n := b.book(bookRevenue); n != 0 {
		t.Errorf("revenue = %d, want 0: the refund already reversed the charge", n)
	}
	if n := b.book(bookCreditExpired); n != 0 {
		t.Errorf("expired credit = %d, want 0: no term expired", n)
	}
	b.balanced()
}
