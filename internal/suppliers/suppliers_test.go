package suppliers

import (
	"strings"
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
)

type fakeAdapter struct {
	adapter.Adapter
	kind adapter.Kind
}

func (f fakeAdapter) Kind() adapter.Kind          { return f.kind }
func (fakeAdapter) Buckets() []adapter.BucketSpec { return nil }

// TestStartupRefusesSuppliersWithoutAnAdapter: a supplier still in use whose kind this program has no adapter for stops
// the start and is named; a removed one, and kinds that are registered, do not.
func TestStartupRefusesSuppliersWithoutAnAdapter(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t, Module())
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	platform := pg.PlatformScope()
	r, err := registry.New(fakeAdapter{kind: "newapi"})
	if err != nil {
		t.Fatal(err)
	}
	add := func(name, kind, status string) {
		t.Helper()
		if err := addSupplier(t, db, name, kind, status); err != nil {
			t.Fatal(err)
		}
	}
	check := func() error { return checkAdapters(t.Context(), db, platform, r) }
	if err := check(); err != nil {
		t.Fatalf("no suppliers: %v", err)
	}
	add("known", "newapi", "active")
	add("gone", "oldsystem", "removed")
	if err := check(); err != nil {
		t.Fatalf("a registered kind and a removed supplier: %v", err)
	}
	add("orphan", "sub2api", "active")
	add("paused", "ghost", "paused")
	err = check()
	for _, want := range []string{`"orphan" (kind "sub2api")`, `"paused" (kind "ghost")`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not name %s", err, want)
		}
	}
	if err != nil && (strings.Contains(err.Error(), "known") || strings.Contains(err.Error(), "oldsystem")) {
		t.Errorf("error %v names a supplier that is fine", err)
	}
}

// addSupplier inserts a platform supplier of the given kind and sets its status.
func addSupplier(t *testing.T, db *pg.DB, name, kind, status string) error {
	t.Helper()
	return db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		id, err := store.New().InsertSupplier(t.Context(), tx, store.InsertSupplierParams{
			OwnerScope: "platform", Name: name, Kind: kind, Origin: "https://" + name + ".example", UpstreamAccount: "me",
			NativeUnit: "quota", SettlementCurrency: "USD", MgmtEgressID: "direct",
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), "UPDATE suppliers.suppliers SET status = $2 WHERE id = $1", id, status)
		return err
	})
}

// TestTheModuleIsMigrated: the module's name and files are what pg.Migrate expects.
func TestTheModuleIsMigrated(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t, Module())
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Check(t.Context(), Module()); err != nil {
		t.Fatal(err)
	}
}
