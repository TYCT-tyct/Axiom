package store_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/store"
)

// SQLSTATEs the tests expect from PostgreSQL.
const (
	insufficientPrivilege = "42501" // also a row refused by row-level security
	foreignKeyViolation   = "23503"
	uniqueViolation       = "23505"
	checkViolation        = "23514"
)

var (
	siteA, siteB = uuid.NewV7(), uuid.NewV7()
	accountA1    = uuid.NewV7() // of siteA
	platform     = pg.NewScope(true, nil, nil)
	queries      = store.New()
)

// open returns the application's pool on a fresh database that has the module's tables.
func open(t *testing.T) *pg.DB {
	t.Helper()
	database := pgtest.New(t, pg.Module{Name: "suppliers", Migrations: store.Migrations()})
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

// run executes fn in a transaction limited to scope.
func run(t *testing.T, db *pg.DB, scope pg.Scope, fn func(ctx context.Context, tx pg.Tx) error) error {
	t.Helper()
	return db.Tx(t.Context(), scope, func(tx pg.Tx) error { return fn(t.Context(), tx) })
}

// exec runs statements one after another in one platform transaction.
func exec(t *testing.T, db *pg.DB, scope pg.Scope, statements ...string) error {
	t.Helper()
	return run(t, db, scope, func(ctx context.Context, tx pg.Tx) error {
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
}

// owner is who a supplier belongs to.
type owner struct {
	scope         string
	site, account *uuid.UUID
}

var owners = map[string]owner{
	"platform":   {"platform", nil, nil},
	"site A":     {"site", &siteA, nil},
	"site B":     {"site", &siteB, nil},
	"account A1": {"account", &siteA, &accountA1},
}

// seq numbers what tests make so that names and addresses are unique, whichever tests run at once.
var seq atomic.Int64

func addSupplier(ctx context.Context, tx pg.Tx, o owner) (uuid.UUID, error) {
	n := seq.Add(1)
	return queries.InsertSupplier(ctx, tx, store.InsertSupplierParams{
		OwnerScope: o.scope, SiteID: o.site, AccountID: o.account, Name: fmt.Sprintf("supplier %d", n), Kind: "newapi",
		Origin: fmt.Sprintf("https://s%d.example", n), UpstreamAccount: "me", NativeUnit: "quota",
		SettlementCurrency: "USD", MgmtEgressID: "direct",
	})
}

func addGroup(ctx context.Context, tx pg.Tx, supplier uuid.UUID) (uuid.UUID, error) {
	return queries.InsertGroup(ctx, tx, store.InsertGroupParams{
		SupplierID: supplier, UpstreamRef: fmt.Sprint(seq.Add(1)), Name: "default", Ratio: "1.5",
	})
}

// world is one supplier of each owner, each with a group, made in the platform scope; both by the owner's name.
type world struct{ supplier, group map[string]uuid.UUID }

func seed(t *testing.T, db *pg.DB) world {
	t.Helper()
	w := world{map[string]uuid.UUID{}, map[string]uuid.UUID{}}
	if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) (err error) {
		for name, o := range owners {
			if w.supplier[name], err = addSupplier(ctx, tx, o); err != nil {
				return err
			}
			if w.group[name], err = addGroup(ctx, tx, w.supplier[name]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

// seen lists the owners whose rows scope sees in table, by the column that holds the supplier's id.
func seen(t *testing.T, db *pg.DB, scope pg.Scope, w world, table, column string) (names []string) {
	t.Helper()
	if err := run(t, db, scope, func(ctx context.Context, tx pg.Tx) error {
		for _, name := range []string{"platform", "site A", "site B", "account A1"} {
			var n int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+column+" = $1", w.supplier[name]).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				names = append(names, name)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return names
}

// TestScopesSeeTheirOwnSuppliersAndTheirChildren: a supplier is seen by the platform and by the site or account it
// belongs to, and by nobody else; its groups, which carry no owner, are seen by exactly the same scopes.
func TestScopesSeeTheirOwnSuppliersAndTheirChildren(t *testing.T) {
	t.Parallel()
	db := open(t)
	w := seed(t, db)
	cases := []struct {
		name  string
		scope pg.Scope
		want  []string
	}{
		{"no scope", pg.Scope{}, nil},
		{"the platform", platform, []string{"platform", "site A", "site B", "account A1"}},
		{"site A", pg.NewScope(false, []uuid.UUID{siteA}, nil), []string{"site A", "account A1"}},
		{"site B", pg.NewScope(false, []uuid.UUID{siteB}, nil), []string{"site B"}},
		{"account A1", pg.NewScope(false, nil, []uuid.UUID{accountA1}), []string{"account A1"}},
		{"another account", pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()}), nil},
	}
	for _, c := range cases {
		if diff := cmp.Diff(c.want, seen(t, db, c.scope, w, "suppliers.suppliers", "id")); diff != "" {
			t.Errorf("%s sees these suppliers (-want +got):\n%s", c.name, diff)
		}
		if diff := cmp.Diff(c.want, seen(t, db, c.scope, w, "suppliers.supplier_groups", "supplier_id")); diff != "" {
			t.Errorf("%s sees the groups of these suppliers (-want +got):\n%s", c.name, diff)
		}
	}
}

// TestScopesCannotWriteWhatTheyCannotSee: a site cannot create a supplier for the platform or for another site, and
// cannot attach a row of any child table to a supplier it cannot see.
func TestScopesCannotWriteWhatTheyCannotSee(t *testing.T) {
	t.Parallel()
	db := open(t)
	w := seed(t, db)
	siteScope := pg.NewScope(false, []uuid.UUID{siteA}, nil)
	for _, name := range []string{"platform", "site B"} {
		err := run(t, db, siteScope, func(ctx context.Context, tx pg.Tx) error { _, err := addSupplier(ctx, tx, owners[name]); return err })
		if pg.SQLState(err) != insufficientPrivilege {
			t.Errorf("creating a supplier of %s from site A: %v, want row-level security to refuse", name, err)
		}
	}
	for _, name := range []string{"site A", "account A1"} {
		if err := run(t, db, siteScope, func(ctx context.Context, tx pg.Tx) error { _, err := addSupplier(ctx, tx, owners[name]); return err }); err != nil {
			t.Errorf("creating a supplier of %s from site A: %v", name, err)
		}
	}
	// A group of the platform's supplier exists, so the foreign keys of these rows hold and only the policy can refuse.
	supplier, group := w.supplier["platform"], w.group["platform"]
	rows := map[string]string{
		"group": `INSERT INTO suppliers.supplier_groups (supplier_id, upstream_ref, name, ratio) VALUES ('%[1]s', 'x', 'x', 1)`,
		"login": `INSERT INTO suppliers.supplier_logins (supplier_id, kind, ciphertext, nonce, wrapped_dek, key_version)
			VALUES ('%[1]s', 'pat', repeat('a', 16)::bytea, repeat('n', 12)::bytea, 'w', 1)`,
		"model": `INSERT INTO suppliers.supplier_models (supplier_id, supplier_group_id, upstream_name, formats)
			VALUES ('%[1]s', '%[2]s', 'gpt', '{chat}')`,
		"alias": `INSERT INTO suppliers.supplier_aliases (supplier_id, upstream_name, model_id, set_by)
			VALUES ('%[1]s', 'gpt', gen_random_uuid(), 'user')`,
		"binding": `INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, max_concurrency)
			VALUES (gen_random_uuid(), '%[1]s', '%[2]s', 'k', 1)`,
		"endpoint": `INSERT INTO suppliers.endpoints (supplier_id, supplier_group_id, model_id, upstream_name, formats)
			VALUES ('%[1]s', '%[2]s', gen_random_uuid(), 'gpt', '{chat}')`,
	}
	for name, statement := range rows {
		err := exec(t, db, siteScope, fmt.Sprintf(statement, supplier, group))
		if pg.SQLState(err) != insufficientPrivilege {
			t.Errorf("a %s under the platform's supplier, from site A: %v, want row-level security to refuse", name, err)
		}
		if err := exec(t, db, platform, fmt.Sprintf(statement, supplier, group)); err != nil {
			t.Errorf("a %s under the platform's supplier, from the platform: %v", name, err)
		}
	}
}

// TestOwnerMatchesScope: the owner columns say the same as owner_scope, so a supplier cannot be a site's by one column
// and the platform's by another.
func TestOwnerMatchesScope(t *testing.T) {
	t.Parallel()
	db := open(t)
	cases := []struct {
		name string
		o    owner
		ok   bool
	}{
		{"platform", owners["platform"], true},
		{"site", owners["site A"], true},
		{"account", owners["account A1"], true},
		{"platform with a site", owner{"platform", &siteA, nil}, false},
		{"site without a site", owner{"site", nil, nil}, false},
		{"site with an account", owner{"site", &siteA, &accountA1}, false},
		{"account without an account", owner{"account", &siteA, nil}, false},
		{"account without a site", owner{"account", nil, &accountA1}, false},
		{"an unknown scope", owner{"tenant", nil, nil}, false},
	}
	for _, c := range cases {
		err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error { _, err := addSupplier(ctx, tx, c.o); return err })
		if c.ok && err != nil || !c.ok && pg.SQLState(err) != checkViolation {
			t.Errorf("%s: %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// TestChildrenCannotCrossSuppliers: every row that names a supplier and a group names the group's own supplier.
func TestChildrenCannotCrossSuppliers(t *testing.T) {
	t.Parallel()
	db := open(t)
	w := seed(t, db)
	mine, mineGroup, otherGroup := w.supplier["site A"], w.group["site A"], w.group["site B"]
	statements := map[string]string{
		"binding": `INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, max_concurrency)
			VALUES (gen_random_uuid(), '%s', '%s', 'k', 1)`,
		"model": `INSERT INTO suppliers.supplier_models (supplier_id, supplier_group_id, upstream_name, formats)
			VALUES ('%s', '%s', 'gpt', '{chat}')`,
		"alias": `INSERT INTO suppliers.supplier_aliases (supplier_id, supplier_group_id, upstream_name, model_id, set_by)
			VALUES ('%s', '%s', 'gpt', gen_random_uuid(), 'user')`,
		"endpoint": `INSERT INTO suppliers.endpoints (supplier_id, supplier_group_id, model_id, upstream_name, formats)
			VALUES ('%s', '%s', gen_random_uuid(), 'gpt', '{chat}')`,
	}
	for name, statement := range statements {
		if err := exec(t, db, platform, fmt.Sprintf(statement, mine, otherGroup)); pg.SQLState(err) != foreignKeyViolation {
			t.Errorf("a %s of one supplier in a group of another: %v, want a foreign key violation", name, err)
		}
		if err := exec(t, db, platform, fmt.Sprintf(statement, mine, mineGroup)); err != nil {
			t.Errorf("a %s in its own supplier's group: %v", name, err)
		}
	}
	// An alias for a whole supplier names no group.
	err := exec(t, db, platform, fmt.Sprintf(`INSERT INTO suppliers.supplier_aliases (supplier_id, upstream_name, model_id, set_by)
		VALUES ('%s', 'gpt-x', gen_random_uuid(), 'user')`, mine))
	if err != nil {
		t.Errorf("an alias for a whole supplier: %v", err)
	}
}

// keyring loads a master key file, as the program does at start.
func keyring(t *testing.T) *secret.Keyring {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys")
	line := "1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := secret.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func credential(supplier, group, id uuid.UUID, s secret.Sealed) store.InsertCredentialParams {
	version := int64(s.KeyVersion)
	return store.InsertCredentialParams{
		ID: id, SupplierID: supplier, SupplierGroupID: group, UpstreamKeyRef: id.String(), Ciphertext: s.Ciphertext,
		Nonce: s.Nonce, WrappedDek: s.WrappedDEK, KeyVersion: &version, Status: "active", MaxConcurrency: 4,
	}
}

// TestCredentialsAreStoredSealed: the database holds the key nowhere in the clear; the sealed value opens at its own
// row and at no other.
func TestCredentialsAreStoredSealed(t *testing.T) {
	t.Parallel()
	db := open(t)
	w := seed(t, db)
	keys := keyring(t)
	const key = "sk-test-0123456789abcdef-not-a-real-key"
	id := uuid.NewV7()
	at := secret.Location{Table: "suppliers.credentials", RowID: id.String(), Purpose: "key"}
	if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
		return queries.InsertCredential(ctx, tx, credential(w.supplier["platform"], w.group["platform"], id, keys.Seal(secret.New(key), at)))
	}); err != nil {
		t.Fatal(err)
	}
	var inTheClear, columns int
	var stored store.GetCredentialSecretRow
	if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) (err error) {
		// The key's bytes, as text or as hex, in any column of the row.
		err = tx.QueryRow(ctx, `SELECT count(*) FROM suppliers.credentials c WHERE position(convert_to($1, 'UTF8') IN c.ciphertext) > 0
			OR position(convert_to($1, 'UTF8') IN c.nonce) > 0 OR position(convert_to($1, 'UTF8') IN c.wrapped_dek) > 0
			OR strpos(c::text, $1) > 0 OR strpos(c::text, encode(convert_to($1, 'UTF8'), 'hex')) > 0`, key).Scan(&inTheClear)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'suppliers'
			AND table_name IN ('credentials', 'supplier_logins') AND column_name ~ '(key|secret|password|token)$'
			AND column_name NOT IN ('upstream_key_ref', 'key_version')`).Scan(&columns)
		if err != nil {
			return err
		}
		stored, err = queries.GetCredentialSecret(ctx, tx, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if inTheClear != 0 {
		t.Error("the stored row holds the key")
	}
	if columns != 0 {
		t.Error("a column is named as if it held a key in the clear")
	}
	read := secret.Sealed{Ciphertext: stored.Ciphertext, Nonce: stored.Nonce, WrappedDEK: stored.WrappedDek, KeyVersion: uint32(*stored.KeyVersion)} //nolint:gosec // the column holds 1 to 2³²-1
	if opened, err := keys.Open(read, at); err != nil || opened.Reveal() != key {
		t.Errorf("the key does not open at its own row: %v", err)
	}
	elsewhere := secret.Location{Table: "suppliers.credentials", RowID: uuid.NewV7().String(), Purpose: "key"}
	if _, err := keys.Open(read, elsewhere); err == nil {
		t.Error("the key opens at another row")
	}
}

// TestCredentialShapes: what is not the shape of sealed output, or is half sealed, is not stored; a credential still
// binding, or erased, has no key and any other has one; an erased one has none.
func TestCredentialShapes(t *testing.T) {
	t.Parallel()
	db := open(t)
	w := seed(t, db)
	good := keyring(t).Seal(secret.New("sk-test"), secret.Location{Table: "t", RowID: "r", Purpose: "p"})
	zero := int64(0)
	cases := []struct {
		name   string
		mutate func(*store.InsertCredentialParams)
		want   string
	}{
		{"sealed", func(*store.InsertCredentialParams) {}, ""},
		{"binding, no key yet", func(p *store.InsertCredentialParams) {
			p.Status, p.Ciphertext, p.Nonce, p.WrappedDek, p.KeyVersion = "binding", nil, nil, nil, nil
		}, ""},
		{"erased, with no key", func(p *store.InsertCredentialParams) {
			p.Status, p.Ciphertext, p.Nonce, p.WrappedDek, p.KeyVersion = "erased", nil, nil, nil, nil
		}, ""},
		{"erased, still with its key", func(p *store.InsertCredentialParams) { p.Status = "erased" }, checkViolation},
		{"active without a key", func(p *store.InsertCredentialParams) {
			p.Ciphertext, p.Nonce, p.WrappedDek, p.KeyVersion = nil, nil, nil, nil
		}, checkViolation},
		{"half sealed", func(p *store.InsertCredentialParams) { p.Nonce = nil }, checkViolation},
		{"a key as short as a plain one", func(p *store.InsertCredentialParams) { p.Ciphertext = []byte("sk-test") }, checkViolation},
		{"a nonce of the wrong length", func(p *store.InsertCredentialParams) { p.Nonce = []byte("short") }, checkViolation},
		{"a key version of zero", func(p *store.InsertCredentialParams) { p.KeyVersion = &zero }, checkViolation},
		{"an unknown status", func(p *store.InsertCredentialParams) { p.Status = "ok" }, checkViolation},
		{"no concurrency", func(p *store.InsertCredentialParams) { p.MaxConcurrency = 0 }, checkViolation},
	}
	for _, c := range cases {
		p := credential(w.supplier["platform"], w.group["platform"], uuid.NewV7(), good)
		c.mutate(&p)
		err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error { return queries.InsertCredential(ctx, tx, p) })
		if c.want == "" && err != nil || c.want != "" && pg.SQLState(err) != c.want {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// TestRowsAreNeverDeletedAndIdentityNeverChanges: the application has no DELETE anywhere but on a supplier's sealed
// login (a removal deletes it), and cannot change what a row is: its id, its supplier, its owner, the address credentials go to.
func TestRowsAreNeverDeletedAndIdentityNeverChanges(t *testing.T) {
	t.Parallel()
	db := open(t)
	w := seed(t, db)
	sealed := keyring(t).Seal(secret.New("sk-test"), secret.Location{Table: "t", RowID: "r", Purpose: "p"})
	if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
		if err := queries.InsertSupplierLogin(ctx, tx, store.InsertSupplierLoginParams{
			SupplierID: w.supplier["platform"], Kind: "pat", Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce,
			WrappedDek: sealed.WrappedDEK, KeyVersion: int64(sealed.KeyVersion),
		}); err != nil {
			return err
		}
		return queries.InsertCredential(ctx, tx, credential(w.supplier["platform"], w.group["platform"], uuid.NewV7(), sealed))
	}); err != nil {
		t.Fatal(err)
	}
	other, otherGroup := w.supplier["site A"], w.group["site A"]
	denied := []string{
		"DELETE FROM suppliers.suppliers", "DELETE FROM suppliers.supplier_groups",
		"DELETE FROM suppliers.credentials", "DELETE FROM suppliers.supplier_models", "DELETE FROM suppliers.supplier_aliases",
		"DELETE FROM suppliers.endpoints", "TRUNCATE suppliers.credentials",
		"UPDATE suppliers.suppliers SET origin = 'https://elsewhere.example'",
		"UPDATE suppliers.suppliers SET upstream_account = 'other'",
		"UPDATE suppliers.suppliers SET kind = 'other'",
		"UPDATE suppliers.suppliers SET owner_scope = 'platform', site_id = NULL",
		"UPDATE suppliers.suppliers SET id = gen_random_uuid()",
		fmt.Sprintf("UPDATE suppliers.supplier_groups SET supplier_id = '%s'", other),
		fmt.Sprintf("UPDATE suppliers.credentials SET supplier_id = '%s'", other),
		fmt.Sprintf("UPDATE suppliers.credentials SET supplier_group_id = '%s'", otherGroup),
		"UPDATE suppliers.credentials SET upstream_key_ref = 'other'",
		fmt.Sprintf("UPDATE suppliers.supplier_logins SET supplier_id = '%s'", other),
	}
	for _, statement := range denied {
		if err := exec(t, db, platform, statement); pg.SQLState(err) != insufficientPrivilege {
			t.Errorf("%s: %v, want permission denied", statement, err)
		}
	}
	for _, statement := range []string{
		"UPDATE suppliers.suppliers SET status = 'paused', config_gen = config_gen + 1",
		"UPDATE suppliers.credentials SET status = 'disabled'",
		"UPDATE suppliers.credentials SET max_concurrency = 2, requests_per_minute = 60",
		"UPDATE suppliers.supplier_logins SET version = version + 1",
		"DELETE FROM suppliers.supplier_logins",
	} {
		if err := exec(t, db, platform, statement); err != nil {
			t.Errorf("%s: %v", statement, err)
		}
	}
}

// TestRowRules: what a table itself refuses, case by case.
func TestRowRules(t *testing.T) {
	t.Parallel()
	db := open(t)
	w := seed(t, db)
	s := w.supplier["platform"]
	const g = "{group}" // replaced by a group made for the case, so that cases do not see each other's rows
	endpoint := func(columns, values string) string {
		return fmt.Sprintf(`INSERT INTO suppliers.endpoints (supplier_id, supplier_group_id, model_id, upstream_name, formats%s)
			VALUES ('%s', '%s', gen_random_uuid(), 'gpt', '{chat}'%s)`, columns, s, g, values)
	}
	supplier := func(origin, kind, currency string) string {
		return fmt.Sprintf(`INSERT INTO suppliers.suppliers (owner_scope, name, kind, origin, upstream_account, native_unit,
			settlement_currency, mgmt_egress_id) VALUES ('platform', 'n', '%s', '%s', 'me', 'usd', '%s', 'direct')`, kind, origin, currency)
	}
	group := func(set string) string {
		return fmt.Sprintf("UPDATE suppliers.supplier_groups SET %s WHERE id = '%s'", set, g)
	}
	bound := func(perMinute string) string {
		return fmt.Sprintf(`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, status,
			max_concurrency, requests_per_minute) VALUES (gen_random_uuid(), '%s', '%s', gen_random_uuid()::text, 'binding', 4, %s)`, s, g, perMinute)
	}
	supplierSet := func(set string) string {
		return fmt.Sprintf("UPDATE suppliers.suppliers SET %s WHERE id = '%s'", set, s)
	}
	cases := []struct {
		name       string
		statements []string
		want       string
	}{
		{"a negative balance", []string{supplierSet("balance_native = -13.0000000001")}, ""},
		{"a balance of NaN", []string{supplierSet("balance_native = 'NaN'")}, checkViolation},
		{"an infinite balance", []string{supplierSet("balance_native = 'Infinity'")}, checkViolation},
		{"a balance of minus infinity", []string{supplierSet("balance_native = '-Infinity'")}, checkViolation},
		{"a threshold of NaN", []string{supplierSet("low_balance_threshold = 'NaN'")}, checkViolation},
		{"an infinite threshold", []string{supplierSet("low_balance_threshold = 'Infinity'")}, checkViolation},
		{"a negative threshold", []string{supplierSet("low_balance_threshold = -1")}, checkViolation},
		{"a ratio of NaN", []string{group("ratio = 'NaN'")}, checkViolation},
		{"an infinite ratio", []string{group("ratio = 'Infinity'")}, checkViolation},
		{"a negative ratio", []string{group("ratio = -0.1")}, checkViolation},
		{"a ratio of zero", []string{group("ratio = 0")}, ""},
		{"a group selected has a codename", []string{group("selected = true, codename = 'maple-{group}'")}, ""},
		{"a group selected without one", []string{group("selected = true")}, checkViolation},
		{"a codename once set stays", []string{group("codename = 'maple-{group}'"), group("codename = 'oak-{group}'")}, checkViolation},
		{"a codename may be set again to itself", []string{group("codename = 'maple-{group}'"), group("codename = 'maple-{group}', present = false")}, ""},
		{"a known cost needs a schedule", []string{endpoint(", cost_state", ", 'known'")}, checkViolation},
		{"a credential with no requests a minute", []string{bound("NULL")}, ""},
		{"a requests a minute of zero", []string{bound("0")}, checkViolation},
		{"a requests a minute below zero", []string{bound("-1")}, checkViolation},
		{"a known cost with a schedule that does not exist", []string{endpoint(", cost_state, cost_schedule_id", ", 'known', gen_random_uuid()")}, foreignKeyViolation},
		{"a known cost with its schedule", []string{
			endpoint("", ""),
			"INSERT INTO suppliers.cost_schedules (supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint) " +
				"SELECT supplier_id, id, 'quota', '{}', 'published', 'f' FROM suppliers.endpoints WHERE supplier_group_id = '" + g + "'",
			"UPDATE suppliers.endpoints SET cost_state = 'known', cost_schedule_id = (SELECT id FROM suppliers.cost_schedules " +
				"WHERE endpoint_id = endpoints.id) WHERE supplier_group_id = '" + g + "'",
		}, ""},

		{"an unknown format", []string{strings.Replace(endpoint("", ""), "'{chat}'", "'{grpc}'", 1)}, checkViolation},
		{"no format", []string{strings.Replace(endpoint("", ""), "'{chat}'", "'{}'", 1)}, checkViolation},
		{"an https origin with a port", []string{supplier("https://x.example:8443", "newapi", "USD")}, ""},
		{"an origin with a path", []string{supplier("https://x.example/v1", "newapi", "USD")}, checkViolation},
		{"an origin on plain http", []string{supplier("http://x.example", "newapi", "USD")}, checkViolation},
		{"an origin in capitals", []string{supplier("https://X.example", "newapi", "USD")}, checkViolation},
		{"a kind with a capital", []string{supplier("https://x.example", "NewApi", "USD")}, checkViolation},
		{"a currency in lower case", []string{supplier("https://x.example", "newapi", "usd")}, checkViolation},
		{"one account twice at one origin", []string{supplier("https://x.example", "newapi", "USD"), supplier("https://x.example", "newapi", "USD")}, uniqueViolation},
	}
	for _, c := range cases {
		if err := inNewGroup(t, db, s, g, c.statements); c.want == "" && err != nil || c.want != "" && pg.SQLState(err) != c.want {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// inNewGroup makes a group of supplier and runs statements in one transaction with placeholder replaced by its id.
func inNewGroup(t *testing.T, db *pg.DB, supplier uuid.UUID, placeholder string, statements []string) error {
	t.Helper()
	return run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
		group, err := addGroup(ctx, tx, supplier)
		if err != nil {
			return err
		}
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, strings.ReplaceAll(statement, placeholder, group.String())); err != nil {
				return err
			}
		}
		return nil
	})
}

// TestEndpointsAreOnePerGroupAndModel: two endpoints of one group and model are one too many, and the queries the
// module's code uses fill the tables they say they do.
func TestEndpointsAreOnePerGroupAndModel(t *testing.T) {
	t.Parallel()
	db := open(t)
	w := seed(t, db)
	model := uuid.NewV7()
	add := func() error {
		return run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
			_, err := queries.InsertEndpoint(ctx, tx, store.InsertEndpointParams{
				SupplierID: w.supplier["platform"], SupplierGroupID: w.group["platform"], ModelID: model, UpstreamName: "gpt", Formats: []string{"chat"},
			})
			return err
		})
	}
	if err := add(); err != nil {
		t.Fatal(err)
	}
	if err := add(); pg.SQLState(err) != uniqueViolation {
		t.Errorf("a second endpoint for one group and model: %v, want a unique violation", err)
	}
}
