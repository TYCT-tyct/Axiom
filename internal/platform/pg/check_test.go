package pg_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3/lock"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// parse splits a database URL.
func parse(t *testing.T, databaseURL string) *url.URL {
	t.Helper()
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestCheckRefusesLoginsThatEscape: each login is refused although the role it acts as may be harmless, because
// the login itself can escape row-level security.
func TestCheckRefusesLoginsThatEscape(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t)
	// Role names are cluster-wide, so they carry the database's name.
	risky := strings.TrimPrefix(parse(t, database.App).Path, "/") + "_risky"
	if err := pgtest.Run(t.Context(), database.Superuser,
		"CREATE ROLE "+risky+"_group IN ROLE pg_execute_server_program",
		"CREATE ROLE "+risky+" LOGIN PASSWORD 'risky' IN ROLE "+risky+"_group"); err != nil {
		t.Fatal(err)
	}
	riskyURL := parse(t, database.App)
	riskyURL.User = url.UserPassword(risky, "risky")
	logins := map[string]string{
		"a superuser": database.Superuser,
		"a superuser that switched to axiom_app as it connected":   database.Superuser + "&role=axiom_app",
		"a login that runs programs on the server through a group": riskyURL.String(),
	}
	for name, login := range logins {
		db := open(t, login)
		var current string
		if err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "SELECT current_user").Scan(&current)
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "row-level security cannot hold") {
			t.Errorf("%s, acting as %s: %v", name, current, err)
		}
	}
}

// TestConnectionFailuresAreRedacted: a failed connection, at startup or in a migration, names the variable, not
// the user, database or host, any of which may hold a misplaced secret.
func TestConnectionFailuresAreRedacted(t *testing.T) {
	t.Parallel()
	planted := parse(t, pgtest.New(t).App)
	planted.User, planted.Path = url.UserPassword("planted-user", "planted-password"), "/planted-db"
	failures := map[string]error{
		"AXIOM_DATABASE_URL":         open(t, planted.String()).Check(t.Context()),
		"AXIOM_MIGRATE_DATABASE_URL": pg.Migrate(t.Context(), planted.String()),
	}
	for variable, err := range failures {
		if err == nil || !strings.Contains(err.Error(), variable) {
			t.Errorf("an unknown user: %v, want an error naming %s", err, variable)
			continue
		}
		for _, part := range []string{"planted-user", "planted-password", "planted-db", planted.Host} {
			if strings.Contains(err.Error(), part) {
				t.Errorf("the error %q quotes %q", err, part)
			}
		}
	}
}

// TestMigrateWaitsSecondsForLocks: a run held up by another run's lock gives up within seconds instead of queueing
// a deployment behind it. The database has pending migrations: goose takes no lock when nothing is pending.
func TestMigrateWaitsSecondsForLocks(t *testing.T) {
	t.Parallel()
	locks := map[string]struct {
		take string
		args []any
	}{
		"the bootstrap lock": {"SELECT pg_advisory_xact_lock(hashtext('axiom bootstrap'))", nil},
		"goose's lock":       {"SELECT pg_advisory_lock($1)", []any{lock.DefaultLockID}},
	}
	for name, held := range locks {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fresh := freshDatabase(t)
			hold(t, fresh, held.take, held.args...)
			start := time.Now()
			if err := pg.Migrate(t.Context(), fresh); err == nil || time.Since(start) > 15*time.Second {
				t.Errorf("Migrate under %s: %v after %v", name, err, time.Since(start))
			}
		})
	}
}

// freshDatabase creates an empty database, with every migration pending, and returns the superuser's URL to it.
func freshDatabase(t *testing.T) string {
	t.Helper()
	fresh := parse(t, pgtest.New(t).Superuser)
	name := strings.TrimPrefix(fresh.Path, "/") + "_fresh"
	if err := pgtest.Run(t.Context(), fresh.String(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	fresh.Path = "/" + name
	return fresh.String()
}

// hold takes a lock in a transaction that stays open until the test ends.
func hold(t *testing.T, databaseURL, take string, args ...any) {
	t.Helper()
	holder, err := pgx.Connect(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := holder.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	if _, err := holder.Exec(t.Context(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(t.Context(), take, args...); err != nil {
		t.Fatal(err)
	}
}

// TestMigrateRefusesAnUnsafeOwner: an existing axiom_owner that bypasses row-level security stops the run. It
// alters a cluster-wide role, so it does not run in parallel: Go finishes it before any parallel test resumes.
func TestMigrateRefusesAnUnsafeOwner(t *testing.T) {
	database := pgtest.New(t)
	if err := pgtest.Run(t.Context(), database.Superuser, "ALTER ROLE axiom_owner BYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pgtest.Run(context.WithoutCancel(t.Context()), database.Superuser, "ALTER ROLE axiom_owner NOBYPASSRLS"); err != nil {
			t.Error(err)
		}
	})
	if err := pg.Migrate(t.Context(), database.Superuser); err == nil || !strings.Contains(err.Error(), "axiom_owner must not") {
		t.Errorf("Migrate accepted an owner with BYPASSRLS: %v", err)
	}
}
