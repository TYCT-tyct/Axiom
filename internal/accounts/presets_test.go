package accounts_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func TestAccountPresets(t *testing.T) {
	t.Parallel()
	db, _, s, _ := callSetup(t)
	p := accounts.Preset{Slug: "support", Name: "Support", Config: jsonx.Raw(`{"model":"m","provider":{"sort":"price"}}`)}
	save := func(account accounts.AccountID, create bool) error {
		site := s1
		if account == accounts.AccountID(a2) {
			site = s2
		}
		return db.Tx(t.Context(), platform, func(tx pg.Tx) (err error) {
			_, err = accounts.SavePreset(t.Context(), tx, system, accounts.SiteID(site), account, p, create)
			return err
		})
	}
	wantManagementError(t, save(accounts.AccountID(a1), true), nil)
	wantManagementError(t, save(accounts.AccountID(a1), true), accounts.ErrPresetConflict)
	// The same slug is private to each account, even when reads use a broader scope.
	wantManagementError(t, save(accounts.AccountID(a2), true), nil)
	wantManagementError(t, s.Reload(t.Context()), nil)
	if config, err := s.PresetConfig(accounts.SiteID(s1), accounts.AccountID(a1), p.Slug); err != nil || !strings.Contains(config, `"model"`) {
		t.Fatalf("snapshot: %s, %v", config, err)
	}
	if _, err := s.PresetConfig(accounts.SiteID(s2), accounts.AccountID(a1), p.Slug); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("cross-site: %v", err)
	}
	err := db.ReadTx(t.Context(), pg.AccountScope(a2), func(tx pg.Tx) error {
		_, err := accounts.GetPreset(t.Context(), tx, accounts.AccountID(a1), p.Slug)
		if !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("cross-account read: %v", err)
		}
		return nil
	})
	wantManagementError(t, err, nil)
	p.Config = jsonx.Raw(`{"model":"new"}`)
	wantManagementError(t, save(accounts.AccountID(a1), false), nil)
	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		list, total, err := accounts.ListPresets(t.Context(), tx, accounts.AccountID(a1), 1, 0)
		if err != nil {
			return err
		}
		if total != 1 || len(list) != 1 || !strings.Contains(string(list[0].Config), "new") {
			t.Fatalf("list: %v, %d", list, total)
		}
		return accounts.DeletePreset(t.Context(), tx, system, accounts.SiteID(s1), accounts.AccountID(a1), p.Slug)
	})
	wantManagementError(t, err, nil)
	wantManagementError(t, s.Reload(t.Context()), nil)
	if _, err := s.PresetConfig(accounts.SiteID(s1), accounts.AccountID(a1), p.Slug); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatal(err)
	}
	wantManagementError(t, save(accounts.AccountID(a1), false), accounts.ErrNotFound)
	wantManagementError(t, save(accounts.AccountID(a1), true), nil)
	logs := texts(t, db, platform, `SELECT action FROM platform.audit_log WHERE action LIKE 'preset.%' ORDER BY id`)
	if len(logs) != 5 {
		t.Fatalf("audit: %v", logs)
	}
}

func TestPresetLimitConcurrent(t *testing.T) {
	t.Parallel()
	db, _, _, _ := callSetup(t)
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO accounts.presets(id,site_id,account_id,slug,name,config) SELECT uuidv7(),$1,$2,'preset-'||n,'test','{}'::jsonb FROM generate_series(1,99) n`, s1, a1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			results <- db.Tx(t.Context(), pg.AccountScope(a1), func(tx pg.Tx) error {
				_, err := accounts.SavePreset(t.Context(), tx, system, accounts.SiteID(s1), accounts.AccountID(a1), accounts.Preset{Slug: fmt.Sprintf("last-%d", i), Name: "last", Config: jsonx.Raw(`{}`)}, true)
				return err
			})
		})
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, accounts.ErrPresetConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent limit: %d successes, %d conflicts", successes, conflicts)
	}
}

func TestPresetConfigBoundary(t *testing.T) {
	for _, config := range []string{`null`, `[]`, `{"messages":[]}`, `{"stream":true}`, `{"session_id":"s"}`, `{"model":"@preset/recursive"}`, `{"models":["@preset/recursive"]}`, `{"system":42}`, `{"temperature":1,"temperature":2}`, `{"system":"` + strings.Repeat("x", 17000) + `"}`} {
		if !errors.Is(accounts.ValidatePresetConfig([]byte(config)), accounts.ErrPreset) {
			t.Errorf("accepted %s", config[:min(100, len(config))])
		}
	}
}
