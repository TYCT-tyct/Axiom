package main

import (
	"net/http"
	"strconv"
	"testing"
	"uuid"

	"axiom/internal/platform/consoleapi"
	"axiom/internal/routing"
)

// systemPreset is the system preset's ID as the console writes it.
var systemPreset = uuid.UUID(routing.SystemPreset).String()

// presetOfAnother writes a preset of another account of the site and returns its ID.
func (f consoleFixture) presetOfAnother(site uuid.UUID) string {
	f.t.Helper()
	id := uuid.NewV7().String()
	f.run(`INSERT INTO routing.presets (id, scope, site_id, account_id, slug, name, current_version) VALUES
			('` + id + `', 'account', '` + site.String() + `', '` + uuid.NewV7().String() + `', 'theirs', 'Theirs', 1);
		INSERT INTO routing.preset_versions (preset_id, version, rules) VALUES ('` + id + `', 1, '{"default": {}}')`)
	return id
}

// routingPreset makes a routing preset of the session's account.
func (f consoleFixture) routingPreset(s session, body string) consoleapi.RoutingPreset {
	f.t.Helper()
	rec := f.do(http.MethodPost, "/routing-presets", body, as(s))
	expect(f.t, "create "+body, rec, http.StatusCreated, "")
	var p consoleapi.RoutingPreset
	decode(f.t, rec, &p)
	return p
}

// wantPresetAt checks a preset's revision and version.
func wantPresetAt(t *testing.T, what string, p consoleapi.RoutingPreset, revision, version int64) {
	t.Helper()
	if p.Revision != revision || p.Version != version {
		t.Errorf("%s: revision %d version %d, want %d and %d", what, p.Revision, p.Version, revision, version)
	}
}

// TestConsoleRoutingPresets: an account makes a routing preset, changes it under its revision, makes it its default,
// and disables it only once no default points at it; rules that widen, a slug it has, a stale revision, another
// account's preset and the system preset are refused, and every change is audited (docs/domains/routing.md「接口、权限
// 与审计」).
func TestConsoleRoutingPresets(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	account, site := f.account(s)
	other := f.presetOfAnother(site)
	p := f.routingPreset(s, `{"slug":"official","name":"Official","rules":{"default":{"classes":["official"]}}}`)
	wantPresetAt(t, "created", p, 1, 1)
	if p.Scope != consoleapi.RoutingPresetScopeAccount {
		t.Errorf("created in %s", p.Scope)
	}
	path := "/routing-presets/" + p.Id.String()
	for _, c := range []struct {
		what, method, path, body string
		status                   int
		code                     consoleapi.ErrorCode
	}{
		{
			"rules that widen", http.MethodPost, "/routing-presets",
			`{"slug":"wide","name":"Wide","rules":{"default":{"classes":["official"]},"rules":[{"author":"openai","classes":["relay"]}]}}`,
			http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest,
		},
		{
			"the slug again", http.MethodPost, "/routing-presets", `{"slug":"official","name":"Again","rules":{"default":{}}}`,
			http.StatusConflict, consoleapi.ErrorCodeNameTaken,
		},
		{
			"a stale revision", http.MethodPut, path, `{"name":"Official","revision":7,"rules":{"default":{}}}`,
			http.StatusConflict, consoleapi.ErrorCodeStaleRevision,
		},
		{
			"another account's preset", http.MethodPut, "/routing-presets/" + other, `{"name":"Mine","revision":1,"rules":{"default":{}}}`,
			http.StatusNotFound, consoleapi.ErrorCodeNotFound,
		},
		{
			"the system preset", http.MethodPut, "/routing-presets/" + systemPreset, `{"name":"Mine","revision":1,"rules":{"default":{}}}`,
			http.StatusForbidden, consoleapi.ErrorCodeForbidden,
		},
		{
			"the system preset as the default", http.MethodPut, "/routing-default", `{"preset_id":"` + systemPreset + `","version":0}`,
			http.StatusBadRequest, consoleapi.ErrorCodePresetNotSelectable,
		},
	} {
		expect(t, c.what, f.do(c.method, c.path, c.body, as(s)), c.status, c.code)
	}

	rec := f.do(http.MethodPut, path, `{"name":"Official, cheapest first","revision":1,"rules":{"default":{"classes":["official"],"sort":"price"}}}`, as(s))
	expect(t, "update", rec, http.StatusOK, "")
	decode(t, rec, &p)
	wantPresetAt(t, "updated", p, 2, 2)
	var list consoleapi.RoutingPresetList
	decode(t, f.do(http.MethodGet, "/routing-presets", "", as(s)), &list)
	if len(list.Data) != 1 || list.Data[0].Id != p.Id {
		t.Errorf("listed %+v, want the account's one preset and neither the system's nor another account's", list.Data)
	}
	rec = f.do(http.MethodPut, "/routing-default", `{"preset_id":"`+p.Id.String()+`","version":0}`, as(s))
	expect(t, "set the default", rec, http.StatusOK, "")
	var d consoleapi.RoutingDefault
	decode(t, rec, &d)
	if d.PresetId == nil || *d.PresetId != p.Id || d.Version < 1 {
		t.Fatalf("default %+v", d)
	}
	expect(t, "disable the default", f.do(http.MethodDelete, path+"?revision=2", "", as(s)), http.StatusConflict, consoleapi.ErrorCodePresetInUse)
	expect(t, "clear the default", f.do(http.MethodPut, "/routing-default", `{"preset_id":null,"version":`+strconv.FormatInt(d.Version, 10)+`}`, as(s)), http.StatusOK, "")
	expect(t, "disable", f.do(http.MethodDelete, path+"?revision=2", "", as(s)), http.StatusNoContent, "")
	decode(t, f.do(http.MethodGet, path, "", as(s)), &p)
	if !p.Disabled {
		t.Errorf("read after disabling: %+v", p)
	}
	audited := f.text(`SELECT string_agg(action, ',' ORDER BY id) FROM platform.audit_log WHERE module = 'routing' AND account_id = '` + account.String() + `'`)
	if want := "routing.preset.create,routing.preset.update,routing.default.update,routing.default.update,routing.preset.disable"; audited != want {
		t.Errorf("audited %s, want %s", audited, want)
	}
}

// TestConsoleKeyPresets: a new key takes the account's default with the preset's summary; a key may be bound and
// rebound to presets it may use, and keeps its preset when changed without naming one; the system preset, another
// account's preset and any for a management key are refused.
func TestConsoleKeyPresets(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	_, site := f.account(s)
	other := f.presetOfAnother(site)
	strict := f.routingPreset(s, `{"slug":"strict","name":"Strict","rules":{"default":{"classes":["official"]}}}`)
	open := f.routingPreset(s, `{"slug":"open","name":"Open","rules":{"default":{}}}`)
	expect(t, "set the default", f.do(http.MethodPut, "/routing-default", `{"preset_id":"`+strict.Id.String()+`","version":0}`, as(s)), http.StatusOK, "")

	key := f.createKey(s, `{"name":"bound"}`).Key
	if key.PresetId == nil || *key.PresetId != strict.Id || key.Preset == nil || key.Preset.Name != "Strict" || key.Preset.Version != 1 {
		t.Errorf("a new key %+v %+v, want the account's default", key.PresetId, key.Preset)
	}
	keyPath := "/keys/" + key.Id
	for what, c := range map[string]struct{ method, path, body string }{
		"a new key bound to the system preset":   {http.MethodPost, "/keys", `{"name":"x","preset_id":"` + systemPreset + `"}`},
		"a new key bound to another account's":   {http.MethodPost, "/keys", `{"name":"x","preset_id":"` + other + `"}`},
		"a management key with a preset":         {http.MethodPost, "/keys", `{"name":"x","kind":"management","expires_at":"2099-01-01T00:00:00Z","preset_id":"` + strict.Id.String() + `"}`},
		"a key rebound to the system preset":     {http.MethodPut, keyPath, `{"name":"bound","disabled":false,"preset_id":"` + systemPreset + `"}`},
		"a key rebound to another account's one": {http.MethodPut, keyPath, `{"name":"bound","disabled":false,"preset_id":"` + other + `"}`},
	} {
		expect(t, what, f.do(c.method, c.path, c.body, as(s)), http.StatusBadRequest, consoleapi.ErrorCodePresetNotSelectable)
	}
	for _, step := range []struct {
		what, body string
		want       consoleapi.RoutingPreset
	}{
		{"a change that names no preset", `{"name":"renamed","disabled":false}`, strict},
		{"a rebind", `{"name":"renamed","disabled":false,"preset_id":"` + open.Id.String() + `"}`, open},
	} {
		rec := f.do(http.MethodPut, keyPath, step.body, as(s))
		expect(t, step.what, rec, http.StatusOK, "")
		decode(t, rec, &key)
		if key.PresetId == nil || *key.PresetId != step.want.Id {
			t.Errorf("%s: bound to %v, want %v", step.what, key.PresetId, step.want.Id)
		}
	}
}

// TestManagementKeyPresets: a management key chooses the routing preset of a key it makes or changes among those the
// key may use, as the console does; one it may not use, and null, are refused, and a change that names none keeps it.
func TestManagementKeyPresets(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	_, site := f.account(s)
	other := f.presetOfAnother(site)
	strict := f.routingPreset(s, `{"slug":"strict","name":"Strict","rules":{"default":{"classes":["official"]}}}`)
	open := f.routingPreset(s, `{"slug":"open","name":"Open","rules":{"default":{}}}`)
	manager := f.managementKey(s, `"name":"manager"`).Secret

	made, _ := readManaged(t, f.managementRequest(http.MethodPost, "", `{"name":"chosen","preset_id":"`+strict.Id.String()+`"}`, manager), http.StatusCreated)
	if made.Preset == nil || *made.Preset != strict.Id.String() {
		t.Fatalf("a key made with a preset is bound to %v, want %s", made.Preset, strict.Id)
	}
	for what, c := range map[string]struct{ method, path, body string }{
		"a new key bound to the system preset":   {http.MethodPost, "", `{"name":"x","preset_id":"` + systemPreset + `"}`},
		"a new key bound to another account's":   {http.MethodPost, "", `{"name":"x","preset_id":"` + other + `"}`},
		"a new key bound to no preset":           {http.MethodPost, "", `{"name":"x","preset_id":null}`},
		"a key rebound to another account's one": {http.MethodPatch, "/" + made.Hash, `{"preset_id":"` + other + `"}`},
		"a key rebound to an ID that is not one": {http.MethodPatch, "/" + made.Hash, `{"preset_id":"strict"}`},
	} {
		if rec := f.managementRequest(c.method, c.path, c.body, manager); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", what, rec.Code, rec.Body)
		}
	}
	for _, step := range []struct{ what, body, want string }{
		{"a change that names no preset", `{"name":"renamed"}`, strict.Id.String()},
		{"a rebind", `{"preset_id":"` + open.Id.String() + `"}`, open.Id.String()},
	} {
		got, _ := readManaged(t, f.managementRequest(http.MethodPatch, "/"+made.Hash, step.body, manager), http.StatusOK)
		if got.Preset == nil || *got.Preset != step.want {
			t.Errorf("%s: bound to %v, want %s", step.what, got.Preset, step.want)
		}
	}
}
