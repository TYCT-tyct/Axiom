package kv_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
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

// openBare opens a node without Valkey, the way a single-core deployment runs, and closes it when the test ends.
func openBare(t *testing.T, db *pg.DB, scope pg.Scope) (*kv.Client, error) {
	t.Helper()
	client, err := kv.Open(t.Context(), kv.Config{Nodes: 1}, kv.Deps{DB: db, Scope: scope})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { closeQuietly(t, client) })
	return client, nil
}

// TestNodesWithoutValkeyAreCounted: a node without Valkey registers like any other, so two nodes of N = 1 that both have
// no Valkey, or one that has it and one that has not, cannot both serve; and its row goes when it stops, so a restart is
// not refused (README「配置与启动」「节点心跳」).
func TestNodesWithoutValkeyAreCounted(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	db := openDB(t)
	scope := pg.PlatformScope()
	first, err := openBare(t, db, scope)
	if err != nil {
		t.Fatalf("the first node without Valkey: %v", err)
	}
	if rows := heartbeats(t, db, scope); rows != 1 {
		t.Fatalf("%d rows for one node without Valkey, want 1", rows)
	}
	if _, err := openBare(t, db, scope); err == nil || !strings.Contains(err.Error(), kv.NodesVar) {
		t.Fatalf("a second node without Valkey: %v, want a refusal naming %s", err, kv.NodesVar)
	}
	if _, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()), kv.Deps{DB: db, Scope: scope}); err == nil {
		t.Fatal("a node with Valkey started beside one without")
	}
	if err := first.Close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if rows := heartbeats(t, db, scope); rows != 0 {
		t.Fatalf("%d rows after the node stopped, want 0", rows)
	}
	if _, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()), kv.Deps{DB: db, Scope: scope}); err != nil {
		t.Fatalf("a node with Valkey after the one without stopped: %v", err)
	}
}

// TestNodesStartingTogetherAreOneAtATime: nodes of N = 1 that start at the same moment are let in one by one, because the
// count of the others and the write of the node's own row are one step under a lock: with the count taken apart from the
// write, each would find nobody and every one of them would be let in.
func TestNodesStartingTogetherAreOneAtATime(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	scope := pg.PlatformScope()
	const starting = 8
	begin := make(chan struct{})
	var group sync.WaitGroup
	var started atomic.Int32
	for range starting {
		group.Go(func() {
			<-begin
			client, err := kv.Open(t.Context(), kv.Config{Nodes: 1}, kv.Deps{DB: db, Scope: scope})
			if err != nil {
				return
			}
			started.Add(1)
			t.Cleanup(func() { closeQuietly(t, client) })
		})
	}
	close(begin)
	group.Wait()
	if got := started.Load(); got != 1 {
		t.Fatalf("%d of %d nodes started together against N = 1, want 1", got, starting)
	}
	if rows := heartbeats(t, db, scope); rows != 1 {
		t.Fatalf("%d heartbeat rows, want 1", rows)
	}
}

// TestAStuckDatabaseDoesNotStopTheChecksOfValkey: a heartbeat that waits on a lock the database holds gives up at its own
// deadline, logs it, and the round has already read the server's memory: the checks of Valkey go on whatever the database
// does (CONTRIBUTING.md「检查清单」「可靠」).
func TestAStuckDatabaseDoesNotStopTheChecksOfValkey(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	db := openDB(t)
	scope := pg.PlatformScope()
	seen, logs := newReadings(), &lockedBuffer{}
	cfg := server.Config(kvtest.Namespace())
	client, err := kv.Open(t.Context(), cfg, kv.Deps{
		DB: db, Scope: scope, Meter: seen.meter, Logger: slog.New(slog.NewTextHandler(logs, nil)), HeartbeatTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { closeQuietly(t, client) })

	locked, release := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- db.Tx(t.Context(), scope, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "LOCK TABLE platform.nodes IN ACCESS EXCLUSIVE MODE"); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-held:
		t.Fatalf("take the lock: %v", err)
	}
	defer func() {
		close(release)
		if err := <-held; err != nil {
			t.Errorf("release the lock: %v", err)
		}
	}()
	round := make(chan struct{})
	go func() {
		defer close(round)
		client.Beat(t.Context())
	}()
	select {
	case <-round:
	case <-time.After(time.Minute): // only catches a round that never ends; the heartbeat's own deadline is 200 ms
		t.Fatal("a round of the loop never ended: the heartbeat waited on the database for ever")
	}
	if got := seen.memory(t); got <= 0 {
		t.Fatalf("memory_used_bytes = %d: the round did not read the server", got)
	}
	if !strings.Contains(logs.String(), "heartbeat failed") {
		t.Fatalf("the heartbeat that gave up was not reported: %s", logs.String())
	}
}

// TestARestartedNodeReplacesItsOwnRow: a node that crashed left its row, and the same node started again, under the same
// identity, takes the row over at once and is the one node of N = 1; a node under another identity is another node, and
// is refused while the first is alive. Without that a crash would keep the node out for half a minute (README「节点心跳」).
func TestARestartedNodeReplacesItsOwnRow(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	scope := pg.PlatformScope()
	open := func(id string) (*kv.Client, error) {
		return kv.Open(t.Context(), kv.Config{Nodes: 1, NodeID: id}, kv.Deps{DB: db, Scope: scope})
	}
	crashed, err := open("node-a")
	if err != nil {
		t.Fatalf("the first start: %v", err)
	}
	// The crashed process never deletes its row: its loop is left running here, and its row is as fresh as a crash leaves it.
	t.Cleanup(func() { closeQuietly(t, crashed) })
	restarted, err := open("node-a")
	if err != nil {
		t.Fatalf("the same node started again: %v", err)
	}
	t.Cleanup(func() { closeQuietly(t, restarted) })
	if rows := heartbeats(t, db, scope); rows != 1 {
		t.Fatalf("%d rows for one node that restarted, want 1", rows)
	}
	if _, err := open("node-b"); err == nil || !strings.Contains(err.Error(), kv.NodesVar) {
		t.Fatalf("another node beside the first under N = 1: %v, want a refusal naming %s", err, kv.NodesVar)
	}
}
