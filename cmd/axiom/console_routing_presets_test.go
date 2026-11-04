package main

import (
	"net/http"
	"testing"
	"uuid"

	"axiom/internal/platform/consoleapi"
	"axiom/internal/routing"
)

// TestConsoleRoutingPresets: an account makes a routing preset, changes it under its revision, makes it the default
// its new keys take, binds and rebinds a key to presets it may use, and disables a preset only once no default points
// at it; the system preset, another account's preset and any for a management key are refused, and a key changed
// without naming a preset keeps its own (docs/domains/routing.md「接口、权限与审计」).
func TestConsoleRoutingPresets(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	account, site := f.account(s)
	other := uuid.NewV7()
	f.run(`INSERT INTO routing.presets (id, scope, site_id, account_id, slug, name, current_version) VALUES
			('` + other.String() + `', 'account', '` + site.String() + `', '` + uuid.NewV7().String() + `', 'theirs', 'Theirs', 1);
		INSERT INTO routing.preset_versions (preset_id, version, rules) VALUES ('` + other.String() + `', 1, '{"default": {}}')`)

	rec := f.do(http.MethodPost, "/routing-presets", `{"slug":"official","name":"Official","rules":{"default":{"classes":["official"]}}}`, as(s))
	expect(t, "create", rec, http.StatusCreated, "")
	var p consoleapi.RoutingPreset
	decode(t, rec, &p)
	if p.Scope != consoleapi.RoutingPresetScopeAccount || p.Revision != 1 || p.Version != 1 {
		t.Fatalf("created %+v", p)
	}
	path := "/routing-presets/" + p.Id.String()
	expect(t, "rules that widen", f.do(http.MethodPost, "/routing-presets",
		`{"slug":"wide","name":"Wide","rules":{"default":{"classes":["official"]},"rules":[{"author":"openai","classes":["relay"]}]}}`, as(s)),
		http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	expect(t, "the slug again", f.do(http.MethodPost, "/routing-presets", `{"slug":"official","name":"Again","rules":{"default":{}}}`, as(s)),
		http.StatusConflict, consoleapi.ErrorCodeNameTaken)
	expect(t, "a stale revision", f.do(http.MethodPut, path, `{"name":"Official","revision":7,"rules":{"default":{}}}`, as(s)),
		http.StatusConflict, consoleapi.ErrorCodeStaleRevision)
	expect(t, "another account's preset", f.do(http.MethodPut, "/routing-presets/"+other.String(), `{"name":"Mine","revision":1,"rules":{"default":{}}}`, as(s)),
		http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	expect(t, "the system preset", f.do(http.MethodPut, "/routing-presets/"+uuid.UUID(routing.SystemPreset).String(),
		`{"name":"Mine","revision":1,"rules":{"default":{}}}`, as(s)), http.StatusForbidden, consoleapi.ErrorCodeForbidden)
	rec = f.do(http.MethodPut, path, `{"name":"Official, cheapest first","revision":1,"rules":{"default":{"classes":["official"],"sort":"price"}}}`, as(s))
	expect(t, "update", rec, http.StatusOK, "")
	decode(t, rec, &p)
	if p.Revision != 2 || p.Version != 2 || p.Name != "Official, cheapest first" {
		t.Errorf("updated %+v", p)
	}
	var list consoleapi.RoutingPresetList
	decode(t, f.do(http.MethodGet, "/routing-presets", "", as(s)), &list)
	if len(list.Data) != 1 || list.Data[0].Id != p.Id {
		t.Errorf("listed %+v, want the account's one preset and neither the system's nor another account's", list.Data)
	}

	rec = f.do(http.MethodPut, "/routing-default", `{"preset_id":"`+p.Id.String()+`","version":0}`, as(s))
	expect(t, "set the default", rec, http.StatusOK, "")
	var d consoleapi.RoutingDefault
	decode(t, rec, &d)
	if d.PresetId == nil || *d.PresetId != p.Id || d.Version != 1 {
		t.Fatalf("default %+v", d)
	}
	expect(t, "the system preset as the default", f.do(http.MethodPut, "/routing-default",
		`{"preset_id":"`+uuid.UUID(routing.SystemPreset).String()+`","version":1}`, as(s)), http.StatusBadRequest, consoleapi.ErrorCodePresetNotSelectable)

	key := f.createKey(s, `{"name":"bound"}`).Key
	if key.PresetId == nil || *key.PresetId != p.Id || key.Preset == nil || key.Preset.Name != p.Name || key.Preset.Version != 2 {
		t.Errorf("a new key %+v %+v, want the account's default", key.PresetId, key.Preset)
	}
	for name, id := range map[string]string{"the system preset": uuid.UUID(routing.SystemPreset).String(), "another account's": other.String()} {
		expect(t, "a key bound to "+name, f.do(http.MethodPost, "/keys", `{"name":"refused","preset_id":"`+id+`"}`, as(s)),
			http.StatusBadRequest, consoleapi.ErrorCodePresetNotSelectable)
	}
	expect(t, "a management key with a preset", f.do(http.MethodPost, "/keys",
		`{"name":"manager","kind":"management","expires_at":"2099-01-01T00:00:00Z","preset_id":"`+p.Id.String()+`"}`, as(s)),
		http.StatusBadRequest, consoleapi.ErrorCodePresetNotSelectable)

	keyPath := "/keys/" + key.Id
	rec = f.do(http.MethodPut, keyPath, `{"name":"renamed","disabled":false}`, as(s))
	expect(t, "a change that names no preset", rec, http.StatusOK, "")
	decode(t, rec, &key)
	if key.PresetId == nil || *key.PresetId != p.Id {
		t.Errorf("a key changed without a preset is bound to %v, want it kept", key.PresetId)
	}
	second := f.do(http.MethodPost, "/routing-presets", `{"slug":"open","name":"Open","rules":{"default":{}}}`, as(s))
	var open consoleapi.RoutingPreset
	decode(t, second, &open)
	rec = f.do(http.MethodPut, keyPath, `{"name":"renamed","disabled":false,"preset_id":"`+open.Id.String()+`"}`, as(s))
	expect(t, "rebind", rec, http.StatusOK, "")
	decode(t, rec, &key)
	if key.PresetId == nil || *key.PresetId != open.Id {
		t.Errorf("a rebound key is bound to %v, want %v", key.PresetId, open.Id)
	}
	expect(t, "rebind to the system preset", f.do(http.MethodPut, keyPath,
		`{"name":"renamed","disabled":false,"preset_id":"`+uuid.UUID(routing.SystemPreset).String()+`"}`, as(s)),
		http.StatusBadRequest, consoleapi.ErrorCodePresetNotSelectable)

	expect(t, "disable the default", f.do(http.MethodDelete, path+"?revision=2", "", as(s)), http.StatusConflict, consoleapi.ErrorCodePresetInUse)
	expect(t, "clear the default", f.do(http.MethodPut, "/routing-default", `{"preset_id":null,"version":1}`, as(s)), http.StatusOK, "")
	expect(t, "disable", f.do(http.MethodDelete, path+"?revision=2", "", as(s)), http.StatusNoContent, "")
	decode(t, f.do(http.MethodGet, path, "", as(s)), &p)
	if !p.Disabled {
		t.Errorf("read after disabling: %+v", p)
	}
	audited := f.text(`SELECT string_agg(action, ',' ORDER BY id) FROM platform.audit_log WHERE module = 'routing' AND account_id = '` + account.String() + `'`)
	if want := "routing.preset.create,routing.preset.update,routing.default.update,routing.preset.create,routing.default.update,routing.preset.disable"; audited != want {
		t.Errorf("audited %s, want %s", audited, want)
	}
}
