package pg_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestModuleOrderFollowsArchitecture: migrations run in the order of the dependency table, which is the one list of
// modules; a module added there and not here could not bring migrations, one added here only would be unchecked.
func TestModuleOrderFollowsArchitecture(t *testing.T) {
	t.Parallel()
	doc, err := os.ReadFile("../../../docs/architecture.md")
	if err != nil {
		t.Fatal(err)
	}
	_, table, found := strings.Cut(string(doc), "\n### 模块之间的依赖\n")
	if !found {
		t.Fatal("docs/architecture.md has no dependency table")
	}
	table, _, _ = strings.Cut(table, "\n#")
	var modules []string
	for _, match := range regexp.MustCompile("(?m)^\\|\\s*`([a-z]+)`\\s*\\|").FindAllStringSubmatch(table, -1) {
		modules = append(modules, match[1])
	}
	if diff := cmp.Diff(modules, pg.ModuleOrder); diff != "" {
		t.Errorf("modules (-docs/architecture.md +moduleOrder):\n%s", diff)
	}
}

// TestModulesRunInTheFixedOrder: platform first, then the modules in the table's order whatever order cmd/ gives them
// in; a name that is not a module, or comes twice, fails the start.
func TestModulesRunInTheFixedOrder(t *testing.T) {
	t.Parallel()
	module := func(name string) pg.Module { return pg.Module{Name: name, Migrations: fstest.MapFS{}} }
	cases := []struct {
		name    string
		modules []pg.Module
		want    []string
		err     string
	}{
		{
			"out of order",
			[]pg.Module{module("gateway"), module("accounts"), module("catalog")},
			[]string{"platform", "catalog", "accounts", "gateway"},
			"",
		},
		{"an unknown module", []pg.Module{module("acounts")}, nil, `"acounts": not a module`},
		{"a module twice", []pg.Module{module("accounts"), module("accounts")}, nil, `"accounts": given twice`},
		{"platform given", []pg.Module{module("platform")}, nil, `"platform": given twice`},
	}
	for _, c := range cases {
		modules, err := pg.Ordered(c.modules...)
		var names []string
		for _, m := range modules {
			names = append(names, m.Name)
		}
		if diff := cmp.Diff(c.want, names); diff != "" || c.err == "" && err != nil ||
			c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%s: %v, want %q; order (-want +got):\n%s", c.name, err, c.err, diff)
		}
	}
}

// lookupMigration is a module migration that hands a lookup function to axiom_lookup, as accounts' will: the role
// reads the one column it needs through a policy of its own, and only axiom_app may call the function. ALTER
// FUNCTION … OWNER takes CREATE on the schema for the new owner, which is granted only for that statement.
const lookupMigration = `-- +goose Up
CREATE TABLE accounts.users (site_id uuid NOT NULL, account_id uuid NOT NULL, email text NOT NULL);
ALTER TABLE accounts.users ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON accounts.users USING (platform.in_scope(site_id, account_id));
CREATE POLICY lookup ON accounts.users FOR SELECT TO axiom_lookup USING (true);
GRANT USAGE ON SCHEMA accounts TO axiom_app, axiom_lookup;
GRANT SELECT (account_id, email) ON accounts.users TO axiom_lookup;
CREATE FUNCTION accounts.login_tenant(email text) RETURNS uuid
	LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
	RETURN (SELECT account_id FROM accounts.users WHERE users.email = login_tenant.email);
REVOKE EXECUTE ON FUNCTION accounts.login_tenant FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accounts.login_tenant TO axiom_app;
GRANT CREATE ON SCHEMA accounts TO axiom_lookup;
ALTER FUNCTION accounts.login_tenant OWNER TO axiom_lookup;
REVOKE CREATE ON SCHEMA accounts FROM axiom_lookup;
`

// TestModuleMigrations: the administrator, which is not a superuser, applies a module's migrations, lookup function
// included; the application finds a tenant through the function with no scope, and Check accepts the database for
// the module's migrations but refuses it for a program that carries a later one or one never applied. Migrate grants
// roles cluster-wide, so this test does not run in parallel (see TestMigrateRunsTakeTurns).
func TestModuleMigrations(t *testing.T) {
	database := pgtest.New(t)
	ctx := t.Context()
	accounts := pg.Module{Name: "accounts", Migrations: fstest.MapFS{"00001_users.sql": {Data: []byte(lookupMigration)}}}
	if err := pg.Migrate(ctx, database.Migrator, accounts); err != nil {
		t.Fatal(err)
	}
	site, account := uuid.NewV7(), uuid.NewV7()
	if err := pgtest.Run(ctx, database.Superuser, "INSERT INTO accounts.users VALUES ('"+site.String()+"', '"+
		account.String()+"', 'someone@example.com')"); err != nil {
		t.Fatal(err)
	}
	db := open(t, database.App)
	var found uuid.UUID
	if err := db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		return tx.QueryRow(ctx, "SELECT accounts.login_tenant('someone@example.com')").Scan(&found)
	}); err != nil || found != account {
		t.Errorf("the lookup found %v, %v, want %v", found, err, account)
	}
	later := pg.Module{Name: "accounts", Migrations: fstest.MapFS{
		"00001_users.sql": {Data: []byte(lookupMigration)}, "00002_more.sql": {Data: []byte("-- +goose Up\n")},
	}}
	checks := []struct {
		name   string
		db     *pg.DB
		module pg.Module
		want   string
	}{
		{"the migrated module", db, accounts, ""},
		{"a later migration", db, later, "has accounts migration 1, this program needs 2"},
		{"a module never migrated", open(t, pgtest.New(t).App), accounts, "accounts (run axiom migrate first)"},
	}
	for _, c := range checks {
		err := c.db.Check(ctx, c.module)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// TestCheckAuditsWhatMigrationsBuilt: each database, migrated and then changed the way a careless migration would,
// is refused with what is wrong and where.
func TestCheckAuditsWhatMigrationsBuilt(t *testing.T) {
	t.Parallel()
	const table = "CREATE TABLE platform.leak (note text)"
	const policy = "CREATE POLICY read ON platform.leak USING (true)"
	const enable, force = "ALTER TABLE platform.leak ENABLE ROW LEVEL SECURITY", "ALTER TABLE platform.leak FORCE ROW LEVEL SECURITY"
	// definer creates a lookup function in platform with the given search_path, owner and grants.
	definer := func(path, owner string, revoke bool) []string {
		statements := []string{
			"CREATE FUNCTION platform.find() RETURNS int LANGUAGE sql SECURITY DEFINER SET search_path = " + path + " RETURN 1",
			"ALTER FUNCTION platform.find OWNER TO " + owner,
			"GRANT EXECUTE ON FUNCTION platform.find TO axiom_app",
		}
		if revoke {
			statements = append(statements, "REVOKE EXECUTE ON FUNCTION platform.find FROM PUBLIC")
		}
		return statements
	}
	const tables = "tables without forced row-level security and policies: platform.leak"
	const definers = "SECURITY DEFINER functions not owned by axiom_lookup, with search_path pg_catalog, pg_temp, " +
		"run only by axiom_app: platform.find()"
	cases := []struct {
		name       string
		statements []string
		want       string
	}{
		{"row-level security forced, not enabled", []string{table, force, policy}, tables},
		{"row-level security not forced", []string{table, enable, policy}, tables},
		{"no policy", []string{table, enable, force}, tables},
		{"a protected table", []string{table, enable, force, policy}, ""},
		{"a partitioned table without row-level security", []string{"CREATE TABLE platform.leak (note int) PARTITION BY RANGE (note)"}, tables},
		{"a lookup function", definer("pg_catalog, pg_temp", "axiom_lookup", true), ""},
		{"a definer owned by another role", definer("pg_catalog, pg_temp", "axiom_owner", true), definers},
		{"a definer with another search_path", definer("pg_catalog, public, pg_temp", "axiom_lookup", true), definers},
		{"a definer anyone may run", definer("pg_catalog, pg_temp", "axiom_lookup", false), definers},
		{
			"a table the lookup role owns",
			[]string{"CREATE TABLE public.mine ()", "ALTER TABLE public.mine OWNER TO axiom_lookup"},
			"the lookup role axiom_lookup breaks its limits: it owns mine",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			database := pgtest.New(t)
			if err := pgtest.Run(t.Context(), database.Superuser, c.statements...); err != nil {
				t.Fatal(err)
			}
			err := open(t, database.App).Check(t.Context())
			if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Errorf("%v, want %q", err, c.want)
			}
		})
	}
}
