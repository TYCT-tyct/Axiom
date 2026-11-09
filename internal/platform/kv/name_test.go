package kv_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/pg"
)

// TestANodeTakesItsNameBackAfterTheDatabaseWasOut: a database that restarts or is cut off ends the session that holds the
// node's name, and the node serves through the outage: it takes the name back on a new session once the database answers,
// so a second process under the name is still refused, and the node does not stop (Lost).
func TestANodeTakesItsNameBackAfterTheDatabaseWasOut(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	scope := pg.PlatformScope()
	node, err := openNamed(t, db, scope, "node-a")
	if err != nil {
		t.Fatalf("the node: %v", err)
	}
	cutNameHolders(t, db)
	for deadline := time.Now().Add(time.Minute); nameHolders(t, db) != 1; time.Sleep(100 * time.Millisecond) {
		select {
		case <-node.Lost():
			t.Fatal("the node stopped because its connection was cut")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the node never took its name back") // only catches a retake that never happens
		}
	}
	if _, err := openNamed(t, db, scope, "node-a"); err == nil || !strings.Contains(err.Error(), kv.NodeIDVar) {
		t.Fatalf("a second process under the name after the outage: %v, want a refusal naming %s", err, kv.NodeIDVar)
	}
	select {
	case <-node.Lost():
		t.Fatal("the node stopped after it took its name back")
	default:
	}
}

// TestANodeWhoseNameWasTakenStops: a process that started under the node's name while the node was cut off holds the
// name when the database answers again, so the node is no longer the only one of its name and stops (Lost).
func TestANodeWhoseNameWasTakenStops(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	node, err := openNamed(t, db, pg.PlatformScope(), "node-a")
	if err != nil {
		t.Fatalf("the node: %v", err)
	}
	cutNameHolders(t, db)
	// The node checks its connection every few seconds, so the other process is in first.
	taken, err := db.Hold(t.Context(), "axiom node node-a", time.Minute)
	if err != nil {
		t.Fatalf("another process taking the name while the node is cut off: %v", err)
	}
	t.Cleanup(func() { _ = taken.Release(context.WithoutCancel(t.Context())) }) //nolint:errcheck // the test is over
	select {
	case <-node.Lost():
	case <-time.After(time.Minute): // only catches a node that never finds out
		t.Fatal("the node kept running under a name another process holds")
	}
}

// cutNameHolders ends every session that holds a name, as a database restart or a cut network ends it, and waits until
// they are gone.
func cutNameHolders(t *testing.T, db *pg.DB) {
	t.Helper()
	if err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT pg_terminate_backend(pid, 30000) FROM pg_locks
			WHERE locktype = 'advisory' AND granted AND pid <> pg_backend_pid()
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`)
		return err
	}); err != nil {
		t.Fatalf("cut the sessions that hold names: %v", err)
	}
}

// nameHolders is how many sessions hold a name.
func nameHolders(t *testing.T, db *pg.DB) int {
	t.Helper()
	var n int
	if err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND granted
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&n)
	}); err != nil {
		t.Fatalf("count the sessions that hold names: %v", err)
	}
	return n
}
