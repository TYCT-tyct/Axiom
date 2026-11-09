package pg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestHoldIsOneSessionPerName: a name is held by one session at a time and another session is refused at once; another
// name is free; a released name is free again; and the connection that holds it is not one of the pool's, which has one
// connection here and still serves a transaction (README「连接池」).
func TestHoldIsOneSessionPerName(t *testing.T) {
	t.Parallel()
	url := pgtest.New(t).App
	first, second := open(t, url), open(t, url)
	held, err := first.Hold(t.Context(), "node:a", time.Minute)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := second.Hold(t.Context(), "node:a", time.Minute); !errors.Is(err, pg.ErrHeld) {
		t.Fatalf("a second session holding the same name: %v, want ErrHeld", err)
	}
	other, err := second.Hold(t.Context(), "node:b", time.Minute)
	if err != nil {
		t.Fatalf("another name: %v", err)
	}
	if err := first.Tx(t.Context(), pg.Scope{}, func(pg.Tx) error { return nil }); err != nil {
		t.Fatalf("the pool, with its one connection, after a hold: %v", err)
	}
	if err := held.Release(t.Context()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := held.Release(t.Context()); err != nil {
		t.Fatalf("a second release: %v", err)
	}
	again, err := second.Hold(t.Context(), "node:a", time.Minute)
	if err != nil {
		t.Fatalf("hold after the release: %v", err)
	}
	for _, h := range []*pg.Hold{again, other} {
		if err := h.Release(t.Context()); err != nil {
			t.Fatalf("release: %v", err)
		}
	}
}

// TestHoldAsksTheServerToDropADeadConnection: the holder's session sets TCP keepalive probes and a user timeout on itself,
// so that when the host or the network goes without closing anything the server frees the name within about half a minute
// and not after its two-hour default (the settings are the server's, and a Unix socket ignores them).
func TestHoldAsksTheServerToDropADeadConnection(t *testing.T) {
	t.Parallel()
	held, err := open(t, pgtest.New(t).App).Hold(t.Context(), "node:a", time.Hour)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(t.Context())) }) //nolint:errcheck // the test is over
	for name, want := range map[string]string{
		"tcp_keepalives_idle": "5", "tcp_keepalives_interval": "5", "tcp_keepalives_count": "3", "tcp_user_timeout": "20000",
	} {
		if got, err := held.Setting(t.Context(), name); err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", name, got, err, want)
		}
	}
}

// TestHoldEndsWithItsConnection: a holder whose connection is cut, as a crash or a lost network cuts it, finds out at its
// next check and says so on Lost, and the name is free for the next session at once.
func TestHoldEndsWithItsConnection(t *testing.T) {
	t.Parallel()
	url := pgtest.New(t).App
	holder, other := open(t, url), open(t, url)
	held, err := holder.Hold(t.Context(), "node:a", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	select {
	case <-held.Lost():
		t.Fatal("a hold that has not been cut is lost")
	default:
	}
	err = other.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT pg_terminate_backend(l.pid) FROM pg_locks l
			WHERE l.locktype = 'advisory' AND l.granted AND l.pid <> pg_backend_pid()
			AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())`)
		return err
	})
	if err != nil {
		t.Fatalf("cut the connection: %v", err)
	}
	select {
	case <-held.Lost():
	case <-time.After(time.Minute): // only catches a loss that is never noticed
		t.Fatal("a cut connection was never reported as lost")
	}
	next, err := other.Hold(t.Context(), "node:a", time.Minute)
	if err != nil {
		t.Fatalf("hold after the holder's connection was cut: %v", err)
	}
	if err := next.Release(t.Context()); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// TestRetakeTakesTheNameBackOnlyFromItself: a holder whose connection was lost takes its name back, also while the server
// still keeps its old session, as it does after a network cut until its keepalive finds out; it is refused with ErrHeld
// only when another session took the name in between, never because the database cannot be reached.
func TestRetakeTakesTheNameBackOnlyFromItself(t *testing.T) {
	t.Parallel()
	url := pgtest.New(t).App
	holder, other := open(t, url), open(t, url)
	held, err := holder.Hold(t.Context(), "node:a", time.Minute)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	t.Cleanup(func() { _ = held.Release(context.WithoutCancel(t.Context())) }) //nolint:errcheck // its session was ended by the retake
	unreachable, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := held.Retake(unreachable); err == nil || errors.Is(err, pg.ErrHeld) {
		t.Fatalf("retake without a database to reach: %v, want an error other than ErrHeld, which would end the holder", err)
	}
	again, err := held.Retake(t.Context())
	if err != nil {
		t.Fatalf("retake while the server keeps the holder's old session: %v", err)
	}
	t.Cleanup(func() { _ = again.Release(context.WithoutCancel(t.Context())) }) //nolint:errcheck // cut below
	err = other.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT pg_terminate_backend(l.pid, 30000) FROM pg_locks l
			WHERE l.locktype = 'advisory' AND l.granted AND l.pid <> pg_backend_pid()
			AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())`)
		return err
	})
	if err != nil {
		t.Fatalf("cut the retaken connection: %v", err)
	}
	taken, err := other.Hold(t.Context(), "node:a", time.Minute)
	if err != nil {
		t.Fatalf("another session taking the freed name: %v", err)
	}
	t.Cleanup(func() { _ = taken.Release(context.WithoutCancel(t.Context())) }) //nolint:errcheck // the test is over
	if _, err := again.Retake(t.Context()); !errors.Is(err, pg.ErrHeld) {
		t.Fatalf("retake of a name another session holds: %v, want ErrHeld", err)
	}
}
