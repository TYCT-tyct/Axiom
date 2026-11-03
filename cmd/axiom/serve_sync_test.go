package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/gateway"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/telemetry"
)

// testGatewayConfig is the in-process limits a test world's node starts with. Tests run many nodes at once on machines
// shared with other work, where production's one-second admission budget is not enough for a transaction or a key
// lookup (internal/gateway rig_test.go gives its rigs the same); the tests of a budget set their own.
func testGatewayConfig() gateway.Config {
	limits := gateway.DefaultConfig()
	limits.AdmissionBudget, limits.KeyLookup = 30*time.Second, 30*time.Second
	return limits
}

// serveForTest keeps the entry's admission checks, with periodic downloads disabled unless a test supplies local data.
// A core node's recorder gets a temporary spill directory: the production default is relative to the working
// directory, and a test must not leave call-log/ in the package tree. A node without the core role reads no call log
// and refuses the variable, so it gets none.
func serveForTest(ctx context.Context, environ []string, logger *slog.Logger) error {
	dir, err := os.MkdirTemp("", "axiom-call-log-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			logger.Warn("removing the test call log failed", "dir", dir, "err", err)
		}
	}()
	hasDir, core := false, true
	for _, kv := range environ {
		if strings.HasPrefix(kv, callLogDirVar+"=") {
			hasDir = true
		}
		if value, ok := strings.CutPrefix(kv, nodeRolesVar+"="); ok {
			roles, err := parseRoles(value)
			core = err != nil || roles.has(roleCore) // a refused value fails in serveWith with its own message
		}
	}
	if !hasDir && core {
		environ = append(environ, callLogDirVar+"="+dir)
	}
	return serveWith(ctx, environ, logger, directEgress, startRecorder, nil, testGatewayConfig())
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
		"AXIOM_SHUTDOWN_TIMEOUT=3s", "AXIOM_DATABASE_URL=" + database.App,
		"AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'),
		"AXIOM_IDENTITY_KEY_FILE=" + keyFile(t, 'i'), "AXIOM_CALL_LOG_DIR=" + t.TempDir(),
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
		done <- serveWith(ctx, environ, slog.New(slog.NewJSONHandler(logs, nil)), loopback, startRecorder, prices, testGatewayConfig())
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
	// The upstream has not supplied its response. Cancellation must reach it at once, not wait for the queue drain
	// deadline; the node then exits within the shutdown budget, which includes the edge delay.
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
	case <-time.After(10 * time.Second):
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
