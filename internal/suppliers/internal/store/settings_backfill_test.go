package store_test

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers/internal/store"
)

// TestSettingsBackfillTheFormOfACredentialConnectedBefore: a Bedrock credential stored before credentials had settings
// was an access key, the only form there was; Bedrock now has two, and a node loads a credential of a type with more
// than one form only when its settings name the form. So the migration that adds the column names the form of each
// such credential that is not erased, and of no other.
func TestSettingsBackfillTheFormOfACredentialConnectedBefore(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t)
	ctx := t.Context()
	if err := pg.Migrate(ctx, database.Migrator, pg.Module{Name: "suppliers", Migrations: migrationsBefore(t, "00056")}); err != nil {
		t.Fatal(err)
	}
	if err := pgtest.Run(ctx, database.Superuser,
		`INSERT INTO suppliers.suppliers (id, owner_scope, name, kind, origin, upstream_account, native_unit, settlement_currency, mgmt_egress_id, status, mode)
		VALUES ('01a2163e-0000-7000-8000-000000000001', 'platform', 'b', 'bedrock', 'https://bedrock.amazonaws.com', 'a', 'usd', 'USD', 'direct', 'active', 'cloud'),
			('01a2163e-0000-7000-8000-000000000002', 'platform', 'o', 'openai', 'https://api.openai.com', 'o', 'usd', 'USD', 'direct', 'active', 'direct')`,
		`INSERT INTO suppliers.supplier_groups (id, supplier_id, upstream_ref, name, ratio)
		VALUES ('01a2163e-0000-7000-8000-000000000011', '01a2163e-0000-7000-8000-000000000001', 'line', 'line', 1),
			('01a2163e-0000-7000-8000-000000000012', '01a2163e-0000-7000-8000-000000000002', 'line', 'line', 1)`,
		`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, status, max_concurrency, region, hint)
		VALUES ('01a2163e-0000-7000-8000-000000000021', '01a2163e-0000-7000-8000-000000000001', '01a2163e-0000-7000-8000-000000000011', 'east', 'binding', 1, 'us-east-1', 'live'),
			('01a2163e-0000-7000-8000-000000000022', '01a2163e-0000-7000-8000-000000000001', '01a2163e-0000-7000-8000-000000000011', 'west', 'erased', 1, 'us-west-2', 'gone'),
			('01a2163e-0000-7000-8000-000000000023', '01a2163e-0000-7000-8000-000000000002', '01a2163e-0000-7000-8000-000000000012', 'key', 'binding', 1, NULL, 'open')`,
	); err != nil {
		t.Fatal(err)
	}
	if err := pg.Migrate(ctx, database.Migrator, pg.Module{Name: "suppliers", Migrations: store.Migrations()}); err != nil {
		t.Fatal(err)
	}
	db := openApp(t, database.App)
	defer db.Close()
	var got string
	if err := db.ReadTx(ctx, platform, func(tx pg.Tx) error {
		return tx.QueryRow(ctx, `SELECT string_agg(hint || ' ' || coalesce(settings::text, 'none'), ', ' ORDER BY hint) FROM suppliers.credentials`).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	if want := `gone none, live {"form": "access_key"}, open none`; got != want {
		t.Errorf("the credentials' settings after the migration are %q, want %q", got, want)
	}
}

// migrationsBefore is the module's migrations numbered below version.
func migrationsBefore(t *testing.T, version string) fs.FS {
	t.Helper()
	all := store.Migrations()
	entries, err := fs.ReadDir(all, ".")
	if err != nil {
		t.Fatal(err)
	}
	before := fstest.MapFS{}
	for _, e := range entries {
		if strings.Compare(e.Name(), version) >= 0 {
			continue
		}
		b, err := fs.ReadFile(all, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		before[e.Name()] = &fstest.MapFile{Data: b}
	}
	return before
}
