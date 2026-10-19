package accounts_test

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestUpgradeRegistersExistingTenants: a database holding sites, accounts and keys from before the tenant registry
// is brought up to this version, every table's row-level security forced again, after which an account's keys are made, disabled and deleted in its own scope, each
// with its audit entry. Not parallel: Migrate grants roles cluster-wide, which a template built at the same time
// would race on (pg's TestMigrateRunsTakeTurns).
func TestUpgradeRegistersExistingTenants(t *testing.T) {
	database := pgtest.New(t)
	if err := pg.Migrate(t.Context(), database.Migrator, migrationsBefore(t, "00003")); err != nil {
		t.Fatal(err)
	}
	old := seed[:len(seed)-1] // the last statement registers the tenants by hand
	if !strings.Contains(seed[len(seed)-1], "platform.tenants") {
		t.Fatal("the seed's last statement no longer registers the tenants")
	}
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(strings.Join(old, ";\n"))); err != nil {
		t.Fatal(err)
	}
	if err := pg.Migrate(t.Context(), database.Migrator, accounts.Module()); err != nil {
		t.Fatal(err)
	}
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Check(t.Context(), accounts.Module()); err != nil {
		t.Fatalf("after the upgrade: %v", err) // every table forced again
	}
	s := node(t, db, hashKeys(t, 1), time.Now)
	scope := pg.NewScope(false, nil, []uuid.UUID{a1})
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		created, err := s.CreateKey(t.Context(), tx, system, accounts.KeyRequest{
			Site: accounts.SiteID(s1), Account: accounts.AccountID(a1), Workspace: accounts.WorkspaceID(w1),
			CreatedBy: accounts.UserID(u1), Name: "After the upgrade",
		})
		if err != nil {
			return err
		}
		if err := disableKey(t.Context(), tx, s, created.ID); err != nil {
			return err
		}
		return s.DeleteKey(t.Context(), tx, system, accounts.KeyID(k1))
	}); err != nil {
		t.Fatalf("key changes in an account made before the upgrade: %v", err)
	}
	if got := texts(t, db, platform, "SELECT action FROM platform.audit_log ORDER BY id"); strings.Join(got, " ") != "api_key.create api_key.update api_key.delete" {
		t.Errorf("audit entries %v", got)
	}
}

// migrationsBefore is this module's migrations numbered below version.
func migrationsBefore(t *testing.T, version string) pg.Module {
	t.Helper()
	all, before := accounts.Module().Migrations, fstest.MapFS{}
	paths, err := fs.Glob(all, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if path >= version {
			continue
		}
		data, err := fs.ReadFile(all, path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = &fstest.MapFile{Data: data}
	}
	return pg.Module{Name: "accounts", Migrations: before}
}
