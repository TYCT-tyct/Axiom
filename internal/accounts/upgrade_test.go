package accounts_test

import (
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestBaselineTenantKeyAudit exercises key changes and their audit entries after a fresh baseline.
func TestBaselineTenantKeyAudit(t *testing.T) {
	database := pgtest.New(t, accounts.Module())
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(strings.Join(seed, ";\n"))); err != nil {
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
		t.Fatalf("after the baseline: %v", err) // every table forced again
	}
	s := node(t, db, hashKeys(t, 1), time.Now)
	scope := pg.NewScope(false, nil, []uuid.UUID{a1})
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		created, err := s.CreateKey(t.Context(), tx, system, accounts.KeyRequest{
			Site: accounts.SiteID(s1), Account: accounts.AccountID(a1), Workspace: new(accounts.WorkspaceID(w1)),
			CreatedBy: accounts.UserID(u1), Name: "After the baseline",
		})
		if err != nil {
			return err
		}
		if err := disableKey(t.Context(), tx, s, created.ID); err != nil {
			return err
		}
		return s.DeleteKey(t.Context(), tx, system, accounts.KeyID(k1))
	}); err != nil {
		t.Fatalf("key changes after the baseline: %v", err)
	}
	if got := texts(t, db, platform, "SELECT action FROM platform.audit_log ORDER BY id"); strings.Join(got, " ") != "api_key.create api_key.update api_key.delete" {
		t.Errorf("audit entries %v", got)
	}
}
