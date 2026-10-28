package store_test

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers/internal/store"
)

// migrationsThrough is the module's migrations up to and including last, to build a database at an earlier version.
func migrationsThrough(t *testing.T, last string) fs.FS {
	t.Helper()
	src := store.Migrations()
	entries, err := fs.ReadDir(src, ".")
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() > last {
			continue
		}
		data, err := fs.ReadFile(src, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = &fstest.MapFile{Data: data}
	}
	return out
}

func openApp(t *testing.T, url string) *pg.DB {
	t.Helper()
	db, err := pg.Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// seedAboveCap makes a credential as an old node could have left it before 00011, above the cap.
func seedAboveCap(t *testing.T, db *pg.DB) uuid.UUID {
	t.Helper()
	var supplier, group, credential uuid.UUID
	if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
		var err error
		if supplier, err = addSupplier(ctx, tx, owners["platform"]); err != nil {
			return err
		}
		if group, err = addGroup(ctx, tx, supplier); err != nil {
			return err
		}
		credential = uuid.NewV7()
		if err := queries.InsertCredential(ctx, tx, store.InsertCredentialParams{
			ID: credential, SupplierID: supplier, SupplierGroupID: group, UpstreamKeyRef: "old", Status: "binding", MaxConcurrency: 4,
		}); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE suppliers.credentials SET requests_per_minute = 100001 WHERE id = $1", credential)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return credential
}

func readLimit(t *testing.T, db *pg.DB, credential uuid.UUID) *int32 {
	t.Helper()
	var got *int32
	if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
		return tx.QueryRow(ctx, "SELECT requests_per_minute FROM suppliers.credentials WHERE id = $1", credential).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func setLimit(t *testing.T, db *pg.DB, credential uuid.UUID, n int) error {
	t.Helper()
	return run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.credentials SET requests_per_minute = $2 WHERE id = $1", credential, n)
		return err
	})
}

// TestUpgradeClampsRequestsPerMinuteAboveTheCap: a database at 00010 may hold a value an old node wrote above the cap.
// The upgrade must see that row through forced RLS and lower it; otherwise the later CHECK could not be added, and the
// row would keep an unbounded ceiling. The old CHECK still accepts such a write during the rolling release.
func TestUpgradeClampsRequestsPerMinuteAboveTheCap(t *testing.T) {
	database := pgtest.New(t)
	old := pg.Module{Name: "suppliers", Migrations: migrationsThrough(t, "00010_test_costs.sql")}
	if err := pg.Migrate(t.Context(), database.Migrator, old); err != nil {
		t.Fatal(err)
	}
	db := openApp(t, database.App)
	credential := seedAboveCap(t, db)
	db.Close()

	if err := pg.Migrate(t.Context(), database.Migrator, pg.Module{Name: "suppliers", Migrations: store.Migrations()}); err != nil {
		t.Fatal(err)
	}
	db = openApp(t, database.App)
	defer db.Close()
	if got := readLimit(t, db, credential); got == nil || *got != 100000 {
		t.Fatalf("the upgrade left requests_per_minute at %v, want 100000", got)
	}
	if err := setLimit(t, db, credential, 100001); err != nil {
		t.Fatalf("an old node's write above the cap failed during the rolling release: %v", err)
	}
}
