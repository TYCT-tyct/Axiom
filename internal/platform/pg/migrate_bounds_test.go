package pg_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// deaf is a migration that ignores a cancel: it catches the cancellation of each second's sleep and goes on. Only ending the
// session stops it, so a run that ends is one that terminated its sessions, not one that asked them to cancel (which
// pgx does from the background, and a statement timeout would be caught the same way).
const deaf = `DO $$ BEGIN LOOP BEGIN PERFORM pg_sleep(1); EXCEPTION WHEN query_canceled THEN NULL; END; END LOOP; END $$;`

// TestMigrateEndsItsSessionsAtTheDeadline: a statement that ignores a cancel is ended when the run's context ends; the
// run returns once the server has been told, and leaves nothing running and no lock held, so the migration runs when
// tried again. The context ends only after the server shows the statement running, so the end always falls in the
// statement that swallows a cancel, however slow the machine. Not parallel, as Migrate grants roles cluster-wide.
func TestMigrateEndsItsSessionsAtTheDeadline(t *testing.T) {
	database := pgtest.New(t)
	slow := pg.Module{Name: "quality", Migrations: fstest.MapFS{"00001_deaf.sql": {Data: []byte(up + deaf)}}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	watchCtx, stopWatching := context.WithCancel(t.Context())
	observed := watchRunning(watchCtx, database.Superuser, "%PERFORM pg_sleep(1)%")
	defer func() {
		stopWatching()
		for range observed { // the poller has ended before the test does
		}
	}()
	done := make(chan error, 1)
	go func() { done <- pg.Migrate(ctx, database.Migrator, slow) }()
	requireSeen(t, observed, done)
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(time.Minute):
		t.Fatal("the run did not return within a minute of its context ending")
	}
	if err == nil || strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("%v, want the run to return because its sessions were ended, not because the grace after the deadline ran out", err)
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
	fast := pg.Module{Name: "quality", Migrations: fstest.MapFS{"00001_deaf.sql": {Data: []byte(up + "SELECT 1;")}}}
	if err := pg.Migrate(t.Context(), database.Migrator, fast); err != nil {
		t.Errorf("the migration again: %v", err)
	}
}

// requireSeen fails the test unless observed reports the statement running before the run in done returns or a minute
// passes.
func requireSeen(t *testing.T, observed, done <-chan error) {
	t.Helper()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatalf("watching for the running statement: %v", err)
		}
	case err := <-done:
		t.Fatalf("the run returned before its statement was seen running: %v", err)
	case <-time.After(time.Minute):
		t.Fatal("the statement was not seen running within a minute")
	}
}

// watchRunning polls until the server shows a session of a migration run in the test database executing a statement like
// query. The channel then carries nil, or the error that ended the poll (ctx included), and is closed.
func watchRunning(ctx context.Context, superuserURL, query string) <-chan error {
	result := make(chan error, 1)
	go func() {
		defer close(result)
		result <- poll(ctx, superuserURL, query)
	}()
	return result
}

func poll(ctx context.Context, superuserURL, query string) error {
	conn, err := pgx.Connect(ctx, superuserURL)
	if err != nil {
		return err
	}
	defer conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck // a read-only poll
	for {
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE state = 'active'
			AND datname = current_database() AND application_name LIKE 'axiom-migrate-%' AND query LIKE $1`, query).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestMigrateKeepsItsWorkErrorWhenItCannotEndItsSessions: when the server cannot be reached to end the sessions at the
// deadline, the error says so and still carries what the migration itself reported; the connection failure is
// redacted, the work's error is not. A proxy in front of the server stops accepting once the migration is running, so
// the connection that ends the sessions is refused while the migration's own connections stay up.
func TestMigrateKeepsItsWorkErrorWhenItCannotEndItsSessions(t *testing.T) {
	database := pgtest.New(t)
	u := parse(t, database.Migrator)
	listener := proxy(t, u.Host)
	t.Cleanup(func() {
		listener.Close() //nolint:errcheck,gosec // already closed once the migration was running
		// The sleeping statement the cancelled run left behind.
		if err := pgtest.Run(context.WithoutCancel(t.Context()), database.Superuser, `SELECT pg_terminate_backend(pid)
			FROM pg_stat_activity WHERE application_name LIKE 'axiom-migrate-%' AND pid <> pg_backend_pid()`); err != nil {
			t.Error(err)
		}
	})
	u.Host = listener.Addr().String()
	sleeper := pg.Module{Name: "quality", Migrations: fstest.MapFS{"00001_sleep.sql": {Data: []byte(up + "SELECT pg_sleep(20);")}}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	watchCtx, stopWatching := context.WithCancel(t.Context())
	observed := watchRunning(watchCtx, database.Superuser, "%pg_sleep(20)%")
	defer func() {
		stopWatching()
		for range observed { // the poller has ended before the test does
		}
	}()
	// The connect timeout is long: this test is about the end of the run, and a connection that is slow on a loaded
	// machine (#113) must not end it early.
	done := make(chan error, 1)
	go func() { done <- pg.MigrateWith(ctx, u.String(), time.Minute, sleeper) }()
	requireSeen(t, observed, done)
	listener.Close() //nolint:errcheck,gosec // refusing new connections is the point
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(time.Minute):
		t.Fatal("the run did not return within a minute of its context ending")
	}
	if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "AXIOM_MIGRATE_DATABASE_URL: cannot reach the server") {
		t.Errorf("Migrate: %v, want the cancelled work and the refused connection that ends the sessions", err)
	}
}

// proxy forwards connections from a new local address to server until the listener is closed; connections already
// made go on.
func proxy(t *testing.T, server string) net.Listener {
	t.Helper()
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := new(net.Dialer).DialContext(t.Context(), "tcp", server)
			if err != nil {
				client.Close() //nolint:errcheck,gosec // the test's proxy has nothing to report
				continue
			}
			go pipe(upstream, client)
			go pipe(client, upstream)
		}
	}()
	return listener
}

// pipe copies src to dst until either side closes, then closes dst.
func pipe(dst, src net.Conn) {
	io.Copy(dst, src) //nolint:errcheck,gosec // the proxy ends when either side closes
	dst.Close()       //nolint:errcheck,gosec // as above
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
		if took := time.Since(start); err == nil || took > 2*time.Second || !strings.Contains(err.Error(), "time limit") {
			t.Errorf("three silent addresses%s: %v after %v, want an error after about 1s, not 3s", query, err, took)
		}
	}
}
