package pg_test

import (
	"strings"
	"testing"
	"testing/fstest"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// tenantTxMigration is a module that carries one tenant's row and the lookup that names its tenant, the shape
// internal/platform/mail's outbox has: the row is written in the tenant's scope, and a job that holds only its id finds
// the tenant through a SECURITY DEFINER function owned by axiom_lookup.
const tenantTxMigration = `-- +goose Up
CREATE TABLE accounts.notes (id uuid PRIMARY KEY, site_id uuid NOT NULL, account_id uuid, body text NOT NULL);
ALTER TABLE accounts.notes ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON accounts.notes USING (platform.in_scope(site_id, account_id));
CREATE POLICY lookup ON accounts.notes FOR SELECT TO axiom_lookup USING (true);
GRANT USAGE ON SCHEMA accounts TO axiom_app, axiom_lookup;
GRANT SELECT, INSERT, DELETE ON accounts.notes TO axiom_app;
GRANT SELECT (id, site_id, account_id) ON accounts.notes TO axiom_lookup;
CREATE FUNCTION accounts.note_tenant(id uuid) RETURNS TABLE (site_id uuid, account_id uuid)
	LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
	AS $$ SELECT n.site_id, n.account_id FROM accounts.notes n WHERE n.id = note_tenant.id $$;
REVOKE EXECUTE ON FUNCTION accounts.note_tenant FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accounts.note_tenant TO axiom_app;
GRANT CREATE ON SCHEMA accounts TO axiom_lookup;
ALTER FUNCTION accounts.note_tenant OWNER TO axiom_lookup;
REVOKE CREATE ON SCHEMA accounts FROM axiom_lookup;
`

// note is one row of the module's table.
type note struct {
	id      uuid.UUID
	site    uuid.UUID
	account *uuid.UUID
	body    string
}

// createNotes migrates the module into a database of its own and writes the rows.
func createNotes(t *testing.T) (db *pg.DB, notes []note) {
	t.Helper()
	database := pgtest.New(t)
	module := pg.Module{Name: "accounts", Migrations: fstest.MapFS{"00001_notes.sql": {Data: []byte(tenantTxMigration)}}}
	if err := pg.Migrate(t.Context(), database.Migrator, module); err != nil {
		t.Fatal(err)
	}
	site, otherSite := uuid.NewV7(), uuid.NewV7()
	account, otherAccount := uuid.NewV7(), uuid.NewV7()
	notes = []note{
		{uuid.NewV7(), site, &account, "the account's note"},
		{uuid.NewV7(), site, nil, "the site's note"},
		{uuid.NewV7(), otherSite, &otherAccount, "another tenant's note"},
	}
	for _, row := range notes {
		accountValue := "NULL"
		if row.account != nil {
			accountValue = quote(row.account.String())
		}
		if err := pgtest.Run(t.Context(), database.Superuser, "INSERT INTO accounts.notes VALUES ("+
			quote(row.id.String())+", "+quote(row.site.String())+", "+accountValue+", "+quote(row.body)+")"); err != nil {
			t.Fatal(err)
		}
	}
	return open(t, database.App), notes
}

// quote writes a value as a SQL string literal, the way the seeding statements of the other tests do: the apostrophe is
// doubled, so a value of the test's choosing cannot end the literal.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// TestTenantTx: a transaction that starts with an id alone reads the row of the tenant that id belongs to — the
// account's row for an account, the site's for the site itself — and nothing else. Migrate grants roles cluster-wide,
// so this test does not run in parallel (see TestMigrateRunsTakeTurns).
func TestTenantTx(t *testing.T) {
	db, notes := createNotes(t)
	for _, row := range notes {
		var body string
		found, err := db.TenantTx(t.Context(), "accounts.note_tenant", row.id, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "SELECT body FROM accounts.notes").Scan(&body)
		})
		if err != nil || !found || body != row.body {
			t.Errorf("TenantTx(%s) read %q, %v, %v; want %q", row.id, body, found, err, row.body)
		}
	}
}

// TestTenantTxSetsTheRowsOwnScope: the scope the lookup sets holds the row's tenant alone, so the transaction that reads
// one row cannot read the others, and an id the lookup does not find runs nothing at all.
func TestTenantTxSetsTheRowsOwnScope(t *testing.T) {
	db, notes := createNotes(t)
	var bodies []string
	found, err := db.TenantTx(t.Context(), "accounts.note_tenant", notes[0].id, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), "SELECT body FROM accounts.notes")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body string
			if err := rows.Scan(&body); err != nil {
				return err
			}
			bodies = append(bodies, body)
		}
		return rows.Err()
	})
	if err != nil || !found {
		t.Fatalf("TenantTx() = %v, %v", found, err)
	}
	if len(bodies) != 1 || bodies[0] != notes[0].body {
		t.Errorf("the account's scope read %v; want only %q", bodies, notes[0].body)
	}

	var ran bool
	found, err = db.TenantTx(t.Context(), "accounts.note_tenant", uuid.NewV7(), func(pg.Tx) error { ran = true; return nil })
	if err != nil || found || ran {
		t.Errorf("TenantTx() on an id with no row = %v, %v, ran %v; want it not found", found, err, ran)
	}
}

// TestTenantTxRefusesAMalformedLookup: the lookup's name comes from the module, and a name that is not schema.function
// fails before the lookup runs.
func TestTenantTxRefusesAMalformedLookup(t *testing.T) {
	t.Parallel()
	db := open(t, pgtest.New(t).App)
	for _, lookup := range []string{"", "note_tenant", "accounts.", ".note_tenant", "accounts.note_tenant("} {
		var ran bool
		found, err := db.TenantTx(t.Context(), lookup, uuid.NewV7(), func(pg.Tx) error { ran = true; return nil })
		if err == nil || found || ran {
			t.Errorf("TenantTx(%q) = %v, %v, ran %v; want an error before the transaction", lookup, found, err, ran)
		}
	}
}
