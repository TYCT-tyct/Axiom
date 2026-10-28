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

// TestClusterURLIn: withcluster hands the run's cluster to every test binary by appending ClusterEnv to the
// environment, so the last value is the run's; without one, the binary starts its own cluster.
func TestClusterURLIn(t *testing.T) {
	env := []string{"PATH=/bin", ClusterEnv + "=first", "HOME=/home", ClusterEnv + "=second"}
	if got := clusterURLIn(env); got != "second" {
		t.Errorf("clusterURLIn = %q, want the last value in %s", got, ClusterEnv)
	}
	if got := clusterURLIn([]string{"PATH=/bin"}); got != "" {
		t.Errorf("clusterURLIn without %s = %q, want empty", ClusterEnv, got)
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
