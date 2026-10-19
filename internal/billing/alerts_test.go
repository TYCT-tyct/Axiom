package billing

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/telemetry"
)

// logBuffer is a log the tests read, written by the goroutines that log.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logBuffer) logger() *slog.Logger { return slog.New(slog.NewTextHandler(l, nil)) }

// scrape is the value of a metric in the process's metrics text, or what the text was when it is absent.
func scrape(t *testing.T, tel *telemetry.Telemetry, name string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	tel.MetricsHandler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	for line := range strings.Lines(rec.Body.String()) {
		if value, ok := strings.CutPrefix(line, name+" "); ok {
			return strings.TrimSpace(value)
		}
	}
	return "absent from:\n" + rec.Body.String()
}

// TestThePlatformBearingASettlementIsReported: a settlement the floor kept the account from paying in full (the case of
// TestSettleFloor) is logged at error with the request, the price, what was charged and what the platform bore, and
// counted with the amount; a settlement the account paid in full is neither.
func TestThePlatformBearingASettlementIsReported(t *testing.T) {
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	logs := &logBuffer{}
	b := newBank(t, 2000, -100, Config{
		HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, Logger: logs.logger(), Meter: tel.Metrics.Meter("axiom/billing"),
	})
	other, h := b.hold(500), b.hold(1000)
	if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 5000, 0, 0))); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"level=ERROR", "the platform bears", "request=" + h.id.String(), "price=5000", "charged=1500", "borne=3500"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logs.String())
		}
	}
	if got := scrape(t, tel, "axiom_billing_platform_borne_total"); got != "1" {
		t.Errorf("the scrape counts %s settlements borne, want 1", got)
	}
	if got := scrape(t, tel, "axiom_billing_platform_borne_amount_total"); got != "3500" {
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
	logs := &logBuffer{}
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond, Logger: logs.logger()})
	holds := []*Hold{b.hold(1000), b.hold(1000)}
	slices.SortFunc(holds, func(a, c *Hold) int { return bytes.Compare(a.id[:], c.id[:]) })
	failing, healthy := holds[0], holds[1] // cleanup takes them in request order
	b.supplies.failLost = map[uuid.UUID]error{failing.id: errors.New("the supplier's side is broken")}
	time.Sleep(5 * time.Millisecond) // past the reservations' deadline
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
	b.billing.sweep(t.Context())
	if !strings.Contains(logs.String(), "the cleanup failed") || !strings.Contains(logs.String(), failing.id.String()) {
		t.Errorf("the sweep did not report the failing cleanup:\n%s", logs.String())
	}
}

// TestCleanupReachesPastABacklogOfFailures: more always-failing reservations than one batch must not hold back a
// healthy one behind them in request order; the walk goes on past the batch by a keyset cursor, so one call takes it over.
func TestCleanupReachesPastABacklogOfFailures(t *testing.T) {
	b := newBank(t, 20_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond})
	const batch = 3
	holds := make([]*Hold, batch+1)
	for i := range holds {
		holds[i] = b.hold(1000)
	}
	slices.SortFunc(holds, func(a, c *Hold) int { return bytes.Compare(a.id[:], c.id[:]) })
	healthy := holds[batch] // last in request order, behind a whole batch of failures
	b.supplies.failLost = map[uuid.UUID]error{}
	for _, h := range holds[:batch] {
		b.supplies.failLost[h.id] = errors.New("the supplier's side is broken")
	}
	time.Sleep(5 * time.Millisecond) // past the reservations' deadline
	n, err := b.billing.Cleanup(t.Context(), batch)
	if n != 1 || err == nil {
		t.Fatalf("Cleanup = %d, %v; want the healthy one cleaned and the failures reported", n, err)
	}
	if !b.supplies.lostNow(healthy.id) {
		t.Error("the healthy reservation behind a batch of failures was not taken over")
	}
}

// TestCleanupReportsWhatItReleases: a reservation cleanup gives back without a settlement is logged with its amount,
// because any usage of the call is the platform's.
func TestCleanupReportsWhatItReleases(t *testing.T) {
	logs := &logBuffer{}
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond, Logger: logs.logger()})
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
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	logs := &logBuffer{}
	b := newBank(t, 10_000, 0, Config{})
	r := &reconciler{
		db: b.app, scope: b.platform, logger: logs.logger(),
		problems: counter(tel.Metrics.Meter("axiom/billing"), "axiom.billing.reconcile.problems", "problems"),
	}
	if err := r.work(t.Context(), nil); err != nil || logs.String() != "" {
		t.Fatalf("a balanced ledger: %v, log %q", err, logs.String())
	}
	id := b.unbalance()
	if err := r.work(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"level=ERROR", "does not reconcile", "transaction " + id.String()} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logs.String())
		}
	}
	if got := scrape(t, tel, "axiom_billing_reconcile_problems_total"); got != "1" {
		t.Errorf("the scrape counts %s problems, want 1", got)
	}
}

// TestTheReconciliationRunsOnTheQueue: registered on a queue, it runs when the queue starts, without anyone asking, and
// reports from the worker the queue gives it.
func TestTheReconciliationRunsOnTheQueue(t *testing.T) {
	logs := &logBuffer{}
	b := newBank(t, 10_000, 0, Config{})
	id := b.unbalance()
	q := b.app.NewQueue(slog.New(slog.DiscardHandler), b.platform, nil)
	if err := RegisterReconcile(ReconcileConfig{DB: b.app, Scope: b.platform, Queue: q, Logger: logs.logger()}); err != nil {
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
