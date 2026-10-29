package accounts_test

import (
	"fmt"
	"strings"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func expandedPresetNumber() jsonx.Raw {
	fields := make([]string, 60)
	for i := range fields {
		fields[i] = fmt.Sprintf(`"%d":9e307`, i)
	}
	return jsonx.Raw(`{"logit_bias":{` + strings.Join(fields, ",") + `}}`)
}

func TestPresetStoredBoundary(t *testing.T) {
	t.Parallel()
	db, _, s, _ := callSetup(t)
	save := func(config jsonx.Raw, create bool) error {
		return db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, err := accounts.SavePreset(t.Context(), tx, system, accounts.SiteID(s1), accounts.AccountID(a1), accounts.Preset{Slug: "bounded", Name: "Bounded", Config: config}, create)
			return err
		})
	}
	for _, raw := range []jsonx.Raw{expandedPresetNumber(), jsonx.Raw(`{"system":"a\u0000b"}`), jsonx.Raw(`{"temperature":1e1000000}`)} {
		wantManagementError(t, save(raw, true), accounts.ErrPreset)
	}
	wantManagementError(t, save(jsonx.Raw(`{"model":"m","temperature":1e1}`), true), nil)
	wantManagementError(t, save(expandedPresetNumber(), false), accounts.ErrPreset)
	wantManagementError(t, s.Reload(t.Context()), nil)
	config, err := s.PresetConfig(accounts.SiteID(s1), accounts.AccountID(a1), "bounded")
	wantManagementError(t, err, nil)
	var got struct {
		Temperature int `json:"temperature"`
	}
	wantManagementError(t, jsonx.Unmarshal([]byte(config), &got), nil)
	if got.Temperature != 10 {
		t.Fatalf("failed update changed config: %s", config)
	}
	if logs := texts(t, db, platform, `SELECT action FROM platform.audit_log WHERE action LIKE 'preset.%'`); len(logs) != 1 {
		t.Fatalf("rejected writes left audit: %v", logs)
	}
}

func TestPresetReloadSkipsInvalidRows(t *testing.T) {
	t.Parallel()
	db, _, s, _ := callSetup(t)
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := accounts.SavePreset(t.Context(), tx, system, accounts.SiteID(s1), accounts.AccountID(a1), accounts.Preset{Slug: "valid", Name: "Valid", Config: jsonx.Raw(`{"model":"m"}`)}, true)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO accounts.presets(id,site_id,account_id,slug,name,config) VALUES(uuidv7(),$1,$2,'future','Future','{"future_option":true}'),(uuidv7(),$1,$2,'malformed','Malformed','{"models":[17]}')`, s2, a2)
		return err
	})
	wantManagementError(t, err, nil)
	wantManagementError(t, s.Reload(t.Context()), nil)
	_, _, err = s.SiteForHost(apiDomain)
	wantManagementError(t, err, nil)
	_, err = s.PresetConfig(accounts.SiteID(s1), accounts.AccountID(a1), "valid")
	wantManagementError(t, err, nil)
	for _, slug := range []string{"future", "malformed"} {
		_, err = s.PresetConfig(accounts.SiteID(s2), accounts.AccountID(a2), slug)
		wantManagementError(t, err, accounts.ErrNotFound)
	}
}

func TestPresetDatabaseSizeBoundary(t *testing.T) {
	t.Parallel()
	db, _, _, _ := callSetup(t)
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO accounts.presets(id,site_id,account_id,slug,name,config) VALUES(uuidv7(),$1,$2,'large','Large',$3)`, s1, a1, []byte(expandedPresetNumber()))
		return err
	})
	if pg.SQLState(err) != "23514" {
		t.Fatalf("database accepted oversized stored JSON: %v", err)
	}
}
