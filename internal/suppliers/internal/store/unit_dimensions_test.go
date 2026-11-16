package store_test

import (
	"context"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers/internal/store"
)

func TestBaselineSnapshotsUnitValueDimensions(t *testing.T) {
	database := pgtest.New(t, pg.Module{Name: "suppliers", Migrations: store.Migrations()})
	db := openApp(t, database.App)
	w := seed(t, db)
	if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.suppliers SET settlement_currency = 'CNY', native_unit = 'usd' WHERE id = $1", w.supplier["account A1"])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	insert := func(id uuid.UUID) error {
		return run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
			return queries.InsertUnitValue(ctx, tx, store.InsertUnitValueParams{SupplierID: id, Value: "7.3", Actor: "test", Reason: "confirmed"})
		})
	}
	for _, id := range w.supplier {
		if err := insert(id); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if err := pg.Migrate(t.Context(), database.Migrator, pg.Module{Name: "suppliers", Migrations: store.Migrations()}); err != nil {
		t.Fatal(err)
	}
	db = openApp(t, database.App)
	defer db.Close()
	for name, id := range w.supplier {
		if err := insert(id); err != nil {
			t.Fatalf("%s insert after migration rerun: %v", name, err)
		}
		var count int
		if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM suppliers.unit_values u JOIN suppliers.suppliers s ON s.id = u.supplier_id
				WHERE s.id = $1 AND u.currency = s.settlement_currency AND u.native_unit = s.native_unit AND u.value = 7.3`, id).Scan(&count)
		}); err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Errorf("%s: %d rows with dimensions, want both inserted rows", name, count)
		}
	}
}
