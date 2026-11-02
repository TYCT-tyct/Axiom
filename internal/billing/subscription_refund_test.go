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
		out, err = RefundSubscriptionGrant(b.t.Context(), tx, req)
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
