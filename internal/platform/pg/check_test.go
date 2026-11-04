package pg_test

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
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
		"a login that looks up any tenant through a group":         riskyLogin("looks_up", "IN ROLE axiom_lookup"),
		"a login that reads server files through a group":          riskyLogin("reads_files", "IN ROLE pg_read_server_files"),
		"a login that writes server files through a group":         riskyLogin("writes_files", "IN ROLE pg_write_server_files"),
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

// TestCheckRefusesAJobRoleGrantedToTheApplicationLogin: a module's job role owns the tables its job must alter, so a
// login that can become it could read past row-level security or drop them. The role need not own anything yet: the
// declared JobRole alone is refused. Migrate creates roles cluster-wide, so this test does not run in parallel (see
// TestMigrateRunsTakeTurns).
func TestCheckRefusesAJobRoleGrantedToTheApplicationLogin(t *testing.T) {
	for name, migration := range map[string]string{
		"that owns nothing": "-- +goose Up\nSELECT 1;\n",
		"that owns a table": `-- +goose Up
GRANT USAGE, CREATE ON SCHEMA observability TO axiom_retention_job;
CREATE TABLE observability.call_log (id int);
ALTER TABLE observability.call_log ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON observability.call_log USING ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT ON observability.call_log TO axiom_app;
ALTER TABLE observability.call_log OWNER TO axiom_retention_job;
`,
	} {
		t.Run(name, func(t *testing.T) { checkRefusesAJobRole(t, migration) })
	}
}

// checkRefusesAJobRole migrates the observability module, checks that a login inheriting axiom_app passes, grants the
// job role to it and expects the audit to name the role.
func checkRefusesAJobRole(t *testing.T, migration string) {
	t.Helper()
	database := pgtest.New(t)
	module := pg.Module{Name: "observability", JobRole: "axiom_retention_job", Migrations: fstest.MapFS{
		"00001_call_log.sql": {Data: []byte(migration)},
	}}
	if err := pg.Migrate(t.Context(), database.Migrator, module); err != nil {
		t.Fatal(err)
	}
	loginName, login := appLogin(t, database)
	db := open(t, login)
	if err := db.Check(t.Context(), module); err != nil {
		t.Fatalf("before the job role was granted: %v", err)
	}
	if err := pgtest.Run(t.Context(), database.Superuser, "GRANT axiom_retention_job TO "+loginName); err != nil {
		t.Fatal(err)
	}
	err := db.Check(t.Context(), module)
	if err == nil || !strings.Contains(err.Error(), "row-level security cannot hold") ||
		!strings.Contains(err.Error(), "axiom_retention_job") {
		t.Fatalf("with the job role granted, Check = %v, want the role audit naming axiom_retention_job", err)
	}
	if strings.Contains(err.Error(), "misgranted") {
		t.Errorf("the error quotes the login's password: %v", err)
	}
}

// TestCheckRefusesATableOwnerThatIsNotAJobRole: table ownership is refused by itself, not only through the declared
// job roles, so a role an administrator hands ownership to cannot read past or drop the table. Migrate creates roles
// cluster-wide, so this test does not run in parallel.
func TestCheckRefusesATableOwnerThatIsNotAJobRole(t *testing.T) {
	database := pgtest.New(t)
	module := pg.Module{Name: "quality", Migrations: fstest.MapFS{
		"00001_items.sql": {Data: []byte(`-- +goose Up
CREATE TABLE quality.items (id int);
ALTER TABLE quality.items ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON quality.items USING ((SELECT platform.in_platform_scope()));
GRANT SELECT ON quality.items TO axiom_app;
`)},
	}}
	if err := pg.Migrate(t.Context(), database.Migrator, module); err != nil {
		t.Fatal(err)
	}
	loginName, login := appLogin(t, database)
	owner := loginName + "_owner"
	if err := pgtest.Run(t.Context(), database.Superuser,
		"CREATE ROLE "+owner,
		"ALTER TABLE quality.items OWNER TO "+owner,
		"GRANT "+owner+" TO "+loginName); err != nil {
		t.Fatal(err)
	}
	err := open(t, login).Check(t.Context(), module)
	if err == nil || !strings.Contains(err.Error(), "row-level security cannot hold") || !strings.Contains(err.Error(), owner) {
		t.Fatalf("with the table's ownership granted, Check = %v, want the role audit naming %s", err, owner)
	}
}

// appLogin creates a login that inherits the application role, and returns its name and URL. The login is not
// axiom_app itself, so a test can grant it a dangerous role without changing what every other test connects as. Role
// names are cluster-wide, so the name carries the database's.
func appLogin(t *testing.T, database pgtest.Database) (string, string) {
	t.Helper()
	name := strings.TrimPrefix(parse(t, database.App).Path, "/") + "_app"
	if err := pgtest.Run(t.Context(), database.Superuser,
		"CREATE ROLE "+name+" LOGIN PASSWORD 'misgranted'",
		"GRANT axiom_app TO "+name); err != nil {
		t.Fatal(err)
	}
	login := parse(t, database.App)
	login.User = url.UserPassword(name, "misgranted")
	return name, login.String()
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
			// The run gives up by itself within seconds; this only stops a hang. It is generous because on a loaded machine the
			// bootstrap and the schemas can take longer than the lock wait, and a run cut off by this deadline reports a failed
			// connection, not the lock.
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
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
		"GRANT CREATE ON DATABASE "+name+" TO axiom_migrator"); err != nil {
		t.Fatal(err)
	}
	at := func(databaseURL string) string {
		u := parse(t, databaseURL)
		u.Path = "/" + name
		return u.String()
	}
	return pgtest.Database{App: at(template.App), Retention: at(template.Retention), Migrator: at(template.Migrator), Superuser: at(template.Superuser)}
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

// TestMigrateRefusesUnsafeRoles: an existing axiom_owner or axiom_lookup that row-level security cannot hold, or
// that can log in, stops the run, and a node refuses a lookup role changed after it. Each alters a cluster-wide role,
// so the test does not run in parallel: Go finishes it before any parallel test resumes.
func TestMigrateRefusesUnsafeRoles(t *testing.T) {
	database := pgtest.New(t)
	for _, role := range []struct{ name, unsafe, safe, check string }{
		{"axiom_owner", "BYPASSRLS", "NOBYPASSRLS", ""},
		{"axiom_lookup", "LOGIN", "NOLOGIN", "axiom_lookup breaks its limits: it can log in"},
	} {
		t.Run(role.name, func(t *testing.T) {
			alterRole(t, database.Superuser, role.name, role.unsafe, role.safe)
			if err := pg.Migrate(t.Context(), database.Migrator); err == nil || !strings.Contains(err.Error(), role.name+" must not") {
				t.Errorf("Migrate accepted %s with %s: %v", role.name, role.unsafe, err)
			}
			if err := open(t, database.App).Check(t.Context()); role.check != "" && (err == nil || !strings.Contains(err.Error(), role.check)) {
				t.Errorf("Check accepted %s with %s: %v", role.name, role.unsafe, err)
			}
		})
	}
}

// alterRole changes a cluster-wide role until the test ends.
func alterRole(t *testing.T, superuser, role, change, undo string) {
	t.Helper()
	if err := pgtest.Run(t.Context(), superuser, "ALTER ROLE "+role+" "+change); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pgtest.Run(context.WithoutCancel(t.Context()), superuser, "ALTER ROLE "+role+" "+undo); err != nil {
			t.Error(err)
		}
	})
}

// Check includes both USING and WITH CHECK, so a forgotten write policy cannot retain per-row scope parsing.
func TestCheckRefusesLegacyScopePolicies(t *testing.T) {
	t.Parallel()
	for _, clause := range []string{"USING", "WITH CHECK"} {
		database := pgtest.New(t)
		statement := "ALTER POLICY tenant ON platform.audit_log " + clause + " (platform.in_scope(site_id, account_id))"
		if err := pgtest.Run(t.Context(), database.Superuser, statement); err != nil {
			t.Fatal(err)
		}
		err := open(t, database.App).Check(t.Context())
		if err == nil || !strings.Contains(err.Error(), "per-row scope readers") || !strings.Contains(err.Error(), "platform.audit_log.tenant") {
			t.Errorf("Check accepted legacy %s: %v", clause, err)
		}
	}
}
