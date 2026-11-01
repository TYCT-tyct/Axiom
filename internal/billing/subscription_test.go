package billing

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// The subscription suite: a plan is the first source a call draws on, the wallet the second, and a reservation keeps
// the window it froze (docs/domains/billing.md「订阅套餐」).

// makePlan adds the site's plan: quota of credit in each window, one window every windowSeconds, termSeconds in all.
func (b *bank) makePlan(quota, window, term int64) Plan {
	b.t.Helper()
	var p Plan
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		var err error
		p, err = CreatePlan(b.t.Context(), tx, b.admitted.Site, PlanInput{Name: "Monthly " + uuid.NewV7().String(), Reason: "test",
			Currency: usd, Price: 2_000_000_000, Quota: quota, WindowSeconds: window, TermSeconds: term, Active: true},
			audit.System("billing.test"))
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return p
}

// subscribe grants p to the account under id, or renews the plan in force, and returns the subscription it made.
func (b *bank) subscribe(p Plan, id uuid.UUID) Subscription {
	b.t.Helper()
	var sub Subscription
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		if err := CreditSubscription(b.t.Context(), tx, SubscriptionGrant{ID: id, Site: b.admitted.Site,
			Account: b.admitted.Account, Plan: p.ID, SiteCurrency: usd, Source: "gift", Actor: audit.System("billing.test"),
			Reason: "test"}); err != nil {
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

// advance passes the windows whose time came, as admission and the job do, and returns the window that holds now.
func (b *bank) advance() *planWindow {
	b.t.Helper()
	var w *planWindow
	if err := b.app.Tx(b.t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		if _, err := lockAccount(b.t.Context(), tx, b.admitted.Account); err != nil {
			return err
		}
		var err error
		w, err = planWindowNow(b.t.Context(), tx, b.admitted.Account)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return w
}

// window reads one window of the account's subscription.
func (b *bank) window(seq int32) Window {
	b.t.Helper()
	var w Window
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		r, err := queries.Window(b.t.Context(), tx, WindowParams{SubscriptionID: b.subscription(tx).ID, Seq: seq})
		w = Window{Seq: r.Seq, StartsAt: r.StartsAt, EndsAt: r.EndsAt, Quota: r.Quota, Used: r.Used, Reserved: r.Reserved}
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return w
}

// subscription reads the account's subscription as it stands.
func (b *bank) subscription(tx pg.Tx) Subscription {
	b.t.Helper()
	subs, err := Subscriptions(b.t.Context(), tx, b.admitted.Account, 1)
	if err != nil || len(subs) != 1 {
		b.t.Fatalf("Subscriptions = %d, %v; want one", len(subs), err)
	}
	return subs[0]
}

// book is the sum of one book's entries, in billionths of the site's currency.
func (b *bank) book(name string) int64 {
	return b.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = '" + name + "'")
}

// TestSubscriptionFundsACallFirst: when the window covers the request the plan pays, the wallet's reserved stays zero,
// and the charge lands on the window and on the plan's liability, not on the balance.
func TestSubscriptionFundsACallFirst(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1000, 0, Config{})
	sub := b.subscribe(b.makePlan(400, 3600, 7200), uuid.NewV7())
	h := b.holdOn(b.exact, 400)
	if h.funding != "plan" || h.subscription != sub.ID || h.windowSeq != 1 {
		t.Fatalf("hold funds from %q of %s window %d, want the plan's first window", h.funding, h.subscription, h.windowSeq)
	}
	if n := b.count("SELECT reserved FROM billing.balances"); n != 0 {
		t.Errorf("the balance reserves %d of a call the plan pays for, want 0", n)
	}
	fee, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 300, 0, 0)))
	if err != nil || fee.Charged != platform.AmountExact(300) {
		t.Fatalf("Settle = %+v, %v; want 300 charged", fee, err)
	}
	if w := b.window(1); w.Used != 300 || w.Reserved != 0 || w.Remaining() != 100 {
		t.Errorf("window 1 is %+v, want 300 used and 100 left", w)
	}
	if n := b.count("SELECT balance FROM billing.balances"); n != 1000 {
		t.Errorf("balance %d after a call the plan paid for, want 1000", n)
	}
	if n := b.book(bookPlanLiability); n != -500 {
		t.Errorf("plan liability %d after a 400-credit plan and a 300 charge, want -500", n)
	}
	b.balanced()
}

// TestSubscriptionWindowExhaustedFallsBackToTheWallet: a request the window cannot cover is the wallet's, whole; the
// plan is not charged part of it (docs/domains/billing.md「资金来源」: one source a request).
func TestSubscriptionWindowExhaustedFallsBackToTheWallet(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1000, 0, Config{})
	b.subscribe(b.makePlan(300, 3600, 3600), uuid.NewV7())
	h := b.holdOn(b.exact, 400)
	if h.funding != "wallet" {
		t.Fatalf("hold funds from %q, want the wallet: the window holds 300 of 400", h.funding)
	}
	if n := b.count("SELECT reserved FROM billing.balances"); n != 400 {
		t.Errorf("the balance reserves %d, want the whole 400", n)
	}
	if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 400, 0, 0))); err != nil {
		t.Fatal(err)
	}
	if n := b.count("SELECT balance FROM billing.balances"); n != 600 {
		t.Errorf("balance %d after the call, want 600", n)
	}
	if w := b.window(1); w.Used != 0 || w.Reserved != 0 {
		t.Errorf("window 1 is %+v after a call the wallet paid for, want nothing used", w)
	}
	b.balanced()
}

// TestSubscriptionExtendStaysInTheFrozenWindow: an extension draws on the window the reservation froze and is truncated
// when that window is short, even though the balance could pay (docs/domains/billing.md「订阅套餐」).
func TestSubscriptionExtendStaysInTheFrozenWindow(t *testing.T) {
	t.Parallel()

	b := newBank(t, 10_000, 0, Config{})
	b.subscribe(b.makePlan(300, 3600, 7200), uuid.NewV7())
	h := b.holdOn(b.exact, 250)
	err := b.billing.Extend(t.Context(), h, ExtendRequest{Seq: 2, Target: platform.AmountExact(350)})
	var short *Insufficient
	if !errors.As(err, &short) || short.Source != SourceBalance {
		t.Fatalf("Extend by 100 over a window holding 50 = %v, want Insufficient on the balance", err)
	}
	if w := b.window(1); w.Reserved != 250 {
		t.Errorf("window 1 reserves %d after a refused extension, want 250", w.Reserved)
	}
	if err := b.billing.Extend(t.Context(), h, ExtendRequest{Seq: 3, Target: platform.AmountExact(290)}); err != nil {
		t.Fatalf("Extend by 40 with 50 left: %v", err)
	}
	if w := b.window(1); w.Reserved != 290 || w.Remaining() != 10 {
		t.Errorf("window 1 is %+v after the extension, want 290 reserved and 10 left", w)
	}
	b.consistent()
}

// TestSubscriptionResetDoesNotCarryOver: passing into the next window gives it the plan's quota again and keeps what the
// last one used; nothing is written to the books for a reset.
func TestSubscriptionResetDoesNotCarryOver(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1000, 0, Config{})
	sub := b.subscribe(b.makePlan(300, 3600, 7200), uuid.NewV7())
	h := b.holdOn(b.exact, 100)
	if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 100, 0, 0))); err != nil {
		t.Fatal(err)
	}
	books := b.book(bookPlanLiability)
	b.sql("UPDATE billing.subscription_windows SET starts_at = now() - interval '2 hours', " +
		"ends_at = now() - interval '1 hour' WHERE subscription_id = '" + sub.ID.String() + "' AND seq = 1")
	w := b.advance()
	if w == nil || w.seq != 2 || w.remaining() != 300 || w.used != 0 {
		t.Fatalf("the window after the reset is %+v, want window 2 with its 300 unspent", w)
	}
	if n := b.book(bookPlanLiability); n != books {
		t.Errorf("liability moved from %d to %d on a reset, want no entry", books, n)
	}
	b.consistent()
}

// TestSubscriptionSettleLandsInTheFrozenWindow: a reservation made in one window and settled after the next began
// charges the window it froze, never the current one (cross-window settlement).
func TestSubscriptionSettleLandsInTheFrozenWindow(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1000, 0, Config{})
	sub := b.subscribe(b.makePlan(300, 3600, 7200), uuid.NewV7())
	h := b.holdOn(b.exact, 200)
	b.sql("UPDATE billing.subscription_windows SET starts_at = now() - interval '2 hours', " +
		"ends_at = now() - interval '1 hour' WHERE subscription_id = '" + sub.ID.String() + "' AND seq = 1")
	if w := b.advance(); w == nil || w.seq != 2 {
		t.Fatalf("the window after the pass is %+v, want window 2", w)
	}
	if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 150, 0, 0))); err != nil {
		t.Fatal(err)
	}
	if w := b.window(1); w.Used != 150 || w.Reserved != 0 {
		t.Errorf("window 1 is %+v after the late settlement, want 150 used", w)
	}
	if w := b.window(2); w.Used != 0 || w.Reserved != 0 {
		t.Errorf("window 2 is %+v, want the charge in window 1 only", w)
	}
	if n := b.book(bookPlanLiability); n != -450 {
		t.Errorf("plan liability %d, want -450: two windows of 300 less the 150 charged", n)
	}
	b.balanced()
}

// TestSubscriptionRenewalAddsTermAndWindows: renewing before the end extends from the end, adds a term of windows, and
// credits the plan's liability with their face value.
func TestSubscriptionRenewalAddsTermAndWindows(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{})
	p := b.makePlan(100, 3600, 7200)
	first := b.subscribe(p, uuid.NewV7())
	renewed := b.subscribe(p, uuid.NewV7())
	if renewed.ID != first.ID {
		t.Fatalf("the renewal made subscription %s, want the one in force %s", renewed.ID, first.ID)
	}
	if want := first.EndsAt.Add(7200 * time.Second); !renewed.EndsAt.Equal(want) {
		t.Errorf("the renewal ends at %s, want %s: one term after the end", renewed.EndsAt, want)
	}
	if n := b.count("SELECT count(*) FROM billing.subscription_windows"); n != 4 {
		t.Errorf("%d windows after the renewal, want 4", n)
	}
	if n := b.book(bookPlanLiability); n != -400 {
		t.Errorf("plan liability %d after two terms of 200, want -400", n)
	}
	b.balanced()
}

// TestSubscriptionGrantIsIdempotent: the grant's ID is its idempotency key; the same key with other terms is a conflict,
// never a second credit.
func TestSubscriptionGrantIsIdempotent(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{})
	p := b.makePlan(100, 3600, 7200)
	id := uuid.NewV7()
	b.subscribe(p, id)
	b.subscribe(p, id)
	if n := b.count("SELECT count(*) FROM billing.subscription_grants"); n != 1 {
		t.Errorf("%d grants after the same key twice, want 1", n)
	}
	if n := b.book(bookPlanLiability); n != -200 {
		t.Errorf("plan liability %d after the same key twice, want -200", n)
	}
	other := b.makePlan(1000, 3600, 7200)
	err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		return CreditSubscription(t.Context(), tx, SubscriptionGrant{ID: id, Site: b.admitted.Site,
			Account: b.admitted.Account, Plan: other.ID, SiteCurrency: usd, Source: "gift", Reason: "test"})
	})
	if !errors.Is(err, ErrCreditConflict) {
		t.Fatalf("the same key with another plan = %v, want ErrCreditConflict", err)
	}
	b.balanced()
}

// TestSubscriptionExpiryWaitsForItsHolds: an ended term is expired at once but written off only when no plan hold is
// pending, so a late settlement still has a window to charge; the write-off takes the remaining liability to zero.
func TestSubscriptionExpiryWaitsForItsHolds(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1000, 0, Config{})
	sub := b.subscribe(b.makePlan(300, 3600, 3600), uuid.NewV7())
	h := b.holdOn(b.exact, 200)
	b.sql("UPDATE billing.subscriptions SET starts_at = now() - interval '2 hours', "+
		"ends_at = now() - interval '1 hour' WHERE id = '"+sub.ID.String()+"'",
		"UPDATE billing.subscription_windows SET starts_at = now() - interval '2 hours', "+
			"ends_at = now() - interval '1 hour' WHERE subscription_id = '"+sub.ID.String()+"'")
	if w := b.advance(); w != nil {
		t.Fatalf("a window of an ended term funded a request: %+v", w)
	}
	if state := b.subscribed().State; state != "expired" {
		t.Fatalf("subscription state %q after the term ended, want expired", state)
	}
	b.writeOff()
	if closed := b.subscribed().ClosedAt; closed != nil {
		t.Fatalf("the subscription closed at %s with a plan hold pending", closed)
	}
	if _, err := b.billing.Settle(t.Context(), h, outcome(NotExecuted, FinishNone)); err != nil {
		t.Fatal(err)
	}
	b.writeOff()
	closed := b.subscribed()
	if closed.ClosedAt == nil {
		t.Fatal("the subscription did not close after its last hold ended")
	}
	if n := b.book(bookPlanLiability); n != 0 {
		t.Errorf("plan liability %d after the write-off, want 0", n)
	}
	if n := b.book(bookCreditExpired); n != -300 {
		t.Errorf("expired credit %d, want -300: the window's unspent credit", n)
	}
	b.balanced()
}

// writeOff runs the job's write-off for the account's subscription.
func (b *bank) writeOff() {
	b.t.Helper()
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		return writeOffSubscription(b.t.Context(), tx, b.subscription(tx))
	}); err != nil {
		b.t.Fatal(err)
	}
}

// subscribed reads the account's subscription as it stands.
func (b *bank) subscribed() Subscription {
	b.t.Helper()
	var sub Subscription
	if err := b.app.ReadTx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		sub = b.subscription(tx)
		return nil
	}); err != nil {
		b.t.Fatal(err)
	}
	return sub
}
