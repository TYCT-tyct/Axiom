package kv_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/telemetry/telemetrytest"
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
	_, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()), kvtest.Deps(kv.Deps{DB: db, Scope: scope}))
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

// TestHeartbeatGoesStale: a node that stops writing its row is gone after the heartbeat's staleness bound, so a crashed
// node that comes back under a new name (the host could not say its own) waits at most that long (README「节点心跳」).
func TestHeartbeatGoesStale(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	db := openDB(t)
	scope := pg.PlatformScope()
	crashed := openNode(t, server, db, scope, 1)
	// The crashed node writes no more: its loop and connections are gone, and its row stays.
	if err := crashed.Crash(t.Context()); err != nil {
		t.Fatalf("crash: %v", err)
	}
	// Aging the row is the database's own clock, so the test writes a row that is already old.
	setSeen(t, db, scope, time.Now().Add(-time.Minute))
	if rows := alive(t, db, scope); rows != 0 {
		t.Fatalf("%d nodes alive a minute after the last heartbeat", rows)
	}
	// And a restart is not refused for the row that went stale.
	client, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()), kvtest.Deps(kv.Deps{DB: db, Scope: scope}))
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
	client, err := kv.Open(t.Context(), cfg, kvtest.Deps(kv.Deps{DB: db, Scope: scope}))
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
	if _, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()), kvtest.Deps(kv.Deps{DB: db, Scope: scope})); err == nil {
		t.Fatal("a node with Valkey started beside one without")
	}
	if err := first.Close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if rows := heartbeats(t, db, scope); rows != 0 {
		t.Fatalf("%d rows after the node stopped, want 0", rows)
	}
	if _, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()), kvtest.Deps(kv.Deps{DB: db, Scope: scope})); err != nil {
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
	seen, logs := newReadings(), &telemetrytest.Buffer{}
	cfg := server.Config(kvtest.Namespace())
	client, err := kv.Open(t.Context(), cfg, kvtest.Deps(kv.Deps{
		DB: db, Scope: scope, Meter: seen.meter, Logger: slog.New(slog.NewTextHandler(logs, nil)), HeartbeatTimeout: 200 * time.Millisecond,
	}))
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

// TestAProcessUnderTheNameOfALiveNodeIsRefused: the name of a node belongs to one process at a time, so a second process
// started with the same name (the default is the host name, the same on a host that runs two) is refused while the first
// is alive, whatever the first's row says, and a process with another name is another node, refused under N = 1
// (README「节点心跳」).
func TestAProcessUnderTheNameOfALiveNodeIsRefused(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	scope := pg.PlatformScope()
	first, err := openNamed(t, db, scope, "node-a")
	if err != nil {
		t.Fatalf("the first process: %v", err)
	}
	if _, err := openNamed(t, db, scope, "node-a"); err == nil || !strings.Contains(err.Error(), kv.NodeIDVar) {
		t.Fatalf("a second live process under the same name: %v, want a refusal naming %s", err, kv.NodeIDVar)
	}
	if _, err := openNamed(t, db, scope, "node-b"); err == nil || !strings.Contains(err.Error(), kv.NodesVar) {
		t.Fatalf("a process under another name against N = 1: %v, want a refusal naming %s", err, kv.NodesVar)
	}
	select {
	case <-first.Lost():
		t.Fatal("the first process lost its name because others were refused")
	default:
	}
	if rows := heartbeats(t, db, scope); rows != 1 {
		t.Fatalf("%d rows after the refusals, want 1", rows)
	}
}

// TestACrashedNodeIsRestartedAtOnce: a process that crashed took its connection with it, so its name is free and its
// restart is let in with no wait for the row to expire; the row it left is its own and is replaced, and the node is the
// one node of N = 1 (README「节点心跳」). The crashed client's loops and connections are stopped, as a crash stops them.
func TestACrashedNodeIsRestartedAtOnce(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	scope := pg.PlatformScope()
	crashed, err := openNamed(t, db, scope, "node-a")
	if err != nil {
		t.Fatalf("the first process: %v", err)
	}
	if err := crashed.Crash(t.Context()); err != nil {
		t.Fatalf("crash: %v", err)
	}
	if rows := heartbeats(t, db, scope); rows != 1 {
		t.Fatalf("%d rows after the crash, want the one it left", rows)
	}
	// The server frees the name when it sees the connection go; the test waits for that, not for a row to expire.
	deadline := time.Now().Add(30 * time.Second)
	var restarted *kv.Client
	for restarted == nil {
		if restarted, err = openNamed(t, db, scope, "node-a"); err != nil {
			if time.Now().After(deadline) || !strings.Contains(err.Error(), kv.NodeIDVar) {
				t.Fatalf("the restart of a crashed node: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if rows := heartbeats(t, db, scope); rows != 1 {
		t.Fatalf("%d rows after the restart, want 1", rows)
	}
	if err := restarted.Close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// openNamed opens a node without Valkey under a name, and closes it when the test ends.
func openNamed(t *testing.T, db *pg.DB, scope pg.Scope, name string) (*kv.Client, error) {
	t.Helper()
	client, err := kv.Open(t.Context(), kv.Config{Nodes: 1, NodeID: name}, kv.Deps{DB: db, Scope: scope})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { closeQuietly(t, client) })
	return client, nil
}

// TestHeartbeatReportsTheKeyRing: each beat carries the versions this node has loaded and the one it writes, and the
// time the write version became current changes only when that version changes. The lifecycle reads those columns to
// activate and retire without stopping writers; only identifiers are stored.
func TestHeartbeatReportsTheKeyRing(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	db := openDB(t)
	scope := pg.PlatformScope()
	var versions atomic.Value
	var active atomic.Uint32
	versions.Store([]uint32{1})
	active.Store(1)
	cfg := server.Config(kvtest.Namespace())
	cfg.Nodes = 1
	cfg.Keys = func() kv.KeyReport {
		return kv.KeyReport{Versions: versions.Load().([]uint32), Active: active.Load()}
	}
	client, err := kv.Open(t.Context(), cfg, kvtest.Deps(kv.Deps{DB: db, Scope: scope}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(t, client) })

	read := func() ([]int64, int64, time.Time) {
		t.Helper()
		var got []int64
		var version int64
		var since time.Time
		if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(),
				"SELECT key_versions, key_active, key_active_since FROM platform.nodes").Scan(&got, &version, &since)
		}); err != nil {
			t.Fatal(err)
		}
		return got, version, since
	}
	before, version, since := read()
	if len(before) != 1 || before[0] != 1 || version != 1 {
		t.Fatalf("the first report is %v/%d, want [1]/1", before, version)
	}
	// A reload adds versions; the write version has not moved, so the quiet window keeps its start.
	versions.Store([]uint32{1, 2})
	client.Beat(t.Context())
	after, version, sameSince := read()
	if len(after) != 2 || version != 1 || !sameSince.Equal(since) {
		t.Fatalf("a report without an active change moved the quiet start: %v/%d since %v, before %v", after, version, sameSince, since)
	}
	// Activation moves the write version, which restarts the quiet window from this beat.
	active.Store(2)
	client.Beat(t.Context())
	after, version, moved := read()
	if len(after) != 2 || version != 2 || !moved.After(since) {
		t.Fatalf("activation did not move the write version or its quiet start: %v/%d since %v", after, version, moved)
	}
}

// TestAReusedNodeIDFromAnOldBinaryDoesNotSupportThePreference: an old binary can start under a new node's ID and
// update share_session without knowing the capability columns. The old row's true must not survive that process
// change; only a declaration whose session is the live session counts.
func TestAReusedNodeIDFromAnOldBinaryDoesNotSupportThePreference(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	scope := pg.PlatformScope()
	node, err := openNamed(t, db, scope, "node-a")
	if err != nil {
		t.Fatalf("open the new node: %v", err)
	}
	var shareSession, declaredSession uuid.UUID
	var supported, ready bool
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT share_session, billing_preference_session,
   supports_billing_preference FROM platform.nodes WHERE id = 'node-a'`).Scan(&shareSession, &declaredSession, &supported); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT enabled FROM platform.billing_preference_readiness WHERE id`).Scan(&ready)
	}); err != nil {
		t.Fatalf("read the new node: %v", err)
	}
	if !supported || !ready || shareSession != declaredSession {
		t.Fatalf("new node: supported=%v ready=%v session=%s declared=%s, want true/true/equal", supported, ready, shareSession, declaredSession)
	}

	// The old binary's start: it updates share_session and leaves both capability columns alone.
	oldSession := uuid.NewV7()
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO platform.nodes (id, share_session, share_view, key_versions, key_active)
   VALUES ($1, $2, '{}', '{}', 0)
   ON CONFLICT (id) DO UPDATE SET seen_at = now(), share_session = excluded.share_session, share_view = '{}',
    share_joining = true, key_versions = excluded.key_versions, key_active = excluded.key_active`, "node-a", oldSession)
		return err
	}); err != nil {
		t.Fatalf("register the old binary: %v", err)
	}
	// Its old heartbeat updates the live row without a capability declaration.
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE platform.nodes SET seen_at = now(), share_session = $2,
   share_view = '{}', key_versions = '{}', key_active = 0 WHERE id = $1`, "node-a", oldSession)
		return err
	}); err != nil {
		t.Fatalf("old heartbeat: %v", err)
	}
	node.Beat(t.Context())
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT enabled FROM platform.billing_preference_readiness WHERE id`).Scan(&ready)
	}); err != nil {
		t.Fatalf("read readiness: %v", err)
	}
	if ready {
		t.Fatal("readiness stayed true across an old binary's reuse of the node ID")
	}
}
