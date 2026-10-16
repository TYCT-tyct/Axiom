package pg_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// deaf is a migration that ignores a cancel: it catches the cancellation of each second's sleep and goes on. Only ending the
// session stops it, so a run that ends is one that terminated its sessions, not one that asked them to cancel (which
// pgx does from the background, and a statement timeout would be caught the same way).
const deaf = `DO $$ BEGIN LOOP BEGIN PERFORM pg_sleep(1); EXCEPTION WHEN query_canceled THEN NULL; END; END LOOP; END $$;`

// TestMigrateEndsItsSessionsAtTheDeadline: after most of the budget is spent, a long statement that ignores a cancel is
// ended at the run's deadline, counted from the start of the run and not of the statement; the run returns once the
// server has been told, and leaves nothing running and no lock held, so the migration runs when tried again. Not
// parallel, as Migrate grants roles cluster-wide.
func TestMigrateEndsItsSessionsAtTheDeadline(t *testing.T) {
	database := pgtest.New(t)
	slow := pg.Module{Name: "quality", Migrations: fstest.MapFS{
		"00001_spend.sql": {Data: []byte(up + "SELECT pg_sleep(2.5);")},
		"00002_deaf.sql":  {Data: []byte(up + deaf)},
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	start := time.Now()
	err := pg.Migrate(ctx, database.Migrator, slow)
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("after %v: %v, want an error soon after the 3s deadline", time.Since(start), err)
	}
	superuser, err := pg.Open(t.Context(), database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(superuser.Close)
	var running int
	if err := superuser.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'axiom-migrate-%'
			AND pid <> pg_backend_pid()`).Scan(&running)
	}); err != nil {
		t.Fatal(err)
	}
	if running != 0 {
		t.Errorf("%d sessions of the run still on the server when MigrateWith returned", running)
	}
	fast := pg.Module{Name: "quality", Migrations: fstest.MapFS{
		"00001_spend.sql": {Data: []byte(up + "SELECT 1;")}, "00002_deaf.sql": {Data: []byte(up + "SELECT 1;")},
	}}
	if err := pg.Migrate(t.Context(), database.Migrator, fast); err != nil {
		t.Errorf("the migration again: %v", err)
	}
}

// TestRunBounded: when the context ends, stop runs before the wait; work that does not stop is given up on a grace after
// the deadline, with stop's error; work that stops in time returns its own result, and stop is not run when it finishes
// first.
func TestRunBounded(t *testing.T) {
	t.Parallel()
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	stopped := errors.New("stop failed")
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := pg.RunBounded(ctx, 200*time.Millisecond, func() error { return stopped }, func() error { <-stuck; return nil })
	if err == nil || !strings.Contains(err.Error(), "did not stop") || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, stopped) || time.Since(start) > 5*time.Second {
		t.Errorf("a run that does not stop: %v after %v", err, time.Since(start))
	}
	want := errors.New("the work's own")
	ctx2, cancel2 := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel2()
	ended := make(chan struct{})
	err = pg.RunBounded(ctx2, time.Minute, func() error { close(ended); return nil }, func() error { <-ended; return want })
	if !errors.Is(err, want) {
		t.Errorf("work that ends because stop ended it: %v", err)
	}
	called := false
	if err := pg.RunBounded(t.Context(), time.Minute, func() error { called = true; return nil }, func() error { return want }); !errors.Is(err, want) || called {
		t.Errorf("work that finishes first: %v, stop called: %v", err, called)
	}
}

// hanging is a connector whose connect never ends by itself.
type hanging struct{}

func (hanging) Connect(ctx context.Context) (driver.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (hanging) Driver() driver.Driver { return nil }

// TestPoolConnectionsHaveADeadline: the connections the migrations run on, made by database/sql, are bound by the same
// connect timeout as the first one.
func TestPoolConnectionsHaveADeadline(t *testing.T) {
	t.Parallel()
	start := time.Now()
	_, err := pg.NewTimedConnector(hanging{}, 100*time.Millisecond).Connect(t.Context())
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Errorf("%v after %v", err, time.Since(start))
	}
}

// TestMigrateConnectTimeoutCoversEveryAddress: the connect timeout is one deadline for the whole connect, not one per
// address of the host, and a connect_timeout in the URL cannot lengthen it.
func TestMigrateConnectTimeoutCoversEveryAddress(t *testing.T) {
	var hosts []string
	for range 3 {
		silent, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := silent.Close(); err != nil {
				t.Error(err)
			}
		})
		hosts = append(hosts, silent.Addr().String())
	}
	for _, query := range []string{"", "?connect_timeout=30"} {
		start := time.Now()
		err := pg.MigrateWith(t.Context(), "postgres://u:p@"+strings.Join(hosts, ",")+"/d"+query, time.Second)
		if took := time.Since(start); err == nil || took > 2*time.Second {
			t.Errorf("three silent addresses%s: %v after %v, want an error after about 1s, not 3s", query, err, took)
		}
	}
}
