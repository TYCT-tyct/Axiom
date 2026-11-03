package pgtest

import (
	"context"
	"net/url"
	"os"
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

// TestDatabasesAreDroppedWhenTheirTestEnds: a test's copy is removed when the test ends, so a run holds only the
// copies of the tests in flight instead of one for every test that ever ran. FORCE matters when a test leaves a
// connection open, which its own cleanups do not have to close.
func TestDatabasesAreDroppedWhenTheirTestEnds(t *testing.T) {
	var name string
	var leftover *pgx.Conn
	t.Run("after the test", func(t *testing.T) {
		name = nameOf(t, New(t).Superuser)
	})
	if databaseExists(t, name) {
		t.Errorf("database %s exists after its test ended", name)
	}
	t.Run("with a connection still open", func(t *testing.T) {
		database := New(t)
		name = nameOf(t, database.Superuser)
		conn, err := pgx.Connect(t.Context(), database.Superuser)
		if err != nil {
			t.Fatal(err)
		}
		leftover = conn
	})
	if databaseExists(t, name) {
		t.Errorf("database %s exists after its test ended with a connection open", name)
	}
	if leftover != nil {
		_ = leftover.Close(context.Background()) //nolint:errcheck // The drop has already ended the session.
	}
}

// TestClonesAreDroppedWhenTheirTestEnds: a clone of a fixture follows the same rule as New; the fixture source lives
// on, its copies do not.
func TestClonesAreDroppedWhenTheirTestEnds(t *testing.T) {
	source := New(t)
	var name string
	t.Run("after the test", func(t *testing.T) {
		name = nameOf(t, Clone(t, source).Superuser)
	})
	if databaseExists(t, name) {
		t.Errorf("clone %s exists after its test ended", name)
	}
}

// TestClusterBoundsWAL: the test cluster is removed when its run ends, so the WAL it keeps serves no recovery, and a
// copy of a template writes one template's worth of WAL per test. The default gigabyte per cluster is not worth the
// disk on a host where several runs share the disk.
func TestClusterBoundsWAL(t *testing.T) {
	conn, err := pgx.Connect(t.Context(), New(t).Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.WithoutCancel(t.Context())) //nolint:errcheck // The test's report is what matters.
	for _, c := range []struct{ setting, want string }{
		{"min_wal_size", "32MB"},
		{"max_wal_size", "128MB"},
	} {
		var got string
		if err := conn.QueryRow(t.Context(), "SHOW "+c.setting).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.setting, got, c.want)
		}
	}
}

// databaseExists reports whether the cluster still has a database named name.
func databaseExists(t *testing.T, name string) bool {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), at("postgres", "postgres", "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.WithoutCancel(t.Context())) //nolint:errcheck // The query's result is what matters.
	var exists bool
	if err := conn.QueryRow(t.Context(), "SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// TestTemplatesAreBuiltUnderConcurrentBuilders: binaries of one run build templates at the same time, the same one or
// different ones. Each goroutine here stands for one binary: the mutex of templateFor is not shared between binaries,
// so they go straight to ensureTemplate. All must succeed and every template must hold its own module's migration.
func TestTemplatesAreBuiltUnderConcurrentBuilders(t *testing.T) {
	New(t) // the cluster is up
	var group errgroup.Group
	modules := map[string]pg.Module{}
	for _, module := range []string{"catalog", "billing"} {
		up := "-- +goose Up\nCREATE FUNCTION " + module + ".built() RETURNS text LANGUAGE sql RETURN 'built';\n"
		m := pg.Module{Name: module, Migrations: fstest.MapFS{"00001_built.sql": {Data: []byte(up)}}}
		modules["axiom_template_concurrent_"+module+"_"+runID] = m
		for range 3 {
			group.Go(func() error {
				return ensureTemplate(context.Background(), "axiom_template_concurrent_"+module+"_"+runID, []pg.Module{m})
			})
		}
	}
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}
	for name, m := range modules {
		conn, err := pgx.Connect(t.Context(), at("postgres", "postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		var built string
		err = conn.QueryRow(t.Context(), "SELECT "+m.Name+".built()").Scan(&built)
		if closeErr := conn.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
		if err != nil || built != "built" {
			t.Errorf("template %s: built() is %q (%v), want the migration applied", name, built, err)
		}
	}
}

func TestKeepReaperAlive(t *testing.T) {
	for _, c := range []struct{ name, disabled, legacy, current, want string }{
		{"default", "", "", "", "2m"},
		{"enabled", "false", "", "", "2m"},
		{"disabled", "true", "", "", ""},
		{"legacy override", "", "30s", "", ""},
		{"current override", "", "", "45s", "45s"},
		{"disabled override", "true", "", "45s", "45s"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("TESTCONTAINERS_RYUK_DISABLED", c.disabled)
			t.Setenv("RYUK_RECONNECTION_TIMEOUT", c.legacy)
			t.Setenv("TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT", c.current)
			if err := keepReaperAlive(); err != nil {
				t.Fatal(err)
			}
			if got := os.Getenv("TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT"); got != c.want {
				t.Fatalf("reconnection timeout = %q, want %q", got, c.want)
			}
		})
	}
}
