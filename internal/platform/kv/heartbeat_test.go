package kv_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestSecondNodeIsRefused: the heartbeat finds a second node that would reach the same database with the same N, which
// is either N set too low or a node that was started without Valkey (README「配置与启动」, 「节点心跳」).
func TestSecondNodeIsRefused(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	db := openDB(t)
	scope := pg.PlatformScope()
	first := openNode(t, server, db, scope, 1)
	if got := first.Nodes(); got != 1 {
		t.Fatalf("one node reports %d nodes", got)
	}
	_, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()), kv.Deps{DB: db, Scope: scope})
	if err == nil {
		t.Fatal("a second node started against the same N")
	}
	for _, want := range []string{kv.NodesVar, kv.URLVar, "are alive besides this one"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
	// The first node's row is what the second one found, so it must go: a graceful exit deletes it.
	if rows := heartbeats(t, db, scope); rows != 1 {
		t.Fatalf("%d heartbeat rows before the first node closed", rows)
	}
}

// TestHeartbeatGoesStale: a node that stops writing its row is gone after the heartbeat's staleness bound, so a crash
// costs a restart at most that long (README「节点心跳」).
func TestHeartbeatGoesStale(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	db := openDB(t)
	scope := pg.PlatformScope()
	openNode(t, server, db, scope, 1)
	// Aging the row is the database's own clock, so the test writes a row that is already old.
	setSeen(t, db, scope, time.Now().Add(-time.Minute))
	if rows := alive(t, db, scope); rows != 0 {
		t.Fatalf("%d nodes alive a minute after the last heartbeat", rows)
	}
	// And a restart is not refused for the row that went stale.
	client, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()), kv.Deps{DB: db, Scope: scope})
	if err != nil {
		t.Fatalf("a node restarted after a crash: %v", err)
	}
	if err := client.Close(t.Context()); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestNTooLowIsRefused: two live nodes against N = 1 is what the second node's start refuses; with N = 2 the second is
// fine, and each sees two nodes (README「配置与启动」).
func TestNTooLowIsRefused(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	db := openDB(t)
	scope := pg.PlatformScope()
	first := openNode(t, server, db, scope, 2)
	if got := first.Nodes(); got != 2 {
		t.Fatalf("the first node reports %d nodes, want the configured 2", got)
	}
	second := openNode(t, server, db, scope, 2)
	if got := second.Nodes(); got != 2 {
		t.Fatalf("the second node reports %d nodes", got)
	}
	if rows := alive(t, db, scope); rows != 2 {
		t.Fatalf("%d nodes alive, want 2", rows)
	}
}

// openNode opens a client that writes the heartbeat into db and closes it when the test ends.
func openNode(t *testing.T, server *kvtest.Server, db *pg.DB, scope pg.Scope, nodes int) *kv.Client {
	t.Helper()
	cfg := server.Config(kvtest.Namespace())
	cfg.Nodes = nodes
	client, err := kv.Open(t.Context(), cfg, kv.Deps{DB: db, Scope: scope})
	if err != nil {
		t.Fatalf("open the node: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := client.Close(ctx); err != nil {
			t.Errorf("close the node: %v", err)
		}
	})
	return client
}

// openDB is the test's database, with the platform schema's migrations: the heartbeat's table is the platform's.
func openDB(t *testing.T) *pg.DB {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// heartbeats is how many rows the table holds.
func heartbeats(t *testing.T, db *pg.DB, scope pg.Scope) int {
	t.Helper()
	var rows int
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM platform.nodes").Scan(&rows)
	}); err != nil {
		t.Fatalf("count the heartbeat rows: %v", err)
	}
	return rows
}

// alive is how many rows the heartbeat would count alive.
func alive(t *testing.T, db *pg.DB, scope pg.Scope) int {
	t.Helper()
	var rows int
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(),
			"SELECT count(*) FROM platform.nodes WHERE seen_at > now() - make_interval(secs => 30)").Scan(&rows)
	}); err != nil {
		t.Fatalf("count the live nodes: %v", err)
	}
	return rows
}

// setSeen moves every row's last heartbeat back by age, which is how a stopped node looks.
func setSeen(t *testing.T, db *pg.DB, scope pg.Scope, age time.Time) {
	t.Helper()
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "UPDATE platform.nodes SET seen_at = $1", age)
		return err
	}); err != nil {
		t.Fatalf("age the heartbeat rows: %v", err)
	}
}
