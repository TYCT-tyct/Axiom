package pg_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestDedicatedGoesOnWhileEveryPooledConnectionIsTaken: a transaction on a connection of its own runs while the only
// ordinary connection of the pool is held by a call, which is the point of the connection the holder's heartbeat and
// reap keep (#217). The same transaction on the pool cannot even start then: the pool is exhausted, not merely busy.
func TestDedicatedGoesOnWhileEveryPooledConnectionIsTaken(t *testing.T) {
	app := open(t, newDatabase(t).App) // one ordinary connection, one for the completion class
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- app.Tx(t.Context(), pg.PlatformScope(), func(pg.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Errorf("the transaction that held the pool: %v", err)
		}
	}()

	dedicated := app.Dedicated()
	defer dedicated.Close(t.Context())
	var one int
	if err := dedicated.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT 1").Scan(&one)
	}); err != nil || one != 1 {
		t.Fatalf("the dedicated transaction = %d, %v; want 1 while the pool is exhausted", one, err)
	}

	pooled, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := app.Tx(pooled, pg.PlatformScope(), func(pg.Tx) error { return nil }); err == nil {
		t.Fatal("a pooled transaction ran: the connection the dedicated one used came from the pool, so the test proved nothing")
	}
}

// TestDedicatedReconnectsAfterItsSessionIsKilled: the connection of its own is one session, and a session the server
// ends takes whatever transaction was running with it; the next transaction connects anew, so a beat after an outage or
// a restart is on a fresh session and not on a connection that only looks open.
func TestDedicatedReconnectsAfterItsSessionIsKilled(t *testing.T) {
	database := newDatabase(t)
	app := open(t, database.App)
	dedicated := app.Dedicated()
	defer dedicated.Close(t.Context())

	var pid int
	if err := dedicated.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&pid)
	}); err != nil {
		t.Fatal(err)
	}

	// The query that runs on the session when it is ended waits in the database, so the ending and the failure meet in
	// the same transaction whatever the order of the two.
	slept := make(chan error, 1)
	go func() {
		slept <- dedicated.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
			_, err := tx.Exec(t.Context(), "SELECT pg_sleep(30)")
			return err
		})
	}()
	if err := pgtest.Run(t.Context(), database.Superuser, fmt.Sprintf("SELECT pg_terminate_backend(%d)", pid)); err != nil {
		t.Fatal(err)
	}
	if err := <-slept; err == nil {
		t.Fatal("a transaction on the killed session succeeded")
	}

	var again int
	if err := dedicated.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&again)
	}); err != nil {
		t.Fatalf("the transaction after the session was killed: %v", err)
	}
	if again == pid {
		t.Errorf("the transaction after the kill ran on session %d, the one that was killed", pid)
	}
}
