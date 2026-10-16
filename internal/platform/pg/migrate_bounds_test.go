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

// TestMigrateStatementsEndOnTheServer: every connection the run makes carries a statement timeout of what is left of the
// run, so the server ends a statement at the deadline by itself, whether or not the client's cancel request got out
// before the process exited; the run also leaves the server clean, so the migration can be tried again. A migration that
// fails when statement_timeout is off reads the setting. Not parallel, as Migrate grants roles cluster-wide.
func TestMigrateStatementsEndOnTheServer(t *testing.T) {
	database := pgtest.New(t)
	const check = up + `DO $$ BEGIN IF current_setting('statement_timeout') = '0' THEN RAISE EXCEPTION 'no statement timeout'; END IF; END $$;`
	checking := pg.Module{Name: "quality", Migrations: fstest.MapFS{"00001_check.sql": {Data: []byte(check)}}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := pg.Migrate(ctx, database.Migrator, checking); err != nil {
		t.Fatalf("with a deadline: %v", err)
	}
	// Without a deadline there is nothing to bound, and the setting is off.
	other := pgtest.New(t)
	if err := pg.Migrate(t.Context(), other.Migrator, checking); err == nil || !strings.Contains(err.Error(), "no statement timeout") {
		t.Errorf("without a deadline: %v", err)
	}
}

// TestMigrateLeavesNothingRunning: after a run that ended at its deadline the long statement is gone from the server,
// the lock is free, and the migration runs when tried again.
func TestMigrateLeavesNothingRunning(t *testing.T) {
	database := pgtest.New(t)
	slow := pg.Module{Name: "quality", Migrations: fstest.MapFS{"00001_slow.sql": {Data: []byte(up + "SELECT pg_sleep(121);")}}}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := pg.Migrate(ctx, database.Migrator, slow); err == nil {
		t.Fatal("the slow migration finished")
	}
	superuser, err := pg.Open(t.Context(), database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(superuser.Close)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var running int
		if err := superuser.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE state = 'active' AND query LIKE '%pg_sleep(121)%' AND pid <> pg_backend_pid()").Scan(&running)
		}); err != nil {
			t.Fatal(err)
		}
		if running == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d statements still running on the server", running)
		}
		time.Sleep(100 * time.Millisecond)
	}
	fast := pg.Module{Name: "quality", Migrations: fstest.MapFS{"00001_slow.sql": {Data: []byte(up + "SELECT 1;")}}}
	if err := pg.Migrate(t.Context(), database.Migrator, fast); err != nil {
		t.Errorf("the migration again: %v", err)
	}
}

// TestRunBounded: a run whose work does not stop, such as goose releasing its lock on a connection that went silent with a
// context that cannot be cancelled, is given up on a grace after the deadline; work that stops in time returns its own
// result.
func TestRunBounded(t *testing.T) {
	t.Parallel()
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := pg.RunBounded(ctx, 200*time.Millisecond, func() error { <-stuck; return nil })
	if err == nil || !strings.Contains(err.Error(), "did not stop") || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Errorf("a run that does not stop: %v after %v", err, time.Since(start))
	}
	want := errors.New("the work's own")
	ctx2, cancel2 := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel2()
	if err := pg.RunBounded(ctx2, time.Minute, func() error { <-ctx2.Done(); return want }); !errors.Is(err, want) {
		t.Errorf("work that stops after the deadline, within the grace: %v", err)
	}
	if err := pg.RunBounded(t.Context(), time.Minute, func() error { return want }); !errors.Is(err, want) {
		t.Errorf("work that finishes: %v", err)
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
