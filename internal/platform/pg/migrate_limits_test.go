package pg_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// up is the line every migration starts with, and a line end.
const up = `-- +goose Up
`

// TestMigrateEndsAtTheDeadline: a migration that outlasts the context's deadline is cancelled on the server and the run
// ends about then, instead of holding goose's lock for as long as the DDL takes. Migrate grants roles cluster-wide, so
// this test does not run in parallel (see TestMigrateRunsTakeTurns).
func TestMigrateEndsAtTheDeadline(t *testing.T) {
	database := pgtest.New(t)
	slow := pg.Module{Name: "quality", Migrations: fstest.MapFS{"00001_slow.sql": {Data: []byte(up + "SELECT pg_sleep(120);")}}}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := pg.Migrate(ctx, database.Migrator, slow)
	if err == nil || time.Since(start) > 20*time.Second {
		t.Fatalf("after %v: %v, want an error soon after the 2s deadline", time.Since(start), err)
	}
	if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "canceling") {
		t.Errorf("the error does not say why: %v", err)
	}
}

// TestMigrateConnectTimeout: a server that accepts the connection and never answers fails the run within the connect
// timeout, not the operating system's, and the error names no part of the URL. A timeout that is not positive is refused
// before any connection.
func TestMigrateConnectTimeout(t *testing.T) {
	silent, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := silent.Close(); err != nil {
			t.Error(err)
		}
	})
	start := time.Now()
	err = pg.MigrateWith(t.Context(), "postgres://planted-user:planted-password@"+silent.Addr().String()+"/planted-db", time.Second)
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("after %v: %v, want an error soon after the 1s connect timeout", time.Since(start), err)
	}
	if strings.Contains(err.Error(), "planted") || strings.Contains(err.Error(), silent.Addr().String()) {
		t.Errorf("the error quotes the URL: %v", err)
	}
	if err := pg.MigrateWith(t.Context(), "postgres://x@127.0.0.1:1/x", 0); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Errorf("a zero connect timeout: %v", err)
	}
}

// TestLatestVersionNeedsMigrations: a module with no migrations says so, in words, not through a nil error.
func TestLatestVersionNeedsMigrations(t *testing.T) {
	t.Parallel()
	_, err := pg.LatestVersion(pg.Module{Name: "quality", Migrations: fstest.MapFS{}})
	if err == nil || err.Error() != "no migrations of quality" {
		t.Errorf("%v", err)
	}
	version, err := pg.LatestVersion(pg.Module{Name: "quality", Migrations: fstest.MapFS{
		"00001_a.sql": {Data: []byte(up)}, "00002_b.sql": {Data: []byte(up)},
	}})
	if err != nil || version != 2 {
		t.Errorf("%d, %v", version, err)
	}
}
