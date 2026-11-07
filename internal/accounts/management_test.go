package accounts_test

import (
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func managementKey(t *testing.T, db *pg.DB, s *accounts.Service, settings accounts.KeySettings) accounts.CreatedKey {
	t.Helper()
	var created accounts.CreatedKey
	expiry := time.Now().UTC().Add(time.Hour)
	inPlatform(t, db, func(tx pg.Tx) (err error) {
		created, err = s.CreateKey(t.Context(), tx, system, accounts.KeyRequest{
			Kind: accounts.ManagementKey, Site: accounts.SiteID(s1), Account: accounts.AccountID(a1),
			Workspace: new(accounts.WorkspaceID(w1)), CreatedBy: accounts.UserID(u1), ExpiresAt: &expiry, KeySettings: settings,
		})
		return err
	})
	return created
}

func manage(t *testing.T, s *accounts.Service, key string, fn func(accounts.KeyManager, pg.Tx) error) error {
	t.Helper()
	c, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key)
	if err != nil {
		return err
	}
	return s.WithKeyManager(t.Context(), c, accounts.AdmitRequest{Domain: apiDomain, ClientIP: client}, fn)
}

func wantManagementError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestManagementCRUDAndAudit(t *testing.T) {
	t.Parallel()
	db, _, s, _ := callSetup(t)
	manager := managementKey(t, db, s, accounts.KeySettings{Name: "manager"})
	var child accounts.CreatedKey
	err := manage(t, s, manager.Text.Reveal(), func(m accounts.KeyManager, tx pg.Tx) error {
		settings := m.Ceiling().KeySettings
		settings.Name = "child"
		var err error
		child, err = m.Create(t.Context(), nil, nil, settings)
		wantManagementError(t, err, nil)
		key, err := m.Get(t.Context(), child.ID)
		wantManagementError(t, err, nil)
		if key.Kind != accounts.CallKey || key.SpendLimit != nil || key.ExpiresAt == nil {
			t.Fatalf("child: %+v", key)
		}
		settings.Disabled = true
		_, err = m.Update(t.Context(), child.ID, settings)
		wantManagementError(t, err, nil)
		keys, err := m.List(t.Context(), accounts.ManagedKeyPage{IncludeDisabled: true})
		wantManagementError(t, err, nil)
		for _, key := range keys {
			if key.Kind != accounts.CallKey {
				t.Fatal("listed a management key")
			}
		}
		wantManagementError(t, m.Delete(t.Context(), child.ID), nil)
		_, err = m.Get(t.Context(), child.ID)
		wantManagementError(t, err, accounts.ErrKeyNotFound)
		return nil
	})
	wantManagementError(t, err, nil)
	wantManagementError(t, call(t, s, s1, child.Text.Reveal(), apiDomain), accounts.ErrUnknownKey)
	rows := texts(t, db, platform, `SELECT actor_kind || ':' || actor_id || ':' || action FROM platform.audit_log WHERE actor_kind='management_key' ORDER BY id`)
	actor := "management_key:" + uuid.UUID(manager.ID).String() + ":"
	expected := []string{actor + "api_key.create", actor + "api_key.update", actor + "api_key.delete"}
	if !slices.Equal(rows, expected) {
		t.Fatalf("audit: %v, want %v", rows, expected)
	}
}

func TestManagementKeyTypeAndScope(t *testing.T) {
	t.Parallel()
	db, _, s, callKey := callSetup(t)
	manager := managementKey(t, db, s, accounts.KeySettings{Name: "manager"})
	noop := func(accounts.KeyManager, pg.Tx) error { return nil }
	if err := manage(t, s, callKey, noop); !errors.Is(err, accounts.ErrUnknownKey) {
		t.Fatalf("call key managed: %v", err)
	}
	if err := call(t, s, s1, manager.Text.Reveal(), apiDomain); !errors.Is(err, accounts.ErrUnknownKey) {
		t.Fatalf("management key called: %v", err)
	}
	err := manage(t, s, manager.Text.Reveal(), func(m accounts.KeyManager, tx pg.Tx) error {
		for _, id := range []accounts.KeyID{manager.ID, accounts.KeyID(k2)} {
			_, err := m.Get(t.Context(), id)
			wantManagementError(t, err, accounts.ErrKeyNotFound)
			_, err = m.Update(t.Context(), id, m.Ceiling().KeySettings)
			wantManagementError(t, err, accounts.ErrKeyNotFound)
			wantManagementError(t, m.Delete(t.Context(), id), accounts.ErrKeyNotFound)
		}
		_, err := m.Create(t.Context(), new(accounts.WorkspaceID(w2)), nil, m.Ceiling().KeySettings)
		wantManagementError(t, err, accounts.ErrKeyNotFound)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WithKeyManager(t.Context(), accounts.Caller{}, accounts.AdmitRequest{}, noop); !errors.Is(err, accounts.ErrUnknownKey) {
		t.Fatal(err)
	}
}

func TestManagementDelegationNeverExpands(t *testing.T) {
	t.Parallel()
	db, _, s, _ := callSetup(t)
	limit := int64(100)
	manager := managementKey(t, db, s, accounts.KeySettings{
		Name: "manager", SpendLimit: &limit, LimitReset: accounts.ResetDaily, Models: []string{"a", "b"},
		IPAllow: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")},
	})
	cases := []struct {
		name   string
		change func(*accounts.KeySettings, **time.Time)
	}{
		{"larger limit", func(k *accounts.KeySettings, _ **time.Time) { n := int64(101); k.SpendLimit = &n }},
		{"no limit", func(k *accounts.KeySettings, _ **time.Time) { k.SpendLimit = nil; k.LimitReset = "" }},
		{"different reset", func(k *accounts.KeySettings, _ **time.Time) { k.LimitReset = accounts.ResetMonthly }},
		{"all models", func(k *accounts.KeySettings, _ **time.Time) { k.Models = nil }},
		{"another model", func(k *accounts.KeySettings, _ **time.Time) { k.Models = []string{"other"} }},
		{"all IPs", func(k *accounts.KeySettings, _ **time.Time) { k.IPAllow = nil }},
		{"wider IP range", func(k *accounts.KeySettings, _ **time.Time) {
			k.IPAllow = []netip.Prefix{netip.MustParsePrefix("203.0.0.0/16")}
		}},
		{"other IP family", func(k *accounts.KeySettings, _ **time.Time) {
			k.IPAllow = []netip.Prefix{netip.MustParsePrefix("::/0")}
		}},
		{"longer expiry", func(_ *accounts.KeySettings, expiry **time.Time) {
			later := (*expiry).Add(time.Second)
			*expiry = &later
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := manage(t, s, manager.Text.Reveal(), func(m accounts.KeyManager, tx pg.Tx) error {
				parent := m.Ceiling()
				child, err := m.Create(t.Context(), nil, nil, parent.KeySettings)
				if err != nil {
					return err
				}
				settings, expiry := parent.KeySettings, parent.ExpiresAt
				tc.change(&settings, &expiry)
				_, err = m.Create(t.Context(), nil, expiry, settings)
				wantManagementError(t, err, accounts.ErrForbidden)
				if tc.name != "longer expiry" {
					_, err = m.Update(t.Context(), child.ID, settings)
					wantManagementError(t, err, accounts.ErrForbidden)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := manage(t, s, manager.Text.Reveal(), func(m accounts.KeyManager, tx pg.Tx) error {
		k := m.Ceiling().KeySettings
		k.Models = []string{"a"}
		k.IPAllow = []netip.Prefix{netip.MustParsePrefix("203.0.113.7/32")}
		n := int64(50)
		k.SpendLimit = &n
		_, err := m.Create(t.Context(), nil, nil, k)
		return err
	}); err != nil {
		t.Fatal("subset refused", err)
	}
}

func TestManagementRateAndAuditFailure(t *testing.T) {
	t.Parallel()
	db, database, a, _ := callSetup(t)
	b := node(t, db, hashKeys(t, 1), nil)
	manager := managementKey(t, db, a, accounts.KeySettings{Name: "manager"})
	if err := pgtest.Run(t.Context(), database.Superuser, `ALTER TABLE platform.audit_log ADD CONSTRAINT reject_management CHECK(actor_kind <> 'management_key') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if err := manage(t, a, manager.Text.Reveal(), func(m accounts.KeyManager, tx pg.Tx) error {
		_, err := m.Create(t.Context(), nil, nil, m.Ceiling().KeySettings)
		return err
	}); err == nil {
		t.Fatal("unaudited create committed")
	}
	if got := texts(t, db, platform, `SELECT count(*)::text FROM accounts.api_keys WHERE name='manager'`)[0]; got != "1" {
		t.Fatal("audit failure retained child", got)
	}
	// A future window makes the exhaustion assertion independent of minute-boundary timing (also fails closed after a clock rollback).
	inPlatform(t, db, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE accounts.api_keys SET management_window=date_trunc('minute', statement_timestamp()) + interval '1 day', management_requests=59 WHERE id=$1`, uuid.UUID(manager.ID))
		return err
	})
	// Explicitly hold the same database minute throughout this rate assertion by fixing the stored window in the query's transaction.
	rejected := errors.New("invalid operation")
	if err := manage(t, a, manager.Text.Reveal(), func(accounts.KeyManager, pg.Tx) error { return rejected }); !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	var rate *accounts.ManagementRateLimit
	if err := manage(t, b, manager.Text.Reveal(), func(accounts.KeyManager, pg.Tx) error { return nil }); !errors.As(err, &rate) {
		t.Fatalf("second node refunded failed request: %v", err)
	}
	inPlatform(t, db, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE accounts.api_keys SET management_window=statement_timestamp()-interval '2 minutes' WHERE id=$1`, uuid.UUID(manager.ID))
		return err
	})
	if err := manage(t, b, manager.Text.Reveal(), func(accounts.KeyManager, pg.Tx) error { return nil }); err != nil {
		t.Fatal("window did not reset", err)
	}
}

func TestManagementRevalidatesOnEveryNode(t *testing.T) {
	t.Parallel()
	db, _, a, _ := callSetup(t)
	b := node(t, db, hashKeys(t, 1), nil)
	manager := managementKey(t, db, a, accounts.KeySettings{Name: "manager"})
	for _, s := range []*accounts.Service{a, b} {
		if err := manage(t, s, manager.Text.Reveal(), func(accounts.KeyManager, pg.Tx) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	inPlatform(t, db, func(tx pg.Tx) error {
		_, err := a.UpdateKey(t.Context(), tx, system, manager.ID, accounts.KeySettings{Name: "manager", Disabled: true})
		return err
	})
	for _, s := range []*accounts.Service{a, b} {
		if err := manage(t, s, manager.Text.Reveal(), func(accounts.KeyManager, pg.Tx) error { return nil }); !errors.Is(err, accounts.ErrUnknownKey) {
			t.Fatalf("disabled cached manager: %v", err)
		}
	}
}

// TestManagementKeyReads: the read-only endpoints OpenRouter opens to management keys admit one, and Admission.KeyKind
// says which kind the key is; a call key is admitted as before, and the management key is still no call key.
func TestManagementKeyReads(t *testing.T) {
	t.Parallel()
	db, _, s, callKey := callSetup(t)
	manager := managementKey(t, db, s, accounts.KeySettings{Name: "manager"})
	for _, tc := range []struct {
		name, key, want string
	}{
		{"a management key", manager.Text.Reveal(), accounts.ManagementKey},
		{"a call key", callKey, accounts.CallKey},
	} {
		c, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), tc.key)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var kind string
		err = s.AdmitRead(t.Context(), c, accounts.AdmitRequest{Domain: apiDomain, ClientIP: client},
			func(_ pg.Tx, a accounts.Admission) error { kind = a.KeyKind(); return nil })
		if err != nil || kind != tc.want {
			t.Errorf("%s: kind %q, error %v, want %q", tc.name, kind, err, tc.want)
		}
	}
	if err := call(t, s, s1, manager.Text.Reveal(), apiDomain); !errors.Is(err, accounts.ErrUnknownKey) {
		t.Errorf("a management key made a call: %v", err)
	}
}

func TestManagementCeilingIsIndependent(t *testing.T) {
	t.Parallel()
	db, _, s, _ := callSetup(t)
	manager := managementKey(t, db, s, accounts.KeySettings{Name: "manager", SpendLimit: new(int64(100)), Models: []string{"a"}})
	err := manage(t, s, manager.Text.Reveal(), func(m accounts.KeyManager, tx pg.Tx) error {
		copy := m.Ceiling()
		*copy.SpendLimit = 101
		copy.Models[0] = "other"
		*copy.ExpiresAt = copy.ExpiresAt.Add(time.Hour)
		_, err := m.Create(t.Context(), nil, copy.ExpiresAt, copy.KeySettings)
		wantManagementError(t, err, accounts.ErrForbidden)
		return nil
	})
	wantManagementError(t, err, nil)
}

func TestManagementMutableAuthorization(t *testing.T) {
	t.Parallel()
	db, database, a, _ := callSetup(t)
	b := node(t, db, hashKeys(t, 1), nil)
	manager := managementKey(t, db, a, accounts.KeySettings{Name: "manager"})
	for _, tc := range []struct{ name, change, restore string }{
		{"expired", `UPDATE accounts.api_keys SET expires_at=statement_timestamp()-interval '1 second' WHERE kind='management'`, `UPDATE accounts.api_keys SET expires_at=statement_timestamp()+interval '1 hour' WHERE kind='management'`},
		{"IP", `UPDATE accounts.api_keys SET ip_allow=ARRAY['192.0.2.0/24'::cidr] WHERE kind='management'`, `UPDATE accounts.api_keys SET ip_allow=NULL WHERE kind='management'`},
		{"deleted", `UPDATE accounts.api_keys SET deleted_at=now() WHERE kind='management'`, `UPDATE accounts.api_keys SET deleted_at=NULL WHERE kind='management'`},
		{"account", `UPDATE accounts.accounts SET status='disabled' WHERE id=:a1`, `UPDATE accounts.accounts SET status='active' WHERE id=:a1`},
		{"workspace", `UPDATE accounts.workspaces SET status='disabled' WHERE id=:w1`, `UPDATE accounts.workspaces SET status='active' WHERE id=:w1`},
		{"domain", `UPDATE accounts.site_domains SET disabled_at=now() WHERE domain='` + apiDomain + `'`, `UPDATE accounts.site_domains SET disabled_at=NULL WHERE domain='` + apiDomain + `'`},
		{"site", `UPDATE accounts.sites SET status='disabled' WHERE id=:s1`, `UPDATE accounts.sites SET status='active' WHERE id=:s1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, s := range []*accounts.Service{a, b} {
				wantManagementError(t, manage(t, s, manager.Text.Reveal(), func(accounts.KeyManager, pg.Tx) error { return nil }), nil)
			}
			if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(tc.change)); err != nil {
				t.Fatal(err)
			}
			for _, s := range []*accounts.Service{a, b} {
				wantManagementError(t, manage(t, s, manager.Text.Reveal(), func(accounts.KeyManager, pg.Tx) error { return nil }), accounts.ErrUnknownKey)
			}
			if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(tc.restore)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagementHoldsDatabaseAdmissionSlot(t *testing.T) {
	t.Parallel()
	db, _, original, _ := callSetup(t)
	manager := managementKey(t, db, original, accounts.KeySettings{Name: "manager"})
	var gate slots
	s := gatedNode(t, db, &gate)
	caller, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), manager.Text.Reveal())
	wantManagementError(t, err, nil)
	err = s.WithKeyManager(t.Context(), caller, accounts.AdmitRequest{Domain: apiDomain, ClientIP: client}, func(accounts.KeyManager, pg.Tx) error {
		if _, held := gate.counts(); held != 1 {
			t.Fatal("management ran outside admission budget")
		}
		return nil
	})
	wantManagementError(t, err, nil)
	if _, held := gate.counts(); held != 0 {
		t.Fatal("management leaked admission slot")
	}
	gate.mu.Lock()
	gate.refuse = errors.New("no connection slot")
	gate.mu.Unlock()
	err = s.WithKeyManager(t.Context(), caller, accounts.AdmitRequest{Domain: apiDomain, ClientIP: client}, func(accounts.KeyManager, pg.Tx) error {
		t.Fatal("management ran without admission slot")
		return nil
	})
	wantManagementError(t, err, accounts.ErrManagementBusy)
}

func TestManagementCanReduceExistingAuthority(t *testing.T) {
	t.Parallel()
	db, _, s, callKey := callSetup(t)
	manager := managementKey(t, db, s, accounts.KeySettings{Name: "manager", SpendLimit: new(int64(1))})
	caller, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), callKey)
	wantManagementError(t, err, nil)
	err = manage(t, s, manager.Text.Reveal(), func(m accounts.KeyManager, tx pg.Tx) error {
		key, err := m.Get(t.Context(), caller.KeyID())
		wantManagementError(t, err, nil)
		key.Disabled = true
		_, err = m.Update(t.Context(), key.ID, key.KeySettings)
		wantManagementError(t, err, nil)
		key.Disabled = false
		_, err = m.Update(t.Context(), key.ID, key.KeySettings)
		wantManagementError(t, err, accounts.ErrForbidden)
		key.Disabled = true
		key.SpendLimit = new(int64(1))
		_, err = m.Update(t.Context(), key.ID, key.KeySettings)
		wantManagementError(t, err, nil)
		key.SpendLimit = nil
		key.LimitReset = ""
		_, err = m.Update(t.Context(), key.ID, key.KeySettings)
		wantManagementError(t, err, accounts.ErrForbidden)
		return nil
	})
	wantManagementError(t, err, nil)
	wantManagementError(t, call(t, s, s1, callKey, apiDomain), accounts.ErrUnknownKey)
}
