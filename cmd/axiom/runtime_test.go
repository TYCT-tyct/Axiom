package main

import (
	"log/slog"
	"net"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/pgtest"
)

// TestServeClaimsListenersBeforeTheDatabase: startup claims its listeners before the database check, so a taken
// address is refused with the variable's name; a check that fails with free addresses still reports the database's
// error and closes every listener it claimed.
func TestServeClaimsListenersBeforeTheDatabase(t *testing.T) {
	database := pgtest.New(t, modules()...)
	taken, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taken.Close(); err != nil {
			t.Error(err)
		}
	})
	cases := []struct {
		name, probes, want, notWant string
		claimed                     int
	}{
		{"free addresses", "127.0.0.1:0", "database:", "AXIOM_PROBES_ADDR", 3},
		{"an address that is taken", taken.Addr().String(), "AXIOM_PROBES_ADDR: ", "database:", 0},
	}
	for _, c := range cases {
		var claimed []*listening
		err := serveForTestBound(t.Context(), []string{
			"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=" + c.probes,
			"AXIOM_DATABASE_URL=" + database.Superuser,
			"AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'), "AXIOM_IDENTITY_KEY_FILE=" + keyFile(t, 'i'),
		}, slog.New(slog.DiscardHandler), func(servers []*listening) {
			claimed = servers
			t.Cleanup(func() { closeListeners(servers) })
		})
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), c.notWant) {
			t.Errorf("%s: %v, want %q without %q", c.name, err, c.want, c.notWant)
		}
		if len(claimed) != c.claimed {
			t.Fatalf("%s: claimed %d listeners, want %d", c.name, len(claimed), c.claimed)
		}
		assertListenersClosed(t, claimed)
	}
}

// TestMigrateConfiguration: the variables are checked before anything connects, so a wrong one fails at once and names
// itself; a timeout that is not positive, or a connect timeout that is not shorter than the run's, is refused.
func TestMigrateConfiguration(t *testing.T) {
	t.Parallel()
	const url = "AXIOM_MIGRATE_DATABASE_URL=postgres://planted-user:planted-password@127.0.0.1:1/planted-db" //nolint:gosec // a value the test plants to see it is never quoted
	cases := []struct {
		name    string
		environ []string
		want    string
	}{
		{"no URL", nil, "AXIOM_MIGRATE_DATABASE_URL: required"},
		{"a misspelt variable", []string{url, "AXIOM_MIGRATE_TIMOUT=1m"}, "AXIOM_MIGRATE_TIMOUT: unknown variable"},
		{"a run timeout that is not a duration", []string{url, "AXIOM_MIGRATE_TIMEOUT=soon"}, "AXIOM_MIGRATE_TIMEOUT: want a positive duration"},
		{"a connect timeout of zero", []string{url, "AXIOM_MIGRATE_CONNECT_TIMEOUT=0s"}, "AXIOM_MIGRATE_CONNECT_TIMEOUT: want a positive duration"},
		{"a connect timeout as long as the run", []string{url, "AXIOM_MIGRATE_CONNECT_TIMEOUT=1m", "AXIOM_MIGRATE_TIMEOUT=1m"}, "must be shorter than AXIOM_MIGRATE_TIMEOUT"},
		{"a run timeout shorter than the default connect timeout", []string{url, "AXIOM_MIGRATE_TIMEOUT=5s"}, "must be shorter than AXIOM_MIGRATE_TIMEOUT"},
	}
	for _, c := range cases {
		start := time.Now()
		err := migrate(t.Context(), c.environ)
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "planted") || time.Since(start) > 2*time.Second {
			t.Errorf("%s: %v after %v, want %q at once, quoting no value", c.name, err, time.Since(start), c.want)
		}
	}
}

// TestMigrateRuns: with valid settings migrate brings the database up and, as the database is already current, finds
// nothing to do, and one against a server that never answers ends within the connect timeout.
func TestMigrateRuns(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t, modules()...)
	if err := migrate(t.Context(), []string{"AXIOM_MIGRATE_DATABASE_URL=" + database.Migrator, "AXIOM_MIGRATE_TIMEOUT=2m", "AXIOM_MIGRATE_CONNECT_TIMEOUT=5s"}); err != nil {
		t.Fatal(err)
	}
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
	err = migrate(t.Context(), []string{"AXIOM_MIGRATE_DATABASE_URL=postgres://u:p@" + silent.Addr().String() + "/d", "AXIOM_MIGRATE_TIMEOUT=20s", "AXIOM_MIGRATE_CONNECT_TIMEOUT=1s"})
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("migrate against a server that never answers: %v after %v, want an error soon after 1s", err, time.Since(start))
	}
}

// TestProgramCarriesRootCertificates: the image is scratch, with no certificate store, so the program brings its own
// roots for outbound HTTPS; Go uses them only when the system has none. They are a dependency, so the build info shows
// them.
func TestProgramCarriesRootCertificates(t *testing.T) {
	t.Parallel()
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build info")
	}
	for _, dep := range info.Deps {
		if dep.Path == "golang.org/x/crypto/x509roots/fallback" {
			return
		}
	}
	t.Error("the fallback root certificates are not linked into the program")
}
