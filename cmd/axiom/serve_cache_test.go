package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/kv/kvtest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestServeWithTheSharedCache: a node given the cache's URL and password file connects, writes its heartbeat row while it
// serves, and deletes the row on the way out, so a restart of the only node is not refused for a row its last run left
// (internal/platform/kv/README.md「配置与启动」).
func TestServeWithTheSharedCache(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	database := pgtest.New(t, modules()...)
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	nodes := func() (rows int) {
		t.Helper()
		err := db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "SELECT count(*) FROM platform.nodes").Scan(&rows)
		})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	environ := []string{
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0",
		"AXIOM_SHUTDOWN_TIMEOUT=5s", "AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'),
		"AXIOM_IDENTITY_KEY_FILE=" + keyFile(t, 'i'), "AXIOM_DATABASE_URL=" + database.App,
		"AXIOM_VALKEY_URL=" + server.URL, "AXIOM_VALKEY_PASSWORD_FILE=" + server.PasswordFile,
	}
	for run := range 2 { // the second start finds the first one's row gone
		logs := make(records, 16)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- serve(ctx, environ, slog.New(slog.NewJSONHandler(logs, nil))) }()
		servingAddrs(t, logs, done)
		if got := nodes(); got != 1 {
			t.Fatalf("run %d: %d heartbeat rows while the node serves, want 1", run, got)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run %d: shutdown: %v", run, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("run %d: the node did not shut down", run)
		}
		if got := nodes(); got != 0 {
			t.Fatalf("run %d: %d heartbeat rows after the node stopped, want 0", run, got)
		}
	}
}

// TestServeRefusesASecondNodeOfOne: a second process against the same database with the default N of 1 does not start,
// and names the variable; the first keeps serving (internal/platform/kv/README.md「配置与启动」).
func TestServeRefusesASecondNodeOfOne(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	database := pgtest.New(t, modules()...)
	environ := []string{
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0",
		"AXIOM_SHUTDOWN_TIMEOUT=5s", "AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'),
		"AXIOM_IDENTITY_KEY_FILE=" + keyFile(t, 'i'), "AXIOM_DATABASE_URL=" + database.App,
		"AXIOM_VALKEY_URL=" + server.URL, "AXIOM_VALKEY_PASSWORD_FILE=" + server.PasswordFile,
	}
	logs := make(records, 16)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := make(chan error, 1)
	go func() { first <- serve(ctx, environ, slog.New(slog.NewJSONHandler(logs, nil))) }()
	servingAddrs(t, logs, first)

	second, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()
	err := serve(second, environ, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("a second node against N = 1 started")
	}
	if !strings.Contains(err.Error(), "AXIOM_CORE_NODES") {
		t.Errorf("%q does not name AXIOM_CORE_NODES", err)
	}
	select {
	case err := <-first:
		t.Fatalf("the first node stopped when the second was refused: %v", err)
	default:
	}
	cancel()
	if err := <-first; err != nil {
		t.Errorf("the first node's shutdown: %v", err)
	}
}
