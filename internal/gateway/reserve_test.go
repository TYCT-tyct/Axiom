package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/platform"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
)

type reserveJob struct{}

func (reserveJob) Kind() string { return "test.reserve" }
func (reserveJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueSyncs}
}

// Background writes and the console use no gateway permits. Four ordinary connections remain held: a River worker,
// the console transaction, the real recorder and River's own query, the latter two blocked on their tables.
func TestBackgroundAndConsoleCannotTakeTheSettlementReserve(t *testing.T) {
	r := newRig(t, func(o *rigOptions) {
		o.conns = 5
		o.config = func(c *Config) { c.PoolSize, c.PoolReserve = 5, 1 }
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var c *Call
	deps := r.handler.Deps
	deps.Run = func(_ http.ResponseWriter, _ *http.Request, admitted *Call) { c = admitted }
	handler, err := NewHandler(deps)
	if err != nil {
		t.Fatal(err)
	}
	a := r.newAccount(1e9)
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://"+apiHost+"/v1/messages", strings.NewReader(messageBody("acme/alpha")))
	request.Header.Set("Authorization", "Bearer "+a.key)
	response := httptest.NewRecorder()
	httpserver.Public(handler, deps.Logger, time.Minute).ServeHTTP(response, request)
	if c == nil {
		t.Fatalf("admission: %d %s", response.Code, response.Body.String())
	}
	defer c.Done()

	unblockWorker := holdReserveWorker(t, ctx, r.db)
	defer unblockWorker()
	unblockConsole := holdReserveTransaction(t, ctx, r.db, "SELECT 1")
	defer unblockConsole()
	unblockTables := holdReserveBackgroundTables(t, ctx, r)
	defer unblockTables()

	// The ordinary share is full, even though the completion connection is idle.
	wait, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	err = r.db.Tx(wait, platformScope, func(pg.Tx) error {
		t.Error("ordinary work borrowed the completion reserve")
		return nil
	})
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ordinary transaction: %v", err)
	}

	x := &Executor{} // what withFunds needs; the orphan path goes through the handler's own executor
	x.Limits, x.Ledger = r.limits, r.billing
	settle, stop := context.WithTimeout(ctx, settleAttempt)
	defer stop()
	if err := x.withFunds(settle, c, func(ctx context.Context) error {
		_, err := r.billing.Settle(ctx, c.Hold, billing.Outcome{})
		return err
	}); err != nil {
		t.Fatalf("settlement while ordinary work holds every connection: %v", err)
	}
	// The orphan path has its own entry and must select the reserve too.
	if err := handler.orphanOnce(ctx, c); err != nil {
		t.Fatalf("orphan settlement while ordinary work holds every connection: %v", err)
	}
}

func holdReserveTransaction(t *testing.T, ctx context.Context, db *pg.DB, sql string) func() {
	t.Helper()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	unblock := sync.OnceFunc(func() { close(release) })
	go func() {
		done <- db.Tx(ctx, platformScope, func(tx pg.Tx) error {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return err
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("hold transaction: %v", err)
	case <-ctx.Done():
		unblock()
		t.Fatal(ctx.Err())
	}
	t.Cleanup(func() {
		unblock()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("held transaction: %v", err)
		}
	})
	return unblock
}

func holdReserveWorker(t *testing.T, ctx context.Context, db *pg.DB) func() {
	t.Helper()
	q := db.NewQueue(slog.New(slog.DiscardHandler), platformScope, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	river.AddWorker(q.Workers(), river.WorkFunc(func(ctx context.Context, _ *river.Job[reserveJob]) error {
		return db.Tx(ctx, platformScope, func(pg.Tx) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}))
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unblock()
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := q.Stop(stop); err != nil {
			t.Errorf("stop queue: %v", err)
		}
	})
	if err := db.Tx(ctx, platformScope, func(tx pg.Tx) error {
		_, err := q.InsertManyTx(ctx, tx, []river.InsertManyParams{{Args: reserveJob{}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return unblock
}

func holdReserveBackgroundTables(t *testing.T, ctx context.Context, r *rig) func() {
	t.Helper()
	admin, err := pg.Open(ctx, r.database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	unlock := holdReserveTransaction(t, ctx, admin, "LOCK TABLE observability.call_log IN ACCESS EXCLUSIVE MODE")
	logger := slog.New(slog.DiscardHandler)
	recorder := observability.NewRecorder(ctx, r.db, platformScope, observability.Config{
		QueueSize: 1, BatchSize: 1, FlushInterval: time.Hour, WriteTimeout: time.Minute,
		RetryBase: time.Second, RetryMax: time.Second,
	}, logger)
	t.Cleanup(func() {
		unlock()
		drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := recorder.Close(drain); err != nil {
			t.Errorf("close recorder: %v", err)
		}
	})
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Enqueue(observability.CallRecord{ //nolint:contextcheck // Enqueue takes no context: the hand-off never waits
		ID: uuid.NewV7(), At: time.Now(), Site: siteID, Account: uuid.NewV7(),
		IP: netip.MustParseAddr("203.0.113.1"), Class: "chat", Model: "acme/alpha",
		Currency: currency, ConsentVersion: "test",
	}); err != nil {
		t.Fatal(err)
	}
	waitForReserveQuery(t, ctx, admin, "%INSERT INTO observability.call_log%")
	unlockRiver := holdReserveTransaction(t, ctx, admin, "LOCK TABLE river.river_job IN ACCESS EXCLUSIVE MODE")
	// River's driver bypasses DB.Tx, so hold one of its own queries too, not just a worker's business transaction.
	waitForReserveQuery(t, ctx, admin, "%river_job%")
	return func() { unlockRiver(); unlock() }
}

func waitForReserveQuery(t *testing.T, ctx context.Context, admin *pg.DB, pattern string) {
	t.Helper()
	// Observe this test's database lock waits, not a timer or another test's connections.
	waitFor(t, "a query waiting on "+pattern, func() bool {
		var blocked bool
		err := admin.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity WHERE datname = current_database()
				AND wait_event_type = 'Lock' AND query LIKE $1
			)`, pattern).Scan(&blocked)
		})
		if err != nil {
			t.Fatalf("look for a query waiting on %s: %v", pattern, err)
		}
		return blocked
	})
}
