package billing

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/telemetry"
	"axiom/internal/platform/telemetry/telemetrytest"
)

// TestThePlatformBearingASettlementIsReported: a settlement the floor kept the account from paying in full (the case of
// TestSettleFloor) is logged at error with the request, the price, what was charged and what the platform bore, and
// counted with the amount; a settlement the account paid in full is neither.
func TestThePlatformBearingASettlementIsReported(t *testing.T) {
	t.Parallel()

	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	logs := &telemetrytest.Buffer{}
	var signals settlementSignals
	b := newBank(t, 2000, -100, Config{
		Alerts:    &signals,
		HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, Logger: logs.Logger(), Meter: tel.Metrics.Meter("axiom/billing"),
	})
	other, h := b.hold(500), b.hold(1000)
	if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 5000, 0, 0))); err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 || signals[0].Key != "platform_borne" || signals[0].Fields["borne"] != "3500" {
		t.Fatalf("floor-loss signal: %+v", signals)
	}
	for _, want := range []string{"level=ERROR", "the platform bears", "request=" + h.id.String(), "price=5000", "charged=1500", "borne=3500"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logs.String())
		}
	}
	if got := telemetrytest.Metric(t, tel, "axiom_billing_platform_borne_total"); got != "1" {
		t.Errorf("the scrape counts %s settlements borne, want 1", got)
	}
	if got := telemetrytest.Metric(t, tel, "axiom_billing_platform_borne_amount_total"); got != "3500" {
		t.Errorf("the scrape counts %s borne, want 3500", got)
	}
	if _, err := b.billing.Settle(t.Context(), other, outcome(Committed, FinishStop, used(t, 500, 100, 0))); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logs.String(), "the platform bears"); n != 1 {
		t.Errorf("%d reports after a second settlement paid in full, want only the first", n)
	}
}

// TestCleanupGoesPastAReservationThatFails: a reservation whose cleanup fails every time is first in line every sweep,
// so the ones after it must still be taken over, the failure reported with the request; a sweep that can clean nothing
// more ends rather than trying the same again.
func TestCleanupGoesPastAReservationThatFails(t *testing.T) {
	t.Parallel()

	logs := &telemetrytest.Buffer{}
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond, Logger: logs.Logger()})
	holds := []*Hold{b.hold(1000), b.hold(1000)}
	slices.SortFunc(holds, func(a, c *Hold) int { return bytes.Compare(a.id[:], c.id[:]) })
	failing, healthy := holds[0], holds[1] // cleanup takes them in request order
	b.supplies.failLost = map[uuid.UUID]error{failing.id: errors.New("the supplier's side is broken")}
	b.pastDeadline()
	n, err := b.billing.Cleanup(t.Context(), 100)
	if n != 1 || err == nil || !strings.Contains(err.Error(), failing.id.String()) {
		t.Fatalf("Cleanup = %d, %v; want one cleaned and the error naming the failing request", n, err)
	}
	if !b.supplies.lostNow(healthy.id) || b.supplies.lostNow(failing.id) {
		t.Errorf("lost: healthy %t, failing %t; want the one after the failure taken over and the failing one left",
			b.supplies.lostNow(healthy.id), b.supplies.lostNow(failing.id))
	}
	if n := b.count("SELECT count(*) FROM billing.reservations WHERE state = 'pending'"); n != 1 {
		t.Errorf("%d reservations pending, want only the failing one", n)
	}
	b.billing.sweep(t.Context(), uuid.UUID{})
	if !strings.Contains(logs.String(), "the cleanup failed") || !strings.Contains(logs.String(), failing.id.String()) {
		t.Errorf("the sweep did not report the failing cleanup:\n%s", logs.String())
	}
}

// pastDeadline puts every reservation's deadline in the past, so cleanup owns them without the test waiting for a clock.
func (b *bank) pastDeadline() {
	b.t.Helper()
	b.sql("UPDATE billing.reservations SET deadline = clock_timestamp() - interval '1 hour'")
}

// failingBacklog makes n reservations whose cleanup always fails and one healthy one last in request order, already due.
func failingBacklog(b *bank, n int) (healthy *Hold) {
	holds := make([]*Hold, n+1)
	for i := range holds {
		holds[i] = b.hold(1000)
	}
	slices.SortFunc(holds, func(a, c *Hold) int { return bytes.Compare(a.id[:], c.id[:]) })
	b.supplies.failLost = map[uuid.UUID]error{}
	for _, h := range holds[:n] {
		b.supplies.failLost[h.id] = errors.New("the supplier's side is broken")
	}
	b.pastDeadline()
	return holds[n]
}

// TestCleanupReachesPastABacklogOfFailures: a call tries at most its budget, failures included, and carries a cursor
// past them, so a healthy reservation behind a backlog larger than the budget is reached within ceil(rank/budget)
// calls (here 4 failing + 1 healthy, budget 2: the third call), and a call after the scan's end starts over.
func TestCleanupReachesPastABacklogOfFailures(t *testing.T) {
	t.Parallel()

	b := newBank(t, 20_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour})
	healthy := failingBacklog(b, 4)
	var after uuid.UUID
	for call, wantDone := range []bool{false, false, true} {
		n, next, done, err := b.billing.cleanFrom(t.Context(), after, 2)
		if done != wantDone || (call < 2) != (n == 0) {
			t.Fatalf("call %d: changed %d, done %t; want done %t and the healthy one changed only on the last", call+1, n, done, wantDone)
		}
		if call < 2 {
			if got := strings.Count(err.Error(), "billing: clean up"); got != 2 {
				t.Errorf("call %d reported %d failures, want its budget of 2 attempts", call+1, got)
			}
		}
		after = next
	}
	if !b.supplies.lostNow(healthy.id) {
		t.Error("the healthy reservation behind the backlog was not taken over")
	}
	if after != (uuid.UUID{}) {
		t.Errorf("the cursor after the scan's end is %s, want the start", after)
	}
}

// TestCleanupCapsItsFailureReport: the error of a call keeps the first failures and counts the rest.
func TestCleanupCapsItsFailureReport(t *testing.T) {
	t.Parallel()

	b := newBank(t, 20_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour})
	failingBacklog(b, reportedFailures+2)
	_, err := b.billing.Cleanup(t.Context(), reportedFailures+2)
	if got := strings.Count(err.Error(), "billing: clean up"); got != reportedFailures || !strings.Contains(err.Error(), "2 more failures not shown") {
		t.Errorf("%d failures named in %v; want %d and a count of the other 2", got, err, reportedFailures)
	}
}

// TestCleanupSaysWhenItWasCut: a call whose ctx ended returns the cause, wrapped, and the count so far, whether the ctx
// ended before the call or while a reservation was being taken over.
func TestCleanupSaysWhenItWasCut(t *testing.T) {
	t.Parallel()

	b := newBank(t, 20_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour})
	b.hold(1000)
	b.hold(1000)
	b.pastDeadline()
	before, cancel := context.WithCancel(t.Context())
	cancel()
	if n, _, done, err := b.billing.cleanFrom(before, uuid.UUID{}, 10); n != 0 || done || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled before: %d changed, done %t, %v; want 0, not done, context.Canceled", n, done, err)
	}
	during, cancel := context.WithCancel(t.Context())
	defer cancel()
	b.supplies.stuck = make(chan struct{}) // recording a lost request waits here until the ctx ends
	time.AfterFunc(50*time.Millisecond, cancel)
	if n, _, done, err := b.billing.cleanFrom(during, uuid.UUID{}, 10); n != 0 || done || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled during: %d changed, done %t, %v; want 0, not done, context.Canceled", n, done, err)
	}
}

// TestCleanupReportsWhatItReleases: a reservation cleanup gives back without a settlement is logged with its amount,
// because any usage of the call is the platform's.
func TestCleanupReportsWhatItReleases(t *testing.T) {
	t.Parallel()

	logs := &telemetrytest.Buffer{}
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond, Logger: logs.Logger()})
	h := b.hold(1000)
	b.takenOver(h)
	for _, want := range []string{"level=WARN", "cleanup released", "request=" + h.id.String(), "released=1100"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logs.String())
		}
	}
}

// TestALateSettlementWithoutAttemptsAnswersTheLostRequest: cleanup took over a call that then settled with no attempt,
// as a call whose client went before any upstream was called does: it never reached one, so nothing can be charged to it
// and the request is no longer lost, though the reservation stays as cleanup left it.
func TestALateSettlementWithoutAttemptsAnswersTheLostRequest(t *testing.T) {
	t.Parallel()

	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond})
	h := b.hold(1000)
	b.takenOver(h)
	for range 2 { // a retry changes nothing
		if f, err := b.billing.Settle(t.Context(), h, Outcome{}); err != nil || f.Charged.Cmp(platform.Exact{}) != 0 {
			t.Fatalf("Settle = %+v, %v", f, err)
		}
	}
	b.wantReservation(h, "cleanup_released", 0, false)
	b.balanced()
}

// unbalance writes a ledger transaction whose entries do not sum to zero, as no code of billing can, and returns its ID.
func (b *bank) unbalance() uuid.UUID {
	b.t.Helper()
	id, a := uuid.NewV7(), b.admitted
	b.sql(
		"INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id) VALUES ('"+id.String()+"', '"+a.Site.String()+"', '"+a.Account.String()+"', 'credit', '"+uuid.NewV7().String()+"')",
		"INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount) VALUES ('"+id.String()+"', '"+a.Site.String()+"', '"+a.Account.String()+"', 'gift_expense', 5)",
	)
	return id
}

// TestTheReconciliationReportsWhatItRefuses: a ledger that balances is silent and counts nothing; a transaction whose
// entries do not balance is an error in the log naming it, and counted.
func TestTheReconciliationReportsWhatItRefuses(t *testing.T) {
	t.Parallel()

	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	logs := &telemetrytest.Buffer{}
	b := newBank(t, 10_000, 0, Config{})
	r := &reconciler{
		db: b.app, scope: b.platform, logger: logs.Logger(),
		problems: counter(tel.Metrics.Meter("axiom/billing"), "axiom.billing.reconcile.problems", "problems"),
	}
	if err := r.work(t.Context(), nil); err != nil || logs.String() != "" {
		t.Fatalf("a balanced ledger: %v, log %q", err, logs.String())
	}
	assertLedgerSignal(t, b, true)
	id := b.unbalance()
	if err := r.work(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	assertLedgerSignal(t, b, false)
	for _, want := range []string{"level=ERROR", "does not reconcile", "transaction " + id.String()} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logs.String())
		}
	}
	if got := telemetrytest.Metric(t, tel, "axiom_billing_reconcile_problems_total"); got != "1" {
		t.Errorf("the scrape counts %s problems, want 1", got)
	}
}

// TestTheReconciliationRunsOnTheQueue: registered on a queue, it runs when the queue starts, without anyone asking, and
// reports from the worker the queue gives it.
func TestTheReconciliationRunsOnTheQueue(t *testing.T) {
	t.Parallel()

	logs := &telemetrytest.Buffer{}
	b := newBank(t, 10_000, 0, Config{})
	id := b.unbalance()
	q := b.app.NewQueue(slog.New(slog.DiscardHandler), b.platform, nil)
	if err := RegisterReconcile(ReconcileConfig{DB: b.app, Scope: b.platform, Queue: q, Logger: logs.Logger()}); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := q.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(logs.String(), "transaction "+id.String()) {
		if time.Now().After(deadline) {
			t.Fatalf("the queue never ran the reconciliation; the log:\n%s", logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func assertLedgerSignal(t *testing.T, b *bank, resolved bool) {
	t.Helper()
	signals := takeBillingSignals(t, b)
	if len(signals) != 1 || signals[0].Resolved != resolved || signals[0].Kind != "billing.reconciliation_difference" {
		t.Fatalf("ledger signal: %+v", signals)
	}
}
