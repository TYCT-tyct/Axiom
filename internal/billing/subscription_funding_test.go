package billing

import (
	"log/slog"
	"math"
	"testing"
	"uuid"

	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// accountAtSite opens another account of the bank's site, with a zero balance, for tests that need two subscriptions
// under one plan catalog.
func (b *bank) accountAtSite() Admitted {
	b.t.Helper()
	account := uuid.NewV7()
	a := Admitted{
		Scope: pg.NewScope(false, nil, []uuid.UUID{account}), Site: b.admitted.Site, Account: account,
		Workspace: uuid.NewV7(), Key: uuid.NewV7(),
	}
	if err := b.app.Tx(b.t.Context(), a.Scope, func(tx pg.Tx) error {
		return OpenBalance(b.t.Context(), tx, a.Site, a.Account, 0)
	}); err != nil {
		b.t.Fatal(err)
	}
	return a
}

// text reads one string column of one row, in the platform scope.
func (b *bank) text(query string) string {
	b.t.Helper()
	var s string
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		return tx.QueryRow(b.t.Context(), query).Scan(&s)
	}); err != nil {
		b.t.Fatal(err)
	}
	return s
}

// subscriptionByID reads a subscription of any account.
func (b *bank) subscriptionByID(id uuid.UUID) Subscription {
	b.t.Helper()
	var sub Subscription
	if err := b.app.ReadTx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		sub, err = subscriptionOf(b.t.Context(), tx, id)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return sub
}

// expireSubscription moves a subscription and its windows into the past, for the expiry tests.
func (b *bank) expireSubscription(sub Subscription, starts, ends string) {
	b.t.Helper()
	b.sql("UPDATE billing.subscriptions SET starts_at = "+starts+", ends_at = "+ends+" WHERE id = '"+sub.ID.String()+"'",
		"UPDATE billing.subscription_windows SET starts_at = "+starts+", ends_at = "+ends+" WHERE subscription_id = '"+sub.ID.String()+"'")
}

// dueSubscriptions reads one page of the job's due set.
func (b *bank) dueSubscriptions(max int32) []DueSubscriptionsRow {
	b.t.Helper()
	var due []DueSubscriptionsRow
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		due, err = queries.DueSubscriptions(b.t.Context(), tx, max)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return due
}

// runSubscriptionJob runs one whole job round.
func (b *bank) runSubscriptionJob() {
	b.t.Helper()
	cfg := SubscriptionConfig{DB: b.app, Scope: b.platform, Logger: slog.New(slog.DiscardHandler)}
	failures := counter(noop.NewMeterProvider().Meter("billing.test"), "billing.test.subscription.failures", "test")
	if err := runSubscriptions(b.t.Context(), cfg, failures); err != nil {
		b.t.Fatal(err)
	}
}

// expectClosed fails when id's closed state is not want.
func (b *bank) expectClosed(id uuid.UUID, want bool, when string) {
	b.t.Helper()
	if got := b.subscriptionByID(id).ClosedAt != nil; got != want {
		b.t.Errorf("%s: closed = %v, want %v", when, got, want)
	}
}

// TestSubscriptionWriteOffCountsChargedCalls: the liability a write-off lapses includes calls settled from the
// subscription's windows, not only the grant and expiry events keyed by the subscription's own ID. A 300 grant that
// charged 100 must write off 200, leaving the liability book at zero.
func TestSubscriptionWriteOffCountsChargedCalls(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{})
	sub := b.subscribe(b.makePlan(300, 3600), uuid.NewV7())
	h := b.holdOn(b.exact, 100)
	if h.funding != "plan" {
		t.Fatalf("hold funds from %q, want the plan", h.funding)
	}
	if f := b.settleWith(h, outcome(Committed, FinishStop, used(t, 100, 0, 0))); f.Charged != platform.AmountExact(100) {
		t.Fatalf("Settle charged %d, want 100", f.Charged.CeilAmount())
	}
	b.sql("UPDATE billing.subscriptions SET starts_at = now() - interval '2 hours', ends_at = now() - interval '1 hour' WHERE id = '"+sub.ID.String()+"'",
		"UPDATE billing.subscription_windows SET starts_at = now() - interval '2 hours', ends_at = now() - interval '1 hour' "+
			"WHERE subscription_id = '"+sub.ID.String()+"'")
	if w := b.advance(); w != nil {
		t.Fatalf("a window of an ended term funded a request: %+v", w)
	}
	b.writeOff()
	if n := b.book(bookPlanLiability); n != 0 {
		t.Errorf("plan liability %d after the write-off, want 0", n)
	}
	if n := b.book(bookCreditExpired); n != -200 {
		t.Errorf("expired credit %d, want -200: the 300 granted less the 100 charge", n)
	}
	if w := b.window(1); w.Used != 100 {
		t.Errorf("window 1 used %d, want the 100 charge kept", w.Used)
	}
	if closed := b.subscribed().ClosedAt; closed == nil {
		t.Error("the subscription did not close")
	}
	b.balanced()
}

// TestSubscriptionJobRevisitsExpiredUnclosed: a run that expires a subscription with a pending plan hold cannot close
// it, and later scans must find it again once the hold ends. An expired one still waiting for a hold must not take the
// page from another that can close.
func TestSubscriptionJobRevisitsExpiredUnclosed(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{})
	plan := b.makePlan(300, 3600)
	first := b.subscribe(plan, uuid.NewV7())
	held := b.holdOn(b.exact, 100)
	secondAccount := b.accountAtSite()
	second := b.subscribeFor(secondAccount, plan, uuid.NewV7())
	b.expireSubscription(first, "now() - interval '3 hours'", "now() - interval '2 hours'")
	b.expireSubscription(second, "now() - interval '2 hours'", "now() - interval '1 hour'")
	if w := b.advance(); w != nil {
		t.Fatalf("the first subscription's ended term funded a request: %+v", w)
	}
	if w := b.advanceFor(secondAccount); w != nil {
		t.Fatalf("the second subscription's ended term funded a request: %+v", w)
	}

	// The first is expired and waiting for its hold; the second is expired and closeable. One-row page, first ordered
	// by end: the waiting one must not occupy it.
	if due := b.dueSubscriptions(1); len(due) != 1 || due[0].ID != second.ID {
		t.Fatalf("the due page is %+v, want the closeable second subscription %s", due, second.ID)
	}

	b.runSubscriptionJob()
	b.expectClosed(second.ID, true, "the first run")
	b.expectClosed(first.ID, false, "the first run")
	b.runSubscriptionJob()
	b.expectClosed(first.ID, false, "the second run, while its hold is pending")

	if _, err := b.billing.Settle(t.Context(), held, outcome(NotExecuted, FinishNone)); err != nil {
		t.Fatal(err)
	}
	b.runSubscriptionJob()
	b.expectClosed(first.ID, true, "the run after the last hold ended")
	if n := b.book(bookPlanLiability); n != 0 {
		t.Errorf("plan liability %d after both write-offs, want 0", n)
	}
	b.balanced()
}

// TestSubscriptionPlanTermExtremes: the window count ceiling never overflows, and a term or window too large for a
// time.Duration is refused when the plan is saved instead of wrapping to a fraction of a second.
func TestSubscriptionPlanTermExtremes(t *testing.T) {
	t.Parallel()

	if n, err := planWindows(math.MaxInt64, math.MaxInt64); err != nil || n != 1 {
		t.Errorf("planWindows(MaxInt64, MaxInt64) = %d, %v; want one window", n, err)
	}
	if n, err := planWindows(18_446_744_074, 18_446_744_074); err != nil || n != 1 {
		t.Errorf("planWindows(18446744074, 18446744074) = %d, %v; want one window", n, err)
	}
	if _, err := planWindows(math.MaxInt64, 3600); err == nil {
		t.Error("planWindows(MaxInt64, 3600) accepted more windows than a term may have")
	}
	if _, err := faceValue(math.MaxInt64, math.MaxInt64, math.MaxInt64); err != nil {
		t.Errorf("faceValue at the maximum term = %v, want a value rather than a division by zero", err)
	}
	for _, tc := range []struct {
		name         string
		window, term int64
		wantErr      bool
	}{
		{"the largest bigint term and window", math.MaxInt64, math.MaxInt64, true},
		{"a term past the Duration range", 18_446_744_074, 18_446_744_074, true},
		{"the longest convertible term", maxTermSeconds, maxTermSeconds, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := PlanInput{Name: "plan", Currency: usd, Quota: 1, WindowSeconds: tc.window, TermSeconds: tc.term}.check()
			if (err != nil) != tc.wantErr {
				t.Errorf("check(%d, %d) = %v, want error %v", tc.window, tc.term, err, tc.wantErr)
			}
		})
	}
}
