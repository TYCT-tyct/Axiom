package store_test

import (
	"context"
	"database/sql" //nolint:depguard // goose's Down is the only way to exercise a rollback in this package
	"strings"
	"testing"
	"uuid"

	_ "github.com/jackc/pgx/v5/stdlib" //nolint:depguard // the driver for goose's database/sql handle in this test

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers/internal/store"
)

// TestDownDirectFormatsRefusesLiveRows: 00020's Down explains what to clean instead of failing on its CHECK, and runs
// once the rows are gone. The DO block is one goose statement, so its internal semicolon cannot split the file.
func TestDownDirectFormatsRefusesLiveRows(t *testing.T) {
	pgdb := pgtest.New(t, pg.Module{Name: "suppliers", Migrations: store.Migrations()})
	db := openApp(t, pgdb.App)
	defer db.Close()
	var supplier, group uuid.UUID
	if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) (err error) {
		if supplier, err = addSupplier(ctx, tx, owners["platform"]); err != nil {
			return err
		}
		if group, err = addGroup(ctx, tx, supplier); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO suppliers.supplier_models (supplier_id, supplier_group_id, upstream_name, model_id, formats)
			VALUES ($1, $2, 'direct', $3, ARRAY['embeddings'])`, supplier, group, uuid.NewV7())
		return err
	}); err != nil {
		t.Fatal(err)
	}

	admin, err := sql.Open("pgx", pgdb.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := admin.Close(); err != nil {
			t.Errorf("close the migration handle: %v", err)
		}
	}()
	version, err := database.NewStore(database.DialectPostgres, "platform.goose_suppliers")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider("", admin, store.Migrations(),
		goose.WithStore(version), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(t.Context(), 19); err == nil || !strings.Contains(err.Error(), "cannot roll back P18") {
		t.Fatalf("Down with a direct-format row = %v, want the explicit refusal", err)
	}

	if _, err := admin.ExecContext(t.Context(), `DELETE FROM suppliers.supplier_models WHERE supplier_id = $1`, supplier); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(t.Context(), 19); err != nil {
		t.Fatalf("Down after the row was removed: %v", err)
	}
}
