package pg_test

import (
	"context"
	"net"
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
	// riskyLogin creates a login whose only way out of row-level security is the group it is in, created with
	// groupOptions. Role names are cluster-wide, so they carry the database's name.
	riskyLogin := func(name, groupOptions string) string {
		t.Helper()
		name = strings.TrimPrefix(parse(t, database.App).Path, "/") + "_" + name
		if err := pgtest.Run(t.Context(), database.Superuser, "CREATE ROLE "+name+"_group "+groupOptions,
			"CREATE ROLE "+name+" LOGIN PASSWORD 'risky' IN ROLE "+name+"_group"); err != nil {
			t.Fatal(err)
		}
		login := parse(t, database.App)
		login.User = url.UserPassword(name, "risky")
		return login.String()
	}
	logins := map[string]string{
		"a superuser": database.Superuser,
		"a superuser that switched to axiom_app as it connected":   database.Superuser + "&role=axiom_app",
		"a login that runs programs on the server through a group": riskyLogin("runs", "IN ROLE pg_execute_server_program"),
		"a login that replicates through a group":                  riskyLogin("replicates", "REPLICATION"),
		"a login that creates roles through a group":               riskyLogin("creates_roles", "CREATEROLE"),
		"a login that creates databases through a group":           riskyLogin("creates_databases", "CREATEDB"),
		"a login that owns the tables through a group":             riskyLogin("owns", "IN ROLE axiom_owner"),
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

// TestConnectionFailuresAreRedacted: a failed connection, at startup or in a migration, names the variable and the
// kind of failure, not the user, database or host, any of which may hold a misplaced secret.
func TestConnectionFailuresAreRedacted(t *testing.T) {
	t.Parallel()
	refused := parse(t, pgtest.New(t).App)
	refused.User, refused.Path = url.UserPassword("planted-user", "planted-password"), "/planted-db"
	unresolved, unreachable := *refused, *refused
	unresolved.Host = "planted-host..invalid" // the empty label fails before any query: some resolvers answer every name
	unreachable.Host = freeAddr(t)
	failures := []struct {
		name string
		url  *url.URL
		want string
	}{
		{"an unknown user", refused, "the server refused the connection"},
		{"a host name that does not resolve", &unresolved, "the host name does not resolve"},
		{"no server listening", &unreachable, "cannot reach the server"},
	}
	for _, f := range failures {
		errs := map[string]error{
			"AXIOM_DATABASE_URL":         open(t, f.url.String()).Check(t.Context()),
			"AXIOM_MIGRATE_DATABASE_URL": pg.Migrate(t.Context(), f.url.String()),
		}
		for variable, err := range errs {
			if err == nil || !strings.Contains(err.Error(), variable+": "+f.want) {
				t.Errorf("%s: %v, want %s: %s", f.name, err, variable, f.want)
			} else if strings.Contains(err.Error(), "planted") || strings.Contains(err.Error(), f.url.Host) {
				t.Errorf("%s: the error %q quotes the URL", f.name, err)
			}
		}
	}
}

// freeAddr returns an address nothing listens on right now.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// TestMigrateWaitsSecondsForLocks: a run held up by another run's lock gives up within seconds instead of queueing
// a deployment behind it. The database has pending migrations: goose takes no lock when nothing is pending.
func TestMigrateWaitsSecondsForLocks(t *testing.T) {
	t.Parallel()
	locks := map[string]struct {
		take string
		args []any
		want string
	}{
		"the bootstrap lock": {"SELECT pg_advisory_xact_lock(hashtext('axiom bootstrap'))", nil, "SQLSTATE 55P03"}, // lock_timeout
		"goose's lock":       {"SELECT pg_advisory_lock($1)", []any{lock.DefaultLockID}, "failed to acquire lock"},
	}
	for name, held := range locks {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fresh := freshDatabase(t)
			hold(t, fresh.Superuser, held.take, held.args...)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if err := pg.Migrate(ctx, fresh.Migrator); err == nil || !strings.Contains(err.Error(), held.want) {
				t.Errorf("Migrate under %s: %v, want %s", name, err, held.want)
			}
		})
	}
}

// freshDatabase creates an empty database, with every migration pending, that the migration administrator may
// create schemas in.
func freshDatabase(t *testing.T) pgtest.Database {
	t.Helper()
	template := pgtest.New(t)
	name := strings.TrimPrefix(parse(t, template.Superuser).Path, "/") + "_fresh"
	if err := pgtest.Run(t.Context(), template.Superuser, "CREATE DATABASE "+name,
		"GRANT CREATE ON DATABASE "+name+" TO migrator"); err != nil {
		t.Fatal(err)
	}
	at := func(databaseURL string) string {
		u := parse(t, databaseURL)
		u.Path = "/" + name
		return u.String()
	}
	return pgtest.Database{App: at(template.App), Migrator: at(template.Migrator), Superuser: at(template.Superuser)}
}

// TestCheckAllowsANewerDatabase: in a rolling deployment the nodes still on the old version run against a database
// the new version has migrated; a database that was never migrated is refused with what to do.
func TestCheckAllowsANewerDatabase(t *testing.T) {
	t.Parallel()
	databases := map[string]struct{ change, want string }{
		"newer":          {"INSERT INTO platform.goose_platform (version_id, is_applied) VALUES (99999, true)", ""},
		"never migrated": {"DROP SCHEMA platform CASCADE", "run axiom migrate first"},
	}
	for name, c := range databases {
		database := pgtest.New(t)
		if err := pgtest.Run(t.Context(), database.Superuser, c.change); err != nil {
			t.Fatal(err)
		}
		err := open(t, database.App).Check(t.Context())
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("a %s database: %v, want %q", name, err, c.want)
		}
	}
}

// TestMigrateRunsTakeTurns: two runs started together on an empty database, as two nodes of a deployment may, wait
// for each other and both succeed. Bootstrap grants axiom_owner to the administrator, a cluster-wide change that
// concurrent runs on other databases would race on, so this test does not run in parallel.
func TestMigrateRunsTakeTurns(t *testing.T) {
	fresh := freshDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- pg.Migrate(ctx, fresh.Migrator) }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if err := open(t, fresh.App).Check(ctx); err != nil {
		t.Errorf("after both runs: %v", err)
	}
}

// TestMigrateResumesAfterAFailedRun: a run that fails partway, here on an object the migration creates, leaves what
// it finished for the next run, which continues from there. Not parallel, for the reason TestMigrateRunsTakeTurns
// gives.
func TestMigrateResumesAfterAFailedRun(t *testing.T) {
	fresh := freshDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	const obstacle = "platform.in_scope(uuid, uuid)"
	if err := pgtest.Run(ctx, fresh.Superuser, "CREATE SCHEMA platform AUTHORIZATION axiom_owner",
		"CREATE FUNCTION "+obstacle+" RETURNS boolean RETURN false"); err != nil {
		t.Fatal(err)
	}
	if err := pg.Migrate(ctx, fresh.Migrator); err == nil {
		t.Fatal("Migrate succeeded over an object it creates")
	}
	if err := pgtest.Run(ctx, fresh.Superuser, "DROP FUNCTION "+obstacle); err != nil {
		t.Fatal(err)
	}
	if err := pg.Migrate(ctx, fresh.Migrator); err != nil {
		t.Fatalf("the run after the failed one: %v", err)
	}
	if err := open(t, fresh.App).Check(ctx); err != nil {
		t.Errorf("after the second run: %v", err)
	}
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
	if err := pg.Migrate(t.Context(), database.Migrator); err == nil || !strings.Contains(err.Error(), "axiom_owner must not") {
		t.Errorf("Migrate accepted an owner with BYPASSRLS: %v", err)
	}
}
