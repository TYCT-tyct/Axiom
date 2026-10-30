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

// The tenants every test starts from: the main site s1 with its tier t1 and personal account a1, and the secondary
// site s2, opened by a1, with its tier t2 and personal account a2. Each account has its default workspace (w1, w2),
// user (u1, u2), API key (k1, k2), session (e1, e2) and recovery code (c1, c2); u1 holds the platform grant g1, and
// u2 is a2's member. p is a personal account of s1 with no user yet, as registration leaves one before it adds
// the user. role is a role of the test's own, and spare a spare one, for a grant u1 does not hold yet.
var (
	s1, s2, t1, t2     = uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	a1, a2, w1, w2     = uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	u1, u2, k1, k2     = uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	e1, e2, c1, c2     = uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	g1, role, spare, p = uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	x, y, z            = uuid.NewV7(), uuid.NewV7(), uuid.NewV7() // rows a test creates
	platform           = pg.NewScope(true, nil, nil)

	// names spells the IDs in SQL as :name, and reads them back by name.
	names = map[string]uuid.UUID{
		"s1": s1, "s2": s2, "t1": t1, "t2": t2, "a1": a1, "a2": a2, "w1": w1, "w2": w2, "u1": u1, "u2": u2,
		"k1": k1, "k2": k2, "e1": e1, "e2": e2, "c1": c1, "c2": c2, "g1": g1, "role": role, "spare": spare, "p": p, "x": x, "y": y, "z": z,
	}
	ids = func() *strings.Replacer {
		var pairs []string
		for name, id := range names {
			pairs = append(pairs, ":"+name, "'"+id.String()+"'")
		}
		return strings.NewReplacer(pairs...)
	}()
)

// Rows of the account tables, given the names of their IDs; each statement creates one.
func user(id, site, account, inviter string) string {
	return `INSERT INTO accounts.users (id, site_id, account_id, email, password_hash, factor_version, status, invite_code,
		inviter_user_id) VALUES (` + id + `, ` + site + `, ` + account + `, 'user' || md5(` + id + `) || '@example.com',
		'$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$aGFzaA', 1, 'active', left(md5(` + id + `), 12), ` + inviter + `)`
}

func member(site, account, kind, user, userAccount string) string {
	return `INSERT INTO accounts.members (site_id, account_id, account_kind, user_id, user_account_id, role_id)
		VALUES (` + site + `, ` + account + `, '` + kind + `', ` + user + `, ` + userAccount + `, :role)`
}

func grant(id, site, account, user, role string) string {
	return `INSERT INTO accounts.platform_grants (id, site_id, account_id, user_id, role_id)
		VALUES (` + id + `, ` + site + `, ` + account + `, ` + user + `, ` + role + `)`
}

func apiKey(id, site, account, workspace, createdBy, kind string) string {
	return `INSERT INTO accounts.api_keys (id, site_id, account_id, workspace_id, kind, name, prefix, last4, secret_hash,
		secret_version, created_by, status) VALUES (` + id + `, ` + site + `, ` + account + `, ` + workspace + `, '` + kind +
		`', 'Key', 'sk-ax-v1-', 'abcd', sha256(convert_to(` + id + `, 'UTF8')), 1, ` + createdBy + `, 'active')`
}

func session(id, site, account, user string) string {
	return `INSERT INTO accounts.sessions (id, site_id, account_id, user_id, surface, token_hash, csrf_token, level,
		factor_version, expires_at, idle_timeout) VALUES (` + id + `, ` + site + `, ` + account + `, ` + user + `, 'console',
		sha256(convert_to(` + id + `, 'UTF8')), md5(` + id + `), 'password', 1, now() + interval '1 day', '1 hour')`
}

func code(id, site, account, user string) string {
	return `INSERT INTO accounts.recovery_codes (id, site_id, account_id, user_id, factor_version, code_hash)
		VALUES (` + id + `, ` + site + `, ` + account + `, ` + user + `, 1, sha256(convert_to(` + id + `, 'UTF8')))`
}

// seed is the starting rows. s1 has no settings and a1 no member yet, so a test can add them.
var seed = []string{
	`INSERT INTO accounts.sites (id, name, status, key_prefix, registration, default_tier_id)
		VALUES (:s1, 'Main', 'active', 'sk-ax-v1-', 'invite', :t1)`,
	`INSERT INTO accounts.tiers VALUES (:t1, :s1, 'Standard')`,
	`INSERT INTO accounts.accounts VALUES (:a1, :s1, 'personal', 'A1', 'active', :t1)`,
	`INSERT INTO accounts.sites (id, parent_site_id, owner_account_id, name, status, key_prefix, registration,
		default_tier_id) VALUES (:s2, :s1, :a1, 'Second', 'active', 'sk-two-', 'closed', :t2)`,
	`INSERT INTO accounts.tiers VALUES (:t2, :s2, 'Retail')`,
	`INSERT INTO accounts.accounts VALUES (:a2, :s2, 'personal', 'A2', 'active', :t2)`,
	`INSERT INTO accounts.accounts VALUES (:p, :s1, 'personal', 'P', 'active', :t1)`,
	`INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default)
		VALUES (:w1, :s1, :a1, 'Default', 'active', true), (:w2, :s2, :a2, 'Default', 'active', true)`,
	`INSERT INTO accounts.site_domains VALUES ('main.example', :s1, 'console', 'https://main.example', NULL),
		('second.example', :s2, 'api', 'https://second.example', NULL)`,
	`INSERT INTO accounts.site_settings (site_id) VALUES (:s2)`,
	`INSERT INTO accounts.roles VALUES (:role, 'tester', '{}', false), (:spare, 'spare', '{}', false)`,
	user(":u1", ":s1", ":a1", "NULL"), user(":u2", ":s2", ":a2", "NULL"),
	member(":s2", ":a2", "personal", ":u2", ":a2"),
	grant(":g1", ":s1", ":a1", ":u1", ":role"),
	apiKey(":k1", ":s1", ":a1", ":w1", ":u1", "call"), apiKey(":k2", ":s2", ":a2", ":w2", ":u2", "call"),
	session(":e1", ":s1", ":a1", ":u1"), session(":e2", ":s2", ":a2", ":u2"),
	code(":c1", ":s1", ":a1", ":u1"), code(":c2", ":s2", ":a2", ":u2"),
	`INSERT INTO platform.tenants VALUES (:s1, :s1), (:s2, :s2), (:s1, :a1), (:s2, :a2), (:s1, :p)`,
}

// Statements that create a row; newSite also creates its first tier, which the site's default tier must be.
const (
	newSite      = `INSERT INTO accounts.sites (id, parent_site_id, owner_account_id, name, status, key_prefix, registration, default_tier_id) VALUES (:x, :s1, :a1, 'New', 'active', 'sk-new-', 'invite', :y)`
	newTier      = `INSERT INTO accounts.tiers VALUES (:y, :x, 'New')`
	newAccount   = `INSERT INTO accounts.accounts VALUES (:x, :s1, 'personal', 'New', 'active', :t1)`
	newWorkspace = `INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default) VALUES (:x, :s1, :a1, 'New', 'active', false)`
)

// open returns the application's pool on a fresh, seeded database.
func open(t testing.TB) (*pg.DB, pgtest.Database) {
	t.Helper()
	database := pgtest.New(t, accounts.Module())
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(strings.Join(seed, ";\n"))); err != nil {
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
// takes a new row only for a tenant in the scope; roles only the platform scope sees and writes, platform grants
// only the platform scope writes.
func TestScopesSeeAndWriteOnlyTheirRows(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	scopes := map[string]pg.Scope{
		"none": {}, "platform": platform,
		"s1": pg.NewScope(false, []uuid.UUID{s1}, nil), "s2": pg.NewScope(false, []uuid.UUID{s2}, nil),
		"a1": pg.NewScope(false, nil, []uuid.UUID{a1}), "a2": pg.NewScope(false, nil, []uuid.UUID{a2}),
		"p": pg.NewScope(false, nil, []uuid.UUID{p}),
	}
	sites := map[string][]string{"platform": {"s1", "s2"}, "s1": {"s1"}, "s2": {"s2"}}
	accountRows := map[string][]string{"platform": {"a1", "a2"}, "s1": {"a1"}, "s2": {"a2"}, "a1": {"a1"}, "a2": {"a2"}}
	tenantOf1 := []string{"s1", "a1", "platform"}
	tables := []scopeCase{
		{"sites", "id", "name = name", sites, []string{newSite, newTier}, []string{"platform"}, nil},
		{
			"site_domains", "site_id", "disabled_at = now()", sites,
			[]string{`INSERT INTO accounts.site_domains VALUES ('new.example', :s1, 'api', 'https://new.example', NULL)`},
			[]string{"s1", "platform"},
			nil,
		},
		{
			"site_settings", "site_id", "new_key_daily_limit = 0",
			map[string][]string{"platform": {"s2"}, "s2": {"s2"}},
			[]string{`INSERT INTO accounts.site_settings (site_id) VALUES (:s1)`},
			[]string{"s1", "platform"},
			nil,
		},
		{
			"tiers", "site_id", "name = name",
			map[string][]string{"platform": {"s1", "s2"}, "s1": {"s1"}, "s2": {"s2"}, "a1": {"s1"}, "a2": {"s2"}, "p": {"s1"}},
			[]string{`INSERT INTO accounts.tiers VALUES (:x, :s1, 'New')`},
			[]string{"s1", "platform"},
			[]string{"s1", "s2", "platform"},
		},
		{
			"roles", "name", "permissions = '{}'",
			map[string][]string{"platform": {"admin", "finance", "member", "owner", "spare", "support", "tester", "viewer"}},
			[]string{`INSERT INTO accounts.roles VALUES (:x, 'custom', '{}', false)`},
			[]string{"platform"},
			nil,
		},
		{
			"accounts", "id", "name = name",
			map[string][]string{"platform": {"a1", "a2", "p"}, "s1": {"a1", "p"}, "s2": {"a2"}, "a1": {"a1"}, "a2": {"a2"}, "p": {"p"}},
			[]string{newAccount},
			[]string{"s1", "platform"},
			nil,
		},
		{"workspaces", "account_id", "name = name", accountRows, []string{newWorkspace}, tenantOf1, nil},
		{
			"users", "account_id", "status = status", accountRows,
			[]string{user(":z", ":s1", ":p", ":u1")},
			[]string{"s1", "p", "platform"},
			nil,
		},
		{
			"members", "account_id", "",
			map[string][]string{"platform": {"a2"}, "s2": {"a2"}, "a2": {"a2"}},
			[]string{member(":s1", ":a1", "personal", ":u1", ":a1")},
			tenantOf1, nil,
		},
		{
			"platform_grants", "account_id", "revoked_at = revoked_at",
			map[string][]string{"platform": {"a1"}, "s1": {"a1"}, "a1": {"a1"}},
			[]string{grant(":z", ":s1", ":a1", ":u1", ":spare")},
			[]string{"platform"},
			[]string{"platform"},
		},
		{"api_keys", "account_id", "name = name", accountRows, []string{apiKey(":z", ":s1", ":a1", ":w1", ":u1", "call")}, tenantOf1, nil},
		{"sessions", "account_id", "mfa_attempts = mfa_attempts", accountRows, []string{session(":z", ":s1", ":a1", ":u1")}, tenantOf1, nil},
		{"recovery_codes", "account_id", "used_at = used_at", accountRows, []string{code(":z", ":s1", ":a1", ":u1")}, tenantOf1, nil},
	}
	for _, c := range tables {
		for name, scope := range scopes {
			c.check(t, db, name, scope)
		}
	}
}

// scopeCase is what each scope may see and write in one table.
type scopeCase struct {
	table, tenant, update string
	visible               map[string][]string // the tenants each scope sees, by name; none where unlisted
	insert                []string            // creates a row of s1 or a1
	inserters, updaters   []string            // the scopes that may; all that see the rows where nil
}

func (c scopeCase) check(t *testing.T, db *pg.DB, name string, scope pg.Scope) {
	t.Helper()
	seen := tenants(t, db, scope, "SELECT "+c.tenant+"::text FROM accounts."+c.table)
	if diff := cmp.Diff(c.visible[name], seen); diff != "" {
		t.Errorf("%s in scope %s (-want +got):\n%s", c.table, name, diff)
	}
	if c.update != "" {
		want := int64(len(seen))
		if c.updaters != nil && !slices.Contains(c.updaters, name) {
			want = 0
		}
		if changed, err := attempt(t, db, scope, "UPDATE accounts."+c.table+" SET "+c.update); err != nil || changed != want {
			t.Errorf("%s in scope %s: updated %d rows, %v; want %d", c.table, name, changed, err, want)
		}
	}
	_, err := attempt(t, db, scope, c.insert...)
	if want := !slices.Contains(c.inserters, name); want != (state(err) == "42501") || want == (err == nil) {
		t.Errorf("%s in scope %s: inserting returned %v, want refused: %t", c.table, name, err, want)
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
		return strings.Replace(newSite, ":s1, :a1", parent+", "+owner, 1)
	}
	cases := []struct {
		name       string
		statements []string
		want       string
	}{
		{"a workspace on another site than its account", []string{strings.Replace(newWorkspace, ":s1", ":s2", 1)}, "23503"},
		{"an account in another site's tier", []string{strings.Replace(newAccount, ":t1", ":t2", 1)}, "23503"},
		{"a site owned by an account of another site", []string{site(":s1", ":a2"), newTier}, "23503"},
		{"a site under a secondary site", []string{site(":s2", ":a2"), newTier}, "23503"},
		{"a second main site", []string{site("NULL", "NULL"), newTier}, "23505"},
		{"a parent without an owner", []string{site(":s1", "NULL"), newTier}, "23514"},
		{"a site whose default tier is another site's", []string{strings.Replace(newSite, ":y", ":t1", 1)}, "23503"},
		{"a second default workspace", []string{strings.Replace(newWorkspace, "false", "true", 1)}, "23505"},
		{"deleting the default workspace", []string{`UPDATE accounts.workspaces SET deleted_at = now() WHERE id = :w1`}, "23514"},
		{"a user invited by a user of another site", []string{newAccount, user(":z", ":s1", ":x", ":u2")}, "23503"},
		{"a user inviting itself", []string{newAccount, user(":z", ":s1", ":x", ":z")}, "23514"},
		{"a second user on a personal account", []string{user(":z", ":s1", ":a1", "NULL")}, "23505"},
		{
			"a user on an organization account",
			[]string{strings.Replace(newAccount, "'personal'", "'organization'", 1), user(":z", ":s1", ":x", "NULL")},
			"23503",
		},
		{"a member from another site", []string{member(":s1", ":a1", "personal", ":u2", ":a2")}, "23503"},
		{"a member with the account's kind wrong", []string{member(":s1", ":a1", "organization", ":u1", ":a1")}, "23503"},
		{
			"a personal account's member who does not own it",
			[]string{newAccount, user(":z", ":s1", ":x", "NULL"), member(":s1", ":a1", "personal", ":z", ":x")},
			"23503",
		},
		{"a platform grant to a secondary site's user", []string{grant(":z", ":s2", ":a2", ":u2", ":role")}, "23503"},
		{"a key on another account's workspace", []string{apiKey(":z", ":s1", ":a1", ":w2", ":u1", "call")}, "23503"},
		{"a key created by a user of another site", []string{apiKey(":z", ":s1", ":a1", ":w1", ":u2", "call")}, "23503"},
		{"a management key that never expires", []string{apiKey(":z", ":s1", ":a1", ":w1", ":u1", "management")}, "23514"},
		{"a session of another account's user", []string{session(":z", ":s1", ":a1", ":u2")}, "23503"},
		{"a recovery code of another account's user", []string{code(":z", ":s1", ":a1", ":u2")}, "23503"},
		{"a secondary site with its own tier", []string{newSite, newTier}, ""},
	}
	for _, c := range cases {
		if _, err := exec(t, db, platform, c.statements...); state(err) != c.want { // committed: some checks wait for it
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
		var inserted error
		err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, inserted = tx.Exec(t.Context(), ids.Replace(strings.Replace(newSite, ":y", c.tier, 1)))
			if c.tier == ":y" && inserted == nil {
				_, inserted = tx.Exec(t.Context(), ids.Replace(newTier))
			}
			return inserted
		})
		if inserted != nil || state(err) != c.want {
			t.Errorf("default tier %s: inserting returned %v and committing %v, want SQLSTATE %q at commit", c.tier, inserted, err, c.want)
		}
	}
}

// TestApplicationChangesOnlyUpdatableColumns: the application may update exactly the columns the README lists, and
// delete nothing except expired password reset data and dispatched notices; the other tables refuse the privilege, not just the rows.
func TestApplicationChangesOnlyUpdatableColumns(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	updatable := map[string][]string{
		"presets":                  {"name", "config", "updated_at", "deleted_at"},
		"notification_preferences": {"low_balance", "top_up", "factor_changed", "threshold", "webhook_url", "language"},
		"user_notices":             {"email_done", "email_failed", "dispatched", "webhook_url", "webhook_state"},
		"announcements":            {"withdrawn_at"},
		"sites":                    {"name", "status", "key_prefix", "registration", "default_tier_id"},
		"site_domains":             {"disabled_at"},
		"site_settings":            {"console_idle_timeout", "console_max_age", "admin_idle_timeout", "admin_max_age", "new_key_daily_limit", "ip_attempt_burst", "ip_attempts_per_minute", "email_sign_in_failures", "user_code_failures", "challenge_code_attempts", "max_keys_per_account", "key_creations_per_hour", "registration_human_check", "registration_human_check_bits", "registration_domain_burst", "registration_domain_per_hour", "registration_domain_allow", "registration_domain_deny"},
		"tiers":                    {"name", "rate_requests"},
		"roles":                    {"permissions", "additional_permissions"},
		"accounts":                 {"name", "status", "tier_id", "max_keys", "key_creations_per_hour", "rate_requests"},
		"workspaces":               {"name", "status", "budget", "budget_reset", "deleted_at"},
		"users": {
			"password_hash", "factor_version", "status", "totp_ciphertext", "totp_nonce", "totp_wrapped_dek",
			"totp_key_version", "totp_last_step", "mfa_enabled_at",
		},
		"members":                 nil,
		"platform_grants":         {"revoked_at"},
		"api_keys":                {"rate_requests", "updated_at", "management_window", "management_requests", "name", "status", "spend_limit", "limit_reset", "preset_id", "models", "ip_allow", "deleted_at"},
		"sessions":                {"last_seen_at", "mfa_attempts", "expires_at", "idle_timeout", "revoked_at"},
		"recovery_codes":          {"used_at"},
		"security_failures":       {"started_at", "failures", "completed_peak", "sampled_at", "reported"},
		"password_reset_requests": {"id", "email", "domain", "language", "expires_at"},
		"password_reset_tokens":   {"used_at"},
		"registration_requests":   {"tier_id", "domain", "language", "expires_at", "token_hash"},
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
		if strings.HasPrefix(table, "password_reset_") || table == "registration_requests" || table == "user_notices" {
			continue
		}
		if _, err := exec(t, db, platform, "DELETE FROM accounts."+table+" WHERE false"); state(err) != "42501" {
			t.Errorf("deleting from %s: %v, want permission denied", table, err)
		}
	}
}

// TestTerminalStatesAreFinal: the first soft delete, revocation or use goes through; after it nothing changes the
// row, the terminal column included, in the tenant's scope or the platform's.
func TestTerminalStatesAreFinal(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	account := pg.NewScope(false, nil, []uuid.UUID{a1})
	if _, err := exec(t, db, platform, newWorkspace); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		table, row, terminal, other string
		writer                      pg.Scope // the narrowest scope that may make the first change
	}{
		{"workspaces", ":x", "deleted_at", "name = 'Back'", account},
		{"api_keys", ":k1", "deleted_at", "name = 'Back'", account},
		{"sessions", ":e1", "revoked_at", "mfa_attempts = 1", account},
		{"recovery_codes", ":c1", "used_at", "used_at = now()", account},
		{"platform_grants", ":g1", "revoked_at", "revoked_at = now()", platform},
	}
	for _, c := range cases {
		update := func(set string) string {
			return "UPDATE accounts." + c.table + " SET " + set + " WHERE id = " + c.row
		}
		steps := []struct {
			scope     pg.Scope
			statement string
			want      int64
		}{
			{c.writer, update(c.terminal + " = now()"), 1},
			{c.writer, update(c.terminal + " = NULL"), 0},
			{c.writer, update(c.other), 0},
			{platform, update(c.terminal + " = NULL"), 0},
			{platform, update(c.other), 0},
		}
		for _, s := range steps {
			if changed, err := exec(t, db, s.scope, s.statement); err != nil || changed != s.want {
				t.Errorf("%s: changed %d rows, %v; want %d", s.statement, changed, err, s.want)
			}
		}
	}
}

// TestOnePolicyPerCommand: every table has exactly one policy for everyone for each of SELECT, INSERT and UPDATE,
// and none for ALL; only reset tables and user notices allow DELETE of expired rows. Members has none for UPDATE. Policies are OR-ed,
// so a second UPDATE or a FOR ALL policy would let through what a terminal-state policy refuses.
func TestOnePolicyPerCommand(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	got := texts(t, db, platform, `SELECT c.relname || ': ' || coalesce(string_agg(p.cmd, ', ' ORDER BY p.cmd), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_policies p ON p.schemaname = n.nspname AND p.tablename = c.relname AND p.roles = '{public}'
		WHERE n.nspname = 'accounts' AND c.relkind = 'r' GROUP BY c.relname ORDER BY c.relname`)
	var want []string
	for _, table := range []string{
		"accounts", "announcements", "api_keys", "members", "notification_preferences", "password_reset_requests", "password_reset_tokens", "platform_grants", "presets", "recovery_codes", "registration_requests", "roles", "security_failures", "sessions", "site_domains",
		"site_settings", "sites", "tiers", "user_notices", "users", "workspaces",
	} {
		commands := "INSERT, SELECT, UPDATE"
		if strings.HasPrefix(table, "password_reset_") || table == "registration_requests" || table == "user_notices" {
			commands = "DELETE, INSERT, SELECT, UPDATE"
		}
		if table == "members" {
			commands = "INSERT, SELECT"
		}
		want = append(want, table+": "+commands)
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
		{"not forced", []string{"ALTER TABLE accounts.sessions NO FORCE ROW LEVEL SECURITY"}, "accounts.sessions"},
		{
			"without policies",
			[]string{"DROP POLICY reads ON accounts.members", "DROP POLICY inserts ON accounts.members"},
			"accounts.members",
		},
	}
	for _, c := range cases {
		db, database := open(t)
		if err := pgtest.Run(t.Context(), database.Superuser, c.statements...); err != nil {
			t.Fatal(err)
		}
		err := db.Check(t.Context(), accounts.Module())
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
