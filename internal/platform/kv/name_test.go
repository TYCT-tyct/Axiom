package kv_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
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
// name when the database answers again, so the node is no longer the only one of its name and stops (Lost). The other
// process waits on the name's lock before the node's session is cut, so the server hands it the name as that session
// ends, before the node can try again.
func TestANodeWhoseNameWasTakenStops(t *testing.T) {
	t.Parallel()
	url := pgtest.New(t).App
	db, err := pg.Open(t.Context(), url)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(db.Close)
	node, err := openNamed(t, db, pg.PlatformScope(), "node-a")
	if err != nil {
		t.Fatalf("the node: %v", err)
	}
	other, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close(context.WithoutCancel(t.Context())) }) //nolint:errcheck // the test is over
	key := nameKey(t, db)
	taken := make(chan error, 1)
	go func() { _, err := other.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, key); taken <- err }()
	for !lockAwaited(t, db) {
		time.Sleep(10 * time.Millisecond) // until the other session waits on the lock; no outcome depends on how long
	}
	cutNameHolders(t, db)
	if err := <-taken; err != nil {
		t.Fatalf("another process taking the name as the node's session ends: %v", err)
	}
	select {
	case <-node.Lost():
	case <-time.After(time.Minute): // only catches a node that never finds out
		t.Fatal("the node kept running under a name another process holds")
	}
}

// nameKey is the lock key of the one name held in the database.
func nameKey(t *testing.T, db *pg.DB) (key int64) {
	t.Helper()
	if err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (classid::bigint << 32) | objid::bigint FROM pg_locks WHERE locktype = 'advisory'
			AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&key)
	}); err != nil {
		t.Fatalf("read the name's lock key: %v", err)
	}
	return key
}

// lockAwaited says whether a session waits on a name's lock.
func lockAwaited(t *testing.T, db *pg.DB) (waits bool) {
	t.Helper()
	if err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT EXISTS (SELECT FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database()))`).Scan(&waits)
	}); err != nil {
		t.Fatalf("read the waits on the name's lock: %v", err)
	}
	return waits
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
