package catalog_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
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

// openrouterDataset is a model list no model of these tests follows: a round needs every dataset to be readable, and only
// makes a result for a model that follows one.
const openrouterDataset = `{"data":[{"id":"acme/other","pricing":{"prompt":"0.000001","completion":"0.000002"}}]}`

// datasets serves the two datasets a round reads, and fails both on demand: an outage, which is what the alert is about.
type datasets struct {
	litellm, openrouter []byte
	failing             atomic.Bool
}

// served is the two datasets a round reads: a LiteLLM table with the given entry, and an OpenRouter list no model of
// these tests follows. A round needs every dataset to be readable and makes a result only for a model that follows one.
func served(litellmEntry string) *datasets {
	return &datasets{litellm: litellm(litellmEntry).Body, openrouter: []byte(openrouterDataset)}
}

// fetcher reads the datasets from this test's servers instead of the registered addresses.
func (d *datasets) fetcher(t *testing.T) *catalog.Fetcher {
	t.Helper()
	serve := func(body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			if d.failing.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if _, err := w.Write(body); err != nil {
				t.Errorf("write: %v", err)
			}
		}
	}
	litellm := httptest.NewServer(serve(d.litellm))
	t.Cleanup(litellm.Close)
	openrouter := httptest.NewServer(serve(d.openrouter))
	t.Cleanup(openrouter.Close)
	return catalog.NewTestFetcher(egress.NewForTesting(egresstest.Loopback(t)), map[catalog.PriceSource]string{
		catalog.SourceLiteLLM: litellm.URL, catalog.SourceOpenRouter: openrouter.URL,
	})
}

// record is the sync's record as one line — the last success, the start of the failure streak, the failures and the
// pending changes, a time in RFC 3339 or "never" — so a test compares all of it at once.
func (s *store) record() string {
	s.t.Helper()
	state := s.syncState()
	stamp := func(at *time.Time) string {
		if at == nil {
			return "never"
		}
		return at.UTC().Format(time.RFC3339)
	}
	return strings.Join([]string{
		stamp(state.LastSuccessAt), stamp(state.FailingSince), strconv.FormatInt(state.Failures, 10), strconv.FormatInt(state.Pending, 10),
	}, "|")
}

// syncState is the record the scheduled sync keeps.
func (s *store) syncState() catalog.GetSyncStateRow {
	s.t.Helper()
	var state catalog.GetSyncStateRow
	if err := s.db.ReadTx(s.t.Context(), platformScope, func(tx pg.Tx) error {
		var err error
		state, err = catalog.New().GetSyncState(s.t.Context(), tx)
		return err
	}); err != nil {
		s.t.Fatal(err)
	}
	return state
}

// startQueue starts the queue and stops it when the test ends.
func startQueue(t *testing.T, q *pg.Queue) {
	t.Helper()
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
}

// TestTheSyncRoundRunsOnTheSyncsQueue: registered on a queue, a round runs when the queue starts, without anyone asking,
// on the syncs queue, and records itself.
func TestTheSyncRoundRunsOnTheSyncsQueue(t *testing.T) {
	logs := &logBuffer{}
	s := newStore(t)
	d := served(`"gpt-x-2025-01-01"=` + xPrices("1e-06", "2e-06", "1e-07"))
	now := time.Date(2025, 11, 1, 3, 0, 0, 0, time.UTC)
	ran := make(chan struct{})
	q := s.db.NewQueue(slog.New(slog.DiscardHandler), platformScope, nil)
	if err := catalog.RegisterSync(catalog.SyncJobConfig{
		DB: s.db, Scope: platformScope, Queue: q, Fetcher: d.fetcher(t), Logger: logs.logger(),
		Clock: func() time.Time { close(ran); return now },
	}); err != nil {
		t.Fatal(err)
	}
	startQueue(t, q)
	select {
	case <-ran:
	case <-time.After(30 * time.Second):
		t.Fatalf("the queue never ran a round; the log:\n%s", logs.String())
	}
	// Stop waits for the signalled round to finish its transaction and acknowledgement.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := q.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if n := query[int](s, platformScope, "SELECT count(*) FROM river.river_job WHERE kind = 'catalog.sync' AND state = 'completed'"); n != 1 {
		t.Fatalf("completed rounds: %d, want 1", n)
	}
	if queue := query[string](s, platformScope, "SELECT queue FROM river.river_job WHERE kind = 'catalog.sync'"); queue != "syncs" {
		t.Errorf("the round ran on queue %q, want syncs", queue)
	}
	if got, want := s.record(), "2025-11-01T03:00:00Z|never|0|0"; got != want {
		t.Errorf("the record after one healthy round is %s, want %s", got, want)
	}
	if strings.Contains(logs.String(), "catalog.sync.overdue") {
		t.Errorf("a healthy round alerted:\n%s", logs.String())
	}
}

// TestTheSyncRecordsEachRound: a round that read every dataset sets the last success, clears the failure streak and counts
// the price changes it left for a person; a failed round starts or extends the streak, keeps the last success and keeps
// the pending count, since it knows only part of what waits.
func TestTheSyncRecordsEachRound(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id := s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
	before := s.usd(id)
	d := served(`"gpt-x-2025-01-01"=` + xPrices("5e-06", "9e-06", "1e-07"))
	now := time.Date(2025, 11, 1, 3, 0, 0, 0, time.UTC)
	cfg := catalog.SyncJobConfig{
		DB: s.db, Scope: platformScope, Fetcher: d.fetcher(t), Logger: slog.New(slog.DiscardHandler),
		Clock: func() time.Time { return now },
	}
	round := func(wantErr bool) {
		t.Helper()
		if err := catalog.RunSyncRound(t.Context(), cfg); (err != nil) != wantErr {
			t.Fatalf("round: %v, want error %t", err, wantErr)
		}
	}
	// A round that changes nothing is a success, and the change above the limit waits for a person.
	round(false)
	if got, want := s.record(), "2025-11-01T03:00:00Z|never|0|1"; got != want {
		t.Fatalf("the record of a healthy round is %s, want %s", got, want)
	}
	if s.usd(id) != before || s.priceCount(id) != 1 {
		t.Error("the round published a change above the price-change limit")
	}
	// A failed round keeps the last success and the pending count, and remembers when the streak began.
	now = now.Add(25 * time.Hour)
	d.failing.Store(true)
	round(true)
	if got, want := s.record(), "2025-11-01T03:00:00Z|2025-11-02T04:00:00Z|1|1"; got != want {
		t.Fatalf("the record after one failed round is %s, want %s", got, want)
	}
	// A second failure extends the streak without moving its start, so the alert measures the outage, not the retry.
	now = now.Add(time.Hour)
	round(true)
	if got, want := s.record(), "2025-11-01T03:00:00Z|2025-11-02T04:00:00Z|2|1"; got != want {
		t.Fatalf("the record after two failed rounds is %s, want %s", got, want)
	}
	// A success clears the streak and moves the last success.
	d.failing.Store(false)
	now = now.Add(time.Hour)
	round(false)
	if got, want := s.record(), "2025-11-02T06:00:00Z|never|0|1"; got != want {
		t.Fatalf("the record after a success is %s, want %s", got, want)
	}
}

// TestTheSyncAlertsWhenThePricesGoUnrefreshed: a day of failure is left to the next round; two days raise the alert once,
// with the log that carries the stable event name and the counter that rises with it; a success ends it.
func TestTheSyncAlertsWhenThePricesGoUnrefreshed(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	logs := &logBuffer{}
	d := served(`"gpt-x-2025-01-01"=` + xPrices("1e-06", "2e-06", "1e-07"))
	now := time.Date(2025, 11, 1, 3, 0, 0, 0, time.UTC)
	cfg := catalog.SyncJobConfig{
		DB: s.db, Scope: platformScope, Fetcher: d.fetcher(t), Logger: logs.logger(),
		Meter: tel.Metrics.Meter("axiom/catalog"), Clock: func() time.Time { return now },
	}
	round := func(wantErr bool) {
		t.Helper()
		if err := catalog.RunSyncRound(t.Context(), cfg); (err != nil) != wantErr {
			t.Fatalf("round: %v, want error %t", err, wantErr)
		}
	}
	round(false)
	now = now.Add(25 * time.Hour)
	d.failing.Store(true)
	round(true)
	if strings.Contains(logs.String(), "catalog.sync.overdue") {
		t.Fatalf("one day of failure alerted:\n%s", logs.String())
	}
	now = now.Add(23 * time.Hour)
	round(true)
	if strings.Contains(logs.String(), "catalog.sync.overdue") {
		t.Fatalf("a round exactly at the threshold alerted:\n%s", logs.String())
	}
	now = now.Add(time.Hour)
	round(true)
	for _, want := range []string{"level=ERROR", "catalog.sync.overdue", "failures=3", "pending=0"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the alert lacks %q:\n%s", want, logs.String())
		}
	}
	if got := scrape(t, tel, "axiom_catalog_sync_overdue_total"); got != "1" {
		t.Errorf("the counter reads %s, want 1 after one overdue round", got)
	}
	d.failing.Store(false)
	now = now.Add(time.Minute)
	round(false)
	if got := scrape(t, tel, "axiom_catalog_sync_overdue_total"); got != "1" {
		t.Errorf("the counter reads %s after a success, want 1", got)
	}
	if got, want := s.record(), "2025-11-03T04:01:00Z|never|0|0"; got != want {
		t.Errorf("the record after the success is %s, want %s", got, want)
	}
}

// TestTheSyncAlertsWhenItNeverSucceeded: a database whose sync has never read the datasets measures the alert from the
// start of the failure streak, and says so.
func TestTheSyncAlertsWhenItNeverSucceeded(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	logs := &logBuffer{}
	d := served(`"gpt-x-2025-01-01"=` + xPrices("1e-06", "2e-06", "1e-07"))
	d.failing.Store(true)
	now := time.Date(2025, 11, 1, 3, 0, 0, 0, time.UTC)
	cfg := catalog.SyncJobConfig{
		DB: s.db, Scope: platformScope, Fetcher: d.fetcher(t), Logger: logs.logger(),
		Clock: func() time.Time { return now },
	}
	round := func() {
		t.Helper()
		if err := catalog.RunSyncRound(t.Context(), cfg); err == nil {
			t.Fatal("the round succeeded, want an error: both datasets are down")
		}
	}
	round()
	if strings.Contains(logs.String(), "catalog.sync.overdue") {
		t.Fatalf("the first failed round alerted:\n%s", logs.String())
	}
	now = now.Add(49 * time.Hour)
	round()
	for _, want := range []string{"catalog.sync.overdue", "never_succeeded=true", "failures=2"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the alert lacks %q:\n%s", want, logs.String())
		}
	}
	if got, want := s.record(), "never|2025-11-01T03:00:00Z|2|0"; got != want {
		t.Errorf("the record after two failed rounds is %s, want %s", got, want)
	}
}
