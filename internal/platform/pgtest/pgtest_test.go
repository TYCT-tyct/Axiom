package pgtest

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"axiom/internal/platform/pg"
)

// TestDockerHostFor: on Windows without DOCKER_HOST the pipe is named, so testcontainers does not have to find it by
// checking that the path exists, which fails while the pipe is busy; whatever the developer set is kept, and other
// systems are left to testcontainers.
func TestDockerHostFor(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ goos, current, want string }{
		{"windows", "", windowsDockerHost},
		{"windows", "tcp://remote:2375", "tcp://remote:2375"},
		{"linux", "", ""},
		{"darwin", "unix:///x.sock", "unix:///x.sock"},
	} {
		if got := dockerHostFor(c.goos, c.current); got != c.want {
			t.Errorf("%s with %q: %q, want %q", c.goos, c.current, got, c.want)
		}
	}
}

func databaseExists(t *testing.T, name string) bool {
	t.Helper()
	var exists bool
	conn, err := pgx.Connect(t.Context(), at("postgres", "postgres", "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.WithoutCancel(t.Context())) //nolint:errcheck // read-only
	if err := conn.QueryRow(t.Context(), "SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func nameOf(t *testing.T, databaseURL string) string {
	t.Helper()
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(u.Path, "/")
}

// TestDatabasesAreSeparate: each test has a database of its own, whatever else shares the cluster.
func TestDatabasesAreSeparate(t *testing.T) {
	first, second := New(t), New(t)
	if nameOf(t, first.Superuser) == nameOf(t, second.Superuser) {
		t.Fatal("two tests got the same database")
	}
	if err := Run(t.Context(), first.Superuser, "CREATE TABLE only_here (n int)"); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), second.Superuser, "INSERT INTO only_here VALUES (1)"); err == nil {
		t.Error("a table made in one test's database is visible in another's")
	}
}

// TestTemplateIsBuiltOnceUnderConcurrentBuilders: binaries of one run build the same template at the same time. Each
// goroutine here stands for one: the mutex of templateFor is not shared between binaries, so they go straight to
// ensureTemplate. All must succeed and the result must hold the migration.
func TestTemplateIsBuiltOnceUnderConcurrentBuilders(t *testing.T) {
	const up = "-- +goose Up\nCREATE FUNCTION catalog.built() RETURNS text LANGUAGE sql RETURN 'built';\n"
	module := pg.Module{Name: "catalog", Migrations: fstest.MapFS{"00001_built.sql": {Data: []byte(up)}}}
	name := "axiom_template_concurrent_" + runID
	New(t) // the cluster is up
	var group errgroup.Group
	for range 6 {
		group.Go(func() error { return ensureTemplate(context.Background(), name, []pg.Module{module}) })
	}
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}
	var comment string
	conn, err := pgx.Connect(t.Context(), at("postgres", "postgres", name))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.WithoutCancel(t.Context())) //nolint:errcheck // read-only
	if err := conn.QueryRow(t.Context(), "SELECT catalog.built()").Scan(&comment); err != nil || comment != "built" {
		t.Errorf("template's catalog.built() is %q (%v), want the migration applied", comment, err)
	}
	if databaseExists(t, "axiom_template_building") {
		t.Error("the copy being built was left behind")
	}
}
