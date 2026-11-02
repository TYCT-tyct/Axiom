package billing

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// subscribePaid grants a paid plan: 100 minor units buy one credit, so paid 400 buys four windows of 1 credit.
func (b *bank) subscribePaid(p Plan, id uuid.UUID, paid int64) Subscription {
	b.t.Helper()
	var sub Subscription
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		if err := CreditSubscription(b.t.Context(), tx, SubscriptionGrant{
			ID: id, Site: b.admitted.Site, Account: b.admitted.Account, Plan: p.ID, SiteCurrency: usd, Source: "paid",
			Payment: Payment{Currency: usd, Paid: paid}, Channel: Channel{Offline: "bank"},
			Actor: audit.System("billing.test"), Reason: "test",
		}); err != nil {
			return err
		}
		subs, err := Subscriptions(b.t.Context(), tx, b.admitted.Account, 10)
		if err != nil {
			return err
		}
		sub = subs[0]
		return nil
	}); err != nil {
		b.t.Fatal(err)
	}
	return sub
}

func (b *bank) refund(req SubscriptionRefundRequest) (SubscriptionRefund, error) {
	b.t.Helper()
	var out SubscriptionRefund
	err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		out, err = applySubscriptionRefund(b.t.Context(), tx, req)
		return err
	})
	return out, err
}

// TestSubscriptionRefundRevokes: a full refund takes the whole term back, revokes the subscription, and leaves the
// plan liability at zero. A replay returns the stored refund and posts nothing again.
//
//nolint:cyclop // one full-refund journey: the replay, the ledger and the term are one flow.
func TestSubscriptionRefundRevokes(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	plan := b.makePlan(1_000_000_000, 4*3600)
	grant := uuid.NewV7()
	sub := b.subscribePaid(plan, grant, 400)
	req := SubscriptionRefundRequest{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Grant: grant, SiteCurrency: usd,
		Credits: 4_000_000_000, Reason: "customer asked", Actor: audit.System("billing.test"),
	}
	out, err := b.refund(req)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Revoked || out.Windows != 4 || out.Credits != 4_000_000_000 || out.Paid != 400 || out.Unused != 4_000_000_000 {
		t.Errorf("refund = %+v", out)
	}
	got := b.subscriptionByID(sub.ID)
	if got.State != "expired" || got.ClosedAt == nil {
		t.Errorf("subscription after the refund = %+v, want expired and closed", got)
	}
	if n := b.book(bookPlanLiability); n != 0 {
		t.Errorf("plan liability = %d, want 0", n)
	}
	if n := b.book(bookCash); n != 0 {
		t.Errorf("net cash = %d, want 0 (the grant's +400 and the refund's -400)", n)
	}
	if n := b.book(bookRevenue); n != 0 {
		t.Errorf("revenue = %d, want 0 (nothing was used)", n)
	}
	b.balanced()

	// The same request again is the stored result; the ledger and the grant do not move.
	again, err := b.refund(req)
	if err != nil {
		t.Fatal(err)
	}
	if again != out {
		t.Errorf("replay = %+v, want %+v", again, out)
	}
	if n := b.book(bookCash); n != 0 {
		t.Errorf("net cash after the replay = %d, want 0", n)
	}
	if w := b.window(1); w.Seq != 1 {
		t.Errorf("window 1 disappeared: %+v", w)
	}
}

// TestSubscriptionRefundPartialTakesTheTail: half the credits take back the last half of the windows; the subscription
// stays in force with its term shortened, then the rest of the credits revoke it.
//
//nolint:cyclop // one partial-refund journey: two refunds and their term are one flow.
func TestSubscriptionRefundPartialTakesTheTail(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	plan := b.makePlan(1_000_000_000, 4*3600)
	grant := uuid.NewV7()
	sub := b.subscribePaid(plan, grant, 400)
	out, err := b.refund(SubscriptionRefundRequest{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Grant: grant, SiteCurrency: usd,
		Credits: 2_000_000_000, Reason: "half", Actor: audit.System("billing.test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Revoked || out.Windows != 2 || out.Paid != 200 || out.Unused != 2_000_000_000 {
		t.Errorf("partial refund = %+v", out)
	}
	got := b.subscriptionByID(sub.ID)
	if got.State != "active" || got.ClosedAt != nil {
		t.Errorf("subscription after the partial refund = %+v, want active", got)
	}
	if !got.EndsAt.Equal(b.window(2).EndsAt) {
		t.Errorf("term ends at %v, want window 2's end %v", got.EndsAt, b.window(2).EndsAt)
	}
	if w := b.window(3); w.Reserved != 0 {
		t.Errorf("window 3 still funds: %+v", w)
	}
	if n := b.book(bookPlanLiability); n != -2_000_000_000 {
		t.Errorf("plan liability = %d, want the remaining -2 credits", n)
	}
	b.balanced()

	out, err = b.refund(SubscriptionRefundRequest{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Grant: grant, SiteCurrency: usd,
		Credits: 2_000_000_000, Reason: "the rest", Actor: audit.System("billing.test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Revoked || out.Windows != 2 || out.Paid != 200 || out.Unused != 2_000_000_000 {
		t.Errorf("second refund = %+v", out)
	}
	if n := b.book(bookPlanLiability); n != 0 {
		t.Errorf("plan liability after the rest = %d, want 0", n)
	}
	if n := b.book(bookCash); n != 0 {
		t.Errorf("net cash = %d, want 0 (the grant's +400 and the refund's -400)", n)
	}
	b.balanced()
}

// TestSubscriptionRefundRefusals: a gift, a window with a hold, a partial refund that would take the current window,
// and more credits than are left are all refused without changing anything.
func TestSubscriptionRefundRefusals(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	gift := uuid.NewV7()
	b.subscribe(b.makePlan(1_000_000_000, 3600), gift)
	_, err := b.refund(SubscriptionRefundRequest{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Grant: gift, SiteCurrency: usd,
		Credits: 1, Reason: "gift", Actor: audit.System("billing.test"),
	})
	if !errors.Is(err, ErrNotRefundable) {
		t.Errorf("refunding a gift = %v, want ErrNotRefundable", err)
	}

	paid := uuid.NewV7()
	other := b.accountAtSite()
	otherPlan := b.makePlan(1_000_000_000, 2*3600)
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		return CreditSubscription(b.t.Context(), tx, SubscriptionGrant{
			ID: paid, Site: other.Site, Account: other.Account, Plan: otherPlan.ID,
			SiteCurrency: usd, Source: "paid", Payment: Payment{Currency: usd, Paid: 200},
			Channel: Channel{Offline: "bank"}, Actor: audit.System("billing.test"), Reason: "test",
		})
	}); err != nil {
		t.Fatal(err)
	}
	held := b.holdFor(other, b.exact, 100)
	_, err = b.refund(SubscriptionRefundRequest{
		ID: uuid.NewV7(), Site: other.Site, Account: other.Account, Grant: paid, SiteCurrency: usd,
		Credits: 2_000_000_000, Reason: "held", Actor: audit.System("billing.test"),
	})
	if !errors.Is(err, ErrRefundHeld) {
		t.Errorf("refunding a held window = %v, want ErrRefundHeld", err)
	}
	_ = held
}

// TestSubscriptionRefundReversesTheSalePrice: a plan sold below its face reverses its discount, and one sold above
// face reverses the extra revenue, so neither account keeps a balance after a full refund of an unused term.
func TestSubscriptionRefundReversesTheSalePrice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		paid int64 // 100 minor units buy one credit; the plan's face is four credits
	}{
		{"below face", 300},
		{"above face", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBank(t, 0, 0, Config{})
			grant := uuid.NewV7()
			b.subscribePaid(b.makePlan(1_000_000_000, 4*3600), grant, tc.paid)
			if _, err := b.refund(SubscriptionRefundRequest{
				ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Grant: grant, SiteCurrency: usd,
				Credits: tc.paid * 10_000_000, Reason: "full", Actor: audit.System("billing.test"),
			}); err != nil {
				t.Fatal(err)
			}
			for _, book := range []string{bookPlanLiability, bookPlanDiscount, bookRevenue, bookCash, bookFeeIncome, bookExchange} {
				if n := b.book(book); n != 0 {
					t.Errorf("%s = %d after the refund, want 0", book, n)
				}
			}
			b.balanced()
		})
	}
}

// TestSubscriptionRefundCashFloorsCumulatively: four windows paid two minor units are worth twenty million credits.
// Refunding one window is below one minor unit and is refused; two windows return one unit, and the last two return
// the remaining unit. No earlier round-up may consume cash that later windows still represent.
func TestSubscriptionRefundCashFloorsCumulatively(t *testing.T) {
	t.Parallel()
	paid, fee := int64(2), int64(0)
	g := ReadGrantRow{Paid: &paid, Fee: &fee}
	rg := refundCase{grant: g, value: 20_000_000, remaining: 20_000_000}
	if _, _, err := refundCash(SubscriptionRefundRequest{Credits: 5_000_000}, rg); !errors.Is(err, ErrNotRefundable) {
		t.Fatalf("refund below one minor unit = %v, want ErrNotRefundable", err)
	}
	rg.grant.RefundedValue, rg.grant.RefundedPaid = 0, 0
	rg.remaining = 10_000_000
	rg.full = false
	got, gotFee, err := refundCash(SubscriptionRefundRequest{Credits: 10_000_000}, rg)
	if err != nil || got != 1 || gotFee != 0 {
		t.Fatalf("first half = %d/%d, %v; want 1/0", got, gotFee, err)
	}
	rg.grant.RefundedValue, rg.grant.RefundedPaid = 10_000_000, 1
	rg.remaining = 10_000_000
	rg.full = true
	got, gotFee, err = refundCash(SubscriptionRefundRequest{Credits: 10_000_000}, rg)
	if err != nil || got != 1 || gotFee != 0 {
		t.Fatalf("last half = %d/%d, %v; want the remaining 1/0", got, gotFee, err)
	}
}

// TestSubscriptionRefundOfRenewalExpiresTheTerm: the first grant's windows ended and the current window belongs to the
// renewal. Refunding the renewal in full must expire and write off the subscription, not shorten it to a past instant
// while leaving state active and current_seq on a voided window.
func TestSubscriptionRefundOfRenewalExpiresTheTerm(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	p := b.makePlan(1_000_000_000, 2*3600)
	first := b.subscribePaid(p, uuid.NewV7(), 200)
	second := uuid.NewV7()
	b.subscribePaid(p, second, 200)
	b.sql("UPDATE billing.subscription_windows SET starts_at = now() - interval '2 hours', "+
		"ends_at = now() - interval '1 hour' WHERE subscription_id = '"+first.ID.String()+"' AND seq IN (1,2)",
		"UPDATE billing.subscription_windows SET starts_at = now() - interval '1 minute', "+
			"ends_at = now() + interval '59 minutes' WHERE subscription_id = '"+first.ID.String()+"' AND seq = 3",
		"UPDATE billing.subscriptions SET starts_at = now() - interval '3 hours', ends_at = now() + interval '2 hours', "+
			"current_seq = 3 WHERE id = '"+first.ID.String()+"'")
	if _, err := b.refund(SubscriptionRefundRequest{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Grant: second, SiteCurrency: usd,
		Credits: 2_000_000_000, Reason: "refund the renewal", Actor: audit.System("billing.test"),
	}); err != nil {
		t.Fatal(err)
	}
	got := b.subscriptionByID(first.ID)
	if got.State != "expired" || got.ClosedAt == nil {
		t.Fatalf("subscription after refunding the renewal = %+v, want expired and closed", got)
	}
	if n := b.book(bookPlanLiability); n != 0 {
		t.Errorf("plan liability = %d, want 0", n)
	}
	if n := b.book(bookCreditExpired); n != -2_000_000_000 {
		t.Errorf("expired credit = %d, want the first grant's -2 credits", n)
	}
	b.balanced()
}

// TestSubscriptionRefundThenExpiry: a partial refund leaves the earlier windows' liability, which the expiry job
// writes off once; the refund and write-off do not leave a positive liability.
func TestSubscriptionRefundThenExpiry(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	grant := uuid.NewV7()
	sub := b.subscribePaid(b.makePlan(1_000_000_000, 4*3600), grant, 400)
	if _, err := b.refund(SubscriptionRefundRequest{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Grant: grant, SiteCurrency: usd,
		Credits: 2_000_000_000, Reason: "half", Actor: audit.System("billing.test"),
	}); err != nil {
		t.Fatal(err)
	}
	b.sql("UPDATE billing.subscriptions SET starts_at = now() - interval '5 hours', ends_at = now() - interval '1 hour' "+
		"WHERE id = '"+sub.ID.String()+"'",
		"UPDATE billing.subscription_windows SET starts_at = starts_at - interval '5 hours', "+
			"ends_at = ends_at - interval '5 hours' WHERE subscription_id = '"+sub.ID.String()+"'")
	if w := b.advance(); w != nil {
		t.Fatalf("an ended term funded %+v", w)
	}
	b.writeOff()
	if n := b.book(bookPlanLiability); n != 0 {
		t.Errorf("plan liability after expiry = %d, want 0", n)
	}
	if n := b.book(bookCreditExpired); n != -2_000_000_000 {
		t.Errorf("expired credit = %d, want the remaining -2 credits", n)
	}
	b.balanced()
}

// TestSubscriptionRefundNeedsASecondOperator: a request changes nothing until a different operator approves it; the
// requester cannot approve, and replaying the approval returns the stored refund without moving the ledger again.
func TestSubscriptionRefundNeedsASecondOperator(t *testing.T) { //nolint:cyclop,gocognit // request, refusal, approval and replay in one journey.
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	grant := uuid.NewV7()
	sub := b.subscribePaid(b.makePlan(1_000_000_000, 3600), grant, 100)
	requester, approver := uuid.NewV7(), uuid.NewV7()
	proposalID := uuid.NewV7()
	var proposal SubscriptionRefundProposal
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		var err error
		proposal, err = RequestSubscriptionRefund(t.Context(), tx, SubscriptionRefundProposalRequest{
			ID: proposalID, Site: b.admitted.Site, Account: b.admitted.Account, Grant: grant, SiteCurrency: usd,
			Credits: 1_000_000_000, Reason: "customer asked", RequestedBy: requester, Actor: audit.System("billing.test"),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if proposal.State != "pending" || proposal.Credits != 1_000_000_000 {
		t.Fatalf("proposal = %+v, want pending", proposal)
	}
	if n := b.book(bookPlanLiability); n != -1_000_000_000 {
		t.Errorf("liability after the request = %d, want the untouched grant -1 credit", n)
	}
	if n := b.book(bookCash); n != 1_000_000_000 {
		t.Errorf("cash after the request = %d, want the untouched payment 100", n)
	}
	if _, err := b.approve(proposalID, requester, "self"); !errors.Is(err, ErrRefundSecondPerson) {
		t.Errorf("requester approval = %v, want ErrRefundSecondPerson", err)
	}
	out, err := b.approve(proposalID, approver, "checked")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Revoked || out.Paid != 100 {
		t.Errorf("approved refund = %+v", out)
	}
	if got := b.subscriptionByID(sub.ID); got.State != "expired" || got.ClosedAt == nil {
		t.Errorf("subscription after approval = %+v, want expired and closed", got)
	}
	if n := b.book(bookPlanLiability); n != 0 {
		t.Errorf("liability after approval = %d, want 0", n)
	}
	if n := b.book(bookCash); n != 0 {
		t.Errorf("cash after approval = %d, want 0", n)
	}
	again, err := b.approve(proposalID, approver, "checked")
	if err != nil || again != out {
		t.Errorf("approval replay = %+v, %v; want %+v", again, err, out)
	}
	if n := b.book(bookCash); n != 0 {
		t.Errorf("cash after the replay = %d, want 0", n)
	}
	b.balanced()
}

func (b *bank) approve(id, approver uuid.UUID, reason string) (SubscriptionRefund, error) {
	b.t.Helper()
	var out SubscriptionRefund
	err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		var err error
		out, err = ApproveSubscriptionRefund(b.t.Context(), tx, id, approver, audit.System("billing.test"), reason)
		return err
	})
	return out, err
}

// TestSubscriptionRefundReadsAGrantWrittenBeforeFirstWindow: a node of the previous release inserts a grant without
// first_window during the rolling release. Refunding it derives the block's start from the earlier grants.
func TestSubscriptionRefundReadsAGrantWrittenBeforeFirstWindow(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	p := b.makePlan(1_000_000_000, 3600)
	first := uuid.NewV7()
	b.subscribePaid(p, first, 100)
	second := uuid.NewV7()
	b.subscribePaid(p, second, 100)
	b.sql("UPDATE billing.subscription_grants SET first_window = NULL WHERE id = '" + second.String() + "'")
	if _, err := b.refund(SubscriptionRefundRequest{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Grant: second, SiteCurrency: usd,
		Credits: 1_000_000_000, Reason: "old node's grant", Actor: audit.System("billing.test"),
	}); err != nil {
		t.Fatal(err)
	}
	if n := b.book(bookPlanLiability); n != -1_000_000_000 {
		t.Errorf("plan liability after refunding the old grant = %d, want the first grant still -1", n)
	}
	b.balanced()
}
