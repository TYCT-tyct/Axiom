package accounts_test

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// The tenants every test starts from: the main site s1 with its tier t1 and account a1, and the secondary site s2,
// opened by a1, with its tier t2 and account a2. Each account has its default workspace, w1 and w2.
var (
	s1, s2, t1, t2 = uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	a1, a2, w1, w2 = uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	x, y           = uuid.NewV7(), uuid.NewV7() // rows a test creates
	platform       = pg.NewScope(true, nil, nil)

	// names spells the IDs in SQL as :name, and reads them back by name.
	names = map[string]uuid.UUID{"s1": s1, "s2": s2, "t1": t1, "t2": t2, "a1": a1, "a2": a2, "w1": w1, "w2": w2, "x": x, "y": y}
	ids   = func() *strings.Replacer {
		var pairs []string
		for name, id := range names {
			pairs = append(pairs, ":"+name, "'"+id.String()+"'")
		}
		return strings.NewReplacer(pairs...)
	}()
)

// s1 has no settings yet, so a test can add them.
const seed = `
INSERT INTO accounts.sites (id, name, status, key_prefix, registration, default_tier_id)
	VALUES (:s1, 'Main', 'active', 'sk-ax-v1-', 'invite', :t1);
INSERT INTO accounts.tiers VALUES (:t1, :s1, 'Standard');
INSERT INTO accounts.accounts VALUES (:a1, :s1, 'personal', 'A1', 'active', :t1);
INSERT INTO accounts.sites (id, parent_site_id, owner_account_id, name, status, key_prefix, registration, default_tier_id)
	VALUES (:s2, :s1, :a1, 'Second', 'active', 'sk-two-', 'closed', :t2);
INSERT INTO accounts.tiers VALUES (:t2, :s2, 'Retail');
INSERT INTO accounts.accounts VALUES (:a2, :s2, 'organization', 'A2', 'active', :t2);
INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default)
	VALUES (:w1, :s1, :a1, 'Default', 'active', true), (:w2, :s2, :a2, 'Default', 'active', true);
INSERT INTO accounts.site_domains VALUES ('main.example', :s1, 'console', 'https://main.example', NULL),
	('second.example', :s2, 'api', 'https://second.example', NULL);
INSERT INTO accounts.site_settings (site_id) VALUES (:s2);`

// Statements that create a row; newSite also creates its first tier, which the site's default tier must be.
const (
	newSite      = `INSERT INTO accounts.sites (id, parent_site_id, owner_account_id, name, status, key_prefix, registration, default_tier_id) VALUES (:x, :s1, :a1, 'New', 'active', 'sk-new-', 'invite', :y)`
	newTier      = `INSERT INTO accounts.tiers VALUES (:y, :x, 'New')`
	newAccount   = `INSERT INTO accounts.accounts VALUES (:x, :s1, 'personal', 'New', 'active', :t1)`
	newWorkspace = `INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default) VALUES (:x, :s1, :a1, 'New', 'active', false)`
)

// open returns the application's pool on a fresh, seeded database.
func open(t *testing.T) (*pg.DB, pgtest.Database) {
	t.Helper()
	database := pgtest.New(t, accounts.Migrations())
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(seed)); err != nil {
		t.Fatal(err)
	}
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db, database
}

// exec runs statements, with IDs spelled :name, in one transaction of scope, and returns how many rows the last one
// changed.
func exec(t *testing.T, db *pg.DB, scope pg.Scope, statements ...string) (int64, error) {
	t.Helper()
	return run(t, db, scope, true, statements)
}

// attempt is exec rolled back, so the next attempt starts from the same rows.
func attempt(t *testing.T, db *pg.DB, scope pg.Scope, statements ...string) (int64, error) {
	t.Helper()
	return run(t, db, scope, false, statements)
}

var errRollback = errors.New("roll back")

func run(t *testing.T, db *pg.DB, scope pg.Scope, commit bool, statements []string) (int64, error) {
	t.Helper()
	var changed int64
	err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		for _, s := range statements {
			tag, err := tx.Exec(t.Context(), ids.Replace(s))
			if err != nil {
				return err
			}
			changed = tag.RowsAffected()
		}
		if !commit {
			return errRollback
		}
		return nil
	})
	if errors.Is(err, errRollback) {
		err = nil
	}
	return changed, err
}

// texts returns the one text column of query's rows in scope.
func texts(t *testing.T, db *pg.DB, scope pg.Scope, query string) []string {
	t.Helper()
	var values []string
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return err
			}
			values = append(values, v)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return values
}

var sqlstate = regexp.MustCompile(`\(SQLSTATE (\w{5})\)`)

// state is the SQLSTATE an error reports, or "" for none. Outside platform, tests read it from the message: pgx's
// error types are platform's to import.
func state(err error) string {
	if err == nil {
		return ""
	}
	if m := sqlstate.FindStringSubmatch(err.Error()); m != nil {
		return m[1]
	}
	return err.Error()
}

// TestScopesSeeAndWriteOnlyTheirRows: each table shows a scope only its own tenants' rows, updates only those, and
// takes a new row only for a tenant in the scope; roles only the platform scope sees and writes.
func TestScopesSeeAndWriteOnlyTheirRows(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	scopes := map[string]pg.Scope{
		"none": {}, "platform": platform,
		"s1": pg.NewScope(false, []uuid.UUID{s1}, nil), "s2": pg.NewScope(false, []uuid.UUID{s2}, nil),
		"a1": pg.NewScope(false, nil, []uuid.UUID{a1}), "a2": pg.NewScope(false, nil, []uuid.UUID{a2}),
	}
	sites := map[string][]string{"platform": {"s1", "s2"}, "s1": {"s1"}, "s2": {"s2"}}
	accountRows := map[string][]string{"platform": {"a1", "a2"}, "s1": {"a1"}, "s2": {"a2"}, "a1": {"a1"}, "a2": {"a2"}}
	builtin := []string{"admin", "finance", "member", "owner", "support", "viewer"}
	tables := []struct {
		table, tenant, update string
		visible               map[string][]string // the tenants each scope sees, by name; none where unlisted
		insert                []string            // creates a row of s1 or a1
		inserters             []string            // the scopes that may
	}{
		{"sites", "id", "name = name", sites, []string{newSite, newTier}, []string{"platform"}},
		{
			"site_domains", "site_id", "disabled_at = now()", sites,
			[]string{`INSERT INTO accounts.site_domains VALUES ('new.example', :s1, 'api', 'https://new.example', NULL)`},
			[]string{"s1", "platform"},
		},
		{
			"site_settings", "site_id", "new_key_daily_limit = 0",
			map[string][]string{"platform": {"s2"}, "s2": {"s2"}},
			[]string{`INSERT INTO accounts.site_settings (site_id) VALUES (:s1)`},
			[]string{"s1", "platform"},
		},
		{
			"tiers", "site_id", "name = name", sites,
			[]string{`INSERT INTO accounts.tiers VALUES (:x, :s1, 'New')`},
			[]string{"s1", "platform"},
		},
		{
			"roles", "name", "permissions = '{}'",
			map[string][]string{"platform": builtin},
			[]string{`INSERT INTO accounts.roles VALUES (:x, 'custom', '{}', false)`},
			[]string{"platform"},
		},
		{"accounts", "id", "name = name", accountRows, []string{newAccount}, []string{"s1", "platform"}},
		{
			"workspaces", "account_id", "name = name", accountRows,
			[]string{newWorkspace},
			[]string{"s1", "a1", "platform"},
		},
	}
	for _, c := range tables {
		for name, scope := range scopes {
			seen := tenants(t, db, scope, "SELECT "+c.tenant+"::text FROM accounts."+c.table)
			if diff := cmp.Diff(c.visible[name], seen); diff != "" {
				t.Errorf("%s in scope %s (-want +got):\n%s", c.table, name, diff)
			}
			if changed, err := attempt(t, db, scope, "UPDATE accounts."+c.table+" SET "+c.update); err != nil || changed != int64(len(seen)) {
				t.Errorf("%s in scope %s: updated %d rows, %v; want the %d it sees", c.table, name, changed, err, len(seen))
			}
			_, err := attempt(t, db, scope, c.insert...)
			if want := !slices.Contains(c.inserters, name); want != (state(err) == "42501") || want == (err == nil) {
				t.Errorf("%s in scope %s: inserting returned %v, want refused: %t", c.table, name, err, want)
			}
		}
	}
}

// tenants returns query's rows in scope, IDs replaced by the test's names for them, sorted.
func tenants(t *testing.T, db *pg.DB, scope pg.Scope, query string) []string {
	t.Helper()
	seen := texts(t, db, scope, query)
	for i, v := range seen {
		for name, id := range names {
			if id.String() == v {
				seen[i] = name
			}
		}
	}
	slices.Sort(seen)
	return seen
}

// TestKeysHoldTheTenantStructure: in the platform scope, which policies do not narrow, the keys alone refuse rows
// whose tenants do not belong together and the shapes the README rules out.
func TestKeysHoldTheTenantStructure(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	site := func(parent, owner string) string {
		return `INSERT INTO accounts.sites (id, parent_site_id, owner_account_id, name, status, key_prefix, registration, default_tier_id) VALUES (:x, ` +
			parent + `, ` + owner + `, 'New', 'active', 'sk-new-', 'invite', :y)`
	}
	cases := []struct {
		name       string
		statements []string
		want       string
	}{
		{"a workspace on another site than its account", []string{strings.Replace(newWorkspace, ":s1", ":s2", 1)}, "23503"},
		{"an account in another site's tier", []string{`INSERT INTO accounts.accounts VALUES (:x, :s1, 'personal', 'New', 'active', :t2)`}, "23503"},
		{"a site owned by an account of another site", []string{site(":s1", ":a2"), newTier}, "23503"},
		{"a site under a secondary site", []string{site(":s2", ":a2"), newTier}, "23503"},
		{"a second main site", []string{site("NULL", "NULL"), newTier}, "23505"},
		{"a parent without an owner", []string{site(":s1", "NULL"), newTier}, "23514"},
		{"a site whose default tier is another site's", []string{strings.Replace(site(":s1", ":a1"), ":y", ":t1", 1)}, "23503"},
		{"a second default workspace", []string{strings.Replace(newWorkspace, "false", "true", 1)}, "23505"},
		{"deleting the default workspace", []string{`UPDATE accounts.workspaces SET deleted_at = now() WHERE id = :w1`}, "23514"},
		{"a secondary site with its own tier", []string{newSite, newTier}, ""},
	}
	for _, c := range cases {
		if _, err := exec(t, db, platform, c.statements...); state(err) != c.want {
			t.Errorf("%s: %v, want SQLSTATE %q", c.name, err, c.want)
		}
	}
}

// TestDefaultTierIsCheckedAtCommit: a site and its first tier refer to each other, so a site may name a tier not yet
// inserted; the check waits for the commit, and refuses a tier of another site there.
func TestDefaultTierIsCheckedAtCommit(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	for _, c := range []struct{ tier, want string }{{":t1", "23503"}, {":y", ""}} { // the refused one first: it leaves nothing
		tier, want := c.tier, c.want
		var inserted error
		err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, inserted = tx.Exec(t.Context(), ids.Replace(strings.Replace(newSite, ":y", tier, 1)))
			if tier == ":y" && inserted == nil {
				_, inserted = tx.Exec(t.Context(), ids.Replace(newTier))
			}
			return inserted
		})
		if inserted != nil || state(err) != want {
			t.Errorf("default tier %s: inserting returned %v and committing %v, want SQLSTATE %q at commit", tier, inserted, err, want)
		}
	}
}

// TestApplicationChangesOnlyUpdatableColumns: the application may update exactly the columns the README lists, and
// delete nothing, in any scope; the database refuses the privilege, not just the rows.
func TestApplicationChangesOnlyUpdatableColumns(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	updatable := map[string][]string{
		"sites":         {"name", "status", "key_prefix", "registration", "default_tier_id"},
		"site_domains":  {"disabled_at"},
		"site_settings": {"console_idle_timeout", "console_max_age", "admin_idle_timeout", "admin_max_age", "new_key_daily_limit"},
		"tiers":         {"name"},
		"roles":         {"permissions"},
		"accounts":      {"name", "status", "tier_id"},
		"workspaces":    {"name", "status", "budget", "budget_reset", "deleted_at"},
	}
	// Generated columns are left out: the database refuses to set them before it looks at privileges.
	for _, column := range texts(t, db, platform, `SELECT table_name || ' ' || column_name FROM information_schema.columns
		WHERE table_schema = 'accounts' AND is_generated = 'NEVER'`) {
		table, name, _ := strings.Cut(column, " ")
		_, err := exec(t, db, platform, "UPDATE accounts."+table+" SET "+name+" = "+name+" WHERE false")
		if want := slices.Contains(updatable[table], name); want != (err == nil) || !want && state(err) != "42501" {
			t.Errorf("updating %s.%s: %v, want allowed: %t", table, name, err, want)
		}
	}
	for table := range updatable {
		if _, err := exec(t, db, platform, "DELETE FROM accounts."+table+" WHERE false"); state(err) != "42501" {
			t.Errorf("deleting from %s: %v, want permission denied", table, err)
		}
	}
}

// TestDeletedWorkspaceIsFinal: the first soft delete goes through; after it nothing changes the row, deleted_at
// included, in the account's scope or the platform's.
func TestDeletedWorkspaceIsFinal(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	account := pg.NewScope(false, nil, []uuid.UUID{a1})
	if _, err := exec(t, db, platform, newWorkspace); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		scope     pg.Scope
		statement string
		want      int64
	}{
		{account, "UPDATE accounts.workspaces SET deleted_at = now() WHERE id = :x", 1},
		{account, "UPDATE accounts.workspaces SET deleted_at = NULL WHERE id = :x", 0},
		{account, "UPDATE accounts.workspaces SET name = 'Back' WHERE id = :x", 0},
		{platform, "UPDATE accounts.workspaces SET deleted_at = NULL WHERE id = :x", 0},
		{platform, "UPDATE accounts.workspaces SET status = 'disabled' WHERE id = :x", 0},
	}
	for _, s := range steps {
		if changed, err := exec(t, db, s.scope, s.statement); err != nil || changed != s.want {
			t.Errorf("%s: changed %d rows, %v; want %d", s.statement, changed, err, s.want)
		}
	}
}

// TestOnePolicyPerCommand: every table has exactly one policy for everyone for each of SELECT, INSERT and UPDATE,
// and none for DELETE or ALL. Permissive policies are OR-ed, so a second UPDATE or a FOR ALL policy would let
// through what the terminal-state policy refuses.
func TestOnePolicyPerCommand(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	got := texts(t, db, platform, `SELECT c.relname || ': ' || coalesce(string_agg(p.cmd, ', ' ORDER BY p.cmd), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_policies p ON p.schemaname = n.nspname AND p.tablename = c.relname AND p.roles = '{public}'
		WHERE n.nspname = 'accounts' AND c.relkind = 'r' GROUP BY c.relname ORDER BY c.relname`)
	var want []string
	for _, table := range []string{"accounts", "roles", "site_domains", "site_settings", "sites", "tiers", "workspaces"} {
		want = append(want, table+": INSERT, SELECT, UPDATE")
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("policies for everyone (-want +got):\n%s", diff)
	}
}

// TestStartupCheckCoversTheTables: the startup check accepts the migrated database, and refuses it once an accounts
// table no longer forces row-level security or has lost its policies.
func TestStartupCheckCoversTheTables(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		statements []string
		want       string
	}{
		{"as migrated", nil, ""},
		{"not forced", []string{"ALTER TABLE accounts.workspaces NO FORCE ROW LEVEL SECURITY"}, "accounts.workspaces"},
		{
			"without policies",
			[]string{"DROP POLICY reads ON accounts.roles", "DROP POLICY inserts ON accounts.roles", "DROP POLICY updates ON accounts.roles"},
			"accounts.roles",
		},
	}
	for _, c := range cases {
		db, database := open(t)
		if err := pgtest.Run(t.Context(), database.Superuser, c.statements...); err != nil {
			t.Fatal(err)
		}
		err := db.Check(t.Context(), accounts.Migrations())
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
