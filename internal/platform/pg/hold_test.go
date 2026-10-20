package pg_test

import (
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
