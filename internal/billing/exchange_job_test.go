package billing

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
)

type exchangeFixture struct {
	rules
	job   exchangeJob
	at    time.Time
	body  atomic.Value
	calls atomic.Int64
}

func newExchangeJob(t *testing.T) *exchangeFixture {
	t.Helper()
	f := &exchangeFixture{rules: newRules(t), at: exchangeAt}
	f.body.Store(exchangeSample)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.calls.Add(1)
		if _, err := io.WriteString(w, f.body.Load().(string)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	client := egress.NewForTesting(egresstest.Loopback(t))
	t.Cleanup(client.CloseIdleConnections)
	f.job = exchangeJob{db: f.db, scope: pg.PlatformScope(), client: client, url: server.URL, logger: slog.New(slog.DiscardHandler), now: func() time.Time { return f.at }}
	return f
}

func (f *exchangeFixture) run() {
	f.t.Helper()
	if err := f.job.run(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *exchangeFixture) count(query string) int {
	f.t.Helper()
	var n int
	if err := f.tx(func(tx pg.Tx) error { return tx.QueryRow(f.t.Context(), query).Scan(&n) }); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestExchangeSyncDailyAtomicAndManual(t *testing.T) {
	f := newExchangeJob(t)
	if err := f.tx(func(tx pg.Tx) error {
		_, err := SetExchangeRate(t.Context(), tx, system, fxInput("71", "10", "initial", 0))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.run()
	pairs := len(minorDigits) * (len(minorDigits) - 1) / 2
	if got := f.count("SELECT count(*) FROM billing.exchange_rates"); got != pairs {
		t.Fatalf("rows %d want %d", got, pairs)
	}
	if got := f.count("SELECT count(*) FROM billing.exchange_rates WHERE source='ecb' AND source_at='2026-10-29'"); got != pairs-1 {
		t.Fatalf("automatic rows %d", got)
	}
	f.run()
	if f.calls.Load() != 1 {
		t.Fatal("replay downloaded again")
	}
	f.at = f.at.Add(24 * time.Hour)
	f.run()
	if got := f.count("SELECT count(*) FROM billing.exchange_rates"); got != pairs {
		t.Fatal("same source date appended versions")
	}
	// A rejected pair rolls back earlier pairs in the same batch.
	f.at = f.at.Add(24 * time.Hour)
	f.body.Store(strings.ReplaceAll(strings.ReplaceAll(exchangeSample, "2026-10-29", "2026-10-31"), `rate="1.1234"`, `rate="100"`))
	if err := f.job.run(t.Context()); err == nil {
		t.Fatal("accepted a greater-than-fivefold move")
	}
	if got := f.count("SELECT count(*) FROM billing.exchange_rates"); got != pairs {
		t.Fatal("partial batch committed")
	}
	f.at = f.at.Add(24 * time.Hour)
	f.body.Store(strings.ReplaceAll(exchangeSample, "2026-10-29", "2026-11-01"))
	f.run()
	if got := f.count("SELECT count(*) FROM billing.exchange_rates"); got != 2*pairs-1 {
		t.Fatalf("new date rows %d", got)
	}
	if got := f.count("SELECT count(*) FROM billing.exchange_rates WHERE source='manual'"); got != 1 {
		t.Fatal("manual rate overwritten")
	}
	f.at = f.at.Add(24 * time.Hour)
	f.body.Store(exchangeSample)
	if err := f.job.run(t.Context()); err == nil {
		t.Fatal("accepted source date regression")
	}
	if got := f.count("SELECT count(*) FROM billing.exchange_rates"); got != 2*pairs-1 {
		t.Fatal("regressed batch committed")
	}
}

func TestExchangeSyncStaleAndRecovery(t *testing.T) {
	f := newExchangeJob(t)
	f.run()
	f.at = time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	f.run()
	if got := f.count("SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='billing.exchange.stale'"); got != 0 {
		t.Fatal("alert at exact seven-day boundary")
	}
	f.at = f.at.Add(time.Hour)
	f.run()
	// Same old XML still downloads successfully: its source date, not download time, determines freshness.
	if got := f.count("SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='billing.exchange.stale' AND NOT coalesce((body->>'resolved')::boolean,false)"); got != 1 {
		t.Fatalf("stale firing signals %d", got)
	}
	f.at = f.at.Add(time.Hour)
	f.run()
	if got := f.count("SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='billing.exchange.stale'"); got != 1 {
		t.Fatal("repeated stale signal")
	}
	f.at = f.at.Add(24 * time.Hour)
	f.body.Store(strings.ReplaceAll(exchangeSample, "2026-10-29", "2026-11-06"))
	f.run()
	if got := f.count("SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='billing.exchange.stale' AND body->>'resolved'='true'"); got != 1 {
		t.Fatalf("recovery signals %d", got)
	}
}

func TestExchangeSyncNeverSucceeded(t *testing.T) {
	f := newExchangeJob(t)
	f.body.Store("broken XML")
	f.run()
	f.at = f.at.Add(exchangeMaxAge + time.Hour)
	f.run()
	if got := f.count("SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='billing.exchange.stale'"); got != 1 {
		t.Fatalf("missing never-succeeded alert: %d", got)
	}
	if got := f.count("SELECT count(*) FROM billing.exchange_rates"); got != 0 {
		t.Fatal("invalid source saved")
	}
}

func TestExchangeSyncConcurrentClaims(t *testing.T) {
	f := newExchangeJob(t)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { <-start; errs <- f.job.run(t.Context()) })
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.calls.Load() != 1 {
		t.Fatalf("downloads %d", f.calls.Load())
	}
}

func TestExchangeFetchBoundaries(t *testing.T) {
	client := egress.NewForTesting(egresstest.Loopback(t))
	defer client.CloseIdleConnections()
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"redirect", http.StatusFound, exchangeSample},
		{"server error", http.StatusServiceUnavailable, "unavailable"},
		{"oversized", http.StatusOK, strings.Repeat(" ", 64<<10) + exchangeSample},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				if _, err := io.WriteString(w, tc.body); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			_, _, err := fetchExchange(t.Context(), client, server.URL, exchangeAt)
			if err == nil {
				t.Fatal("accepted failed/oversized response")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := fetchExchange(ctx, client, "http://127.0.0.1:1", exchangeAt)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestExchangeSyncStateScope(t *testing.T) {
	f := newRules(t)
	for _, scope := range []pg.Scope{pg.NewScope(false, nil, nil), pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()})} {
		err := f.db.Tx(t.Context(), scope, func(tx pg.Tx) error { return checkExchangeStateHidden(t, tx) })
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"DELETE FROM billing.exchange_sync", "INSERT INTO billing.exchange_sync(singleton) VALUES(false)"} {
		if err := f.tx(func(tx pg.Tx) error { _, err := tx.Exec(t.Context(), query); return err }); pg.SQLState(err) != "42501" {
			t.Fatalf("application changed sync state membership: %v", err)
		}
	}
}

func checkExchangeStateHidden(t *testing.T, tx pg.Tx) error {
	t.Helper()

	var n int
	if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM billing.exchange_sync").Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		t.Fatal("tenant read platform sync state")
	}
	tag, err := tx.Exec(t.Context(), "UPDATE billing.exchange_sync SET alerted=true")
	if err == nil && tag.RowsAffected() != 0 {
		t.Fatal("tenant changed sync state")
	}
	return err
}
