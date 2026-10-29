package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/telemetry"
)

// serveForTest keeps the entry's admission checks, with periodic downloads disabled unless a test supplies local data.
func serveForTest(ctx context.Context, environ []string, logger *slog.Logger) error {
	return serveWith(ctx, environ, logger, directEgress, startRecorder, nil)
}

func TestServeCancelsAPriceFetchOnShutdown(t *testing.T) {
	t.Parallel()
	for _, body := range []bool{false, true} {
		name := "waiting for headers"
		if body {
			name = "reading the body"
		}
		t.Run(name, func(t *testing.T) { checkPriceFetchShutdown(t, body) })
	}
}

func checkPriceFetchShutdown(t *testing.T, body bool) {
	t.Helper()
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var startedOnce, cancelledOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body {
			if _, err := w.Write([]byte(`{`)); err != nil {
				t.Error(err)
				return
			}
			w.(http.Flusher).Flush()
		}
		startedOnce.Do(func() { close(started) })
		select {
		case <-r.Context().Done():
			cancelledOnce.Do(func() { close(cancelled) })
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release) // also unblocks the server if the regression returns
	database := pgtest.New(t, modules()...)
	environ := []string{
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0",
		"AXIOM_SHUTDOWN_TIMEOUT=30s", "AXIOM_DATABASE_URL=" + database.App,
		"AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'),
		"AXIOM_IDENTITY_KEY_FILE=" + keyFile(t, 'i'),
	}
	loopback := func(*telemetry.Telemetry) (*egress.Client, error) { //nolint:unparam // serveWith's production client can fail
		return egress.NewForTesting(egresstest.Loopback(t)), nil
	}
	prices := func(client *egress.Client) *catalog.Fetcher {
		return catalog.NewTestFetcher(t, client, map[catalog.PriceSource]string{
			catalog.SourceLiteLLM: srv.URL, catalog.SourceOpenRouter: srv.URL,
		})
	}
	logs := make(records, 64)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serveWith(ctx, environ, slog.New(slog.NewJSONHandler(logs, nil)), loopback, startRecorder, prices)
	}()
	servingAddrs(t, logs, done)
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("node stopped before fetching: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the startup round never fetched")
	}
	cancel()
	awaitPriceFetchShutdown(t, done, cancelled)
	checkCancelledPriceRound(t, database.App)
}

func awaitPriceFetchShutdown(t *testing.T, done <-chan error, cancelled <-chan struct{}) {
	t.Helper()
	// The upstream has not supplied its response. Cancellation and node exit must precede the 30s queue drain deadline.
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the fetch waited for the queue drain deadline instead of node cancellation")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown during fetch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the node did not shut down after cancelling the fetch")
	}
}

func checkCancelledPriceRound(t *testing.T, url string) {
	t.Helper()
	db, err := pg.Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		var failures, completed int
		var neverSucceeded bool
		if err := tx.QueryRow(t.Context(), `SELECT failures, last_success_at IS NULL FROM catalog.sync_state`).Scan(&failures, &neverSucceeded); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM river.river_job WHERE kind = 'catalog.sync' AND state = 'completed'`).Scan(&completed); err != nil {
			return err
		}
		if failures != 1 || !neverSucceeded || completed != 1 {
			t.Errorf("cancelled round: failures=%d never succeeded=%v completed=%d", failures, neverSucceeded, completed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
