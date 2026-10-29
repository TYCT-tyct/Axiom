package accounts_test

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

func TestBusinessRateSettingsAndAdmission(t *testing.T) {
	t.Parallel()
	db, _, svc, text := callSetup(t)
	caller, err := svc.AuthenticateKey(t.Context(), accounts.SiteID(s1), text)
	if err != nil {
		t.Fatal(err)
	}
	tierID := uuid.UUID{}
	if err := db.ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT tier_id FROM accounts.accounts WHERE id = $1", a1).Scan(&tierID)
	}); err != nil {
		t.Fatal(err)
	}
	checkBusinessRates(t, svc, caller, accounts.DefaultRateRequests, accounts.DefaultRateRequests)
	for _, tc := range []struct {
		target       string
		id           uuid.UUID
		value        *int32
		account, key int
	}{
		{"tier", tierID, new(int32(2)), 2, 2},
		{"account", a1, new(int32(5)), 5, 5},
		{"key", uuid.UUID(caller.KeyID()), new(int32(1)), 5, 1},
		{"account", a1, new(int32(0)), 0, 1},
		{"key", uuid.UUID(caller.KeyID()), nil, 0, 0},
		{"account", a1, nil, 2, 2},
	} {
		if err := setBusinessRate(t, db, svc, tc.target, tc.id, tc.value); err != nil {
			t.Fatal(err)
		}
		checkBusinessRates(t, svc, caller, tc.account, tc.key)
	}
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		if err := svc.SetRateLimit(t.Context(), tx, system, "account", a1, new(int32(9)), "capacity"); err != nil {
			return err
		}
		return errors.New("abort")
	}); err == nil {
		t.Fatal("expected abort")
	}
	checkBusinessRates(t, svc, caller, 2, 2)
	for _, value := range []int32{-1, 1_000_000_001} {
		var setting *accounts.SettingError
		if err := setBusinessRate(t, db, svc, "key", uuid.UUID(caller.KeyID()), new(value)); !errors.As(err, &setting) {
			t.Fatalf("invalid %d: %v", value, err)
		}
	}
	if err := setBusinessRate(t, db, svc, "account", uuid.NewV7(), new(int32(1))); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("missing target: %v", err)
	}
	// Replacing the old key settings cannot erase a business allowance owned by the administrator.
	if err := setBusinessRate(t, db, svc, "key", uuid.UUID(caller.KeyID()), new(int32(1))); err != nil {
		t.Fatal(err)
	}
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := svc.UpdateKey(t.Context(), tx, system, caller.KeyID(), accounts.KeySettings{Name: "old client"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	checkBusinessRates(t, svc, caller, 2, 1)
}

func setBusinessRate(t *testing.T, db *pg.DB, svc *accounts.Service, target string, id uuid.UUID, value *int32) error {
	t.Helper()
	return db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return svc.SetRateLimit(t.Context(), tx, system, target, id, value, "capacity")
	})
}

func checkBusinessRates(t *testing.T, svc *accounts.Service, caller accounts.Caller, wantAccount, wantKey int) {
	t.Helper()
	err := svc.Admit(t.Context(), caller, accounts.AdmitRequest{Domain: apiDomain, ClientIP: client}, func(_ pg.Tx, a accounts.Admission) error {
		account, key := a.RateLimits()
		if account != wantAccount || key != wantKey {
			t.Errorf("limits %d/%d, want %d/%d", account, key, wantAccount, wantKey)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRateTierVisibilityIsOnlyTheAssignedTier(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	if _, err := exec(t, db, platform, ids.Replace(`INSERT INTO accounts.tiers(id,site_id,name) VALUES(:x,:s1,'Other')`)); err != nil {
		t.Fatal(err)
	}
	scope := pg.NewScope(false, nil, []uuid.UUID{a1})
	rows := texts(t, db, scope, "SELECT name FROM accounts.tiers")
	if len(rows) != 1 || rows[0] != "Standard" {
		t.Fatalf("assigned tiers: %v", rows)
	}
	n, err := exec(t, db, scope, "UPDATE accounts.tiers SET rate_requests = 1")
	if err != nil || n != 0 {
		t.Fatalf("account wrote tier: %d %v", n, err)
	}
}
