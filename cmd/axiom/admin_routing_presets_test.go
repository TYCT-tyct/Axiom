package main

import (
	"net/http"
	"slices"
	"strconv"
	"testing"
	"uuid"

	"axiom/internal/platform/adminapi"
)

// adminPreset makes a routing preset at path and returns it.
func (f adminFixture) adminPreset(s session, path, body string) adminapi.RoutingPreset {
	f.t.Helper()
	rec := f.change(s, http.MethodPost, path, body, newKey())
	expectAdmin(f.t, "create "+body, rec, http.StatusCreated, "")
	var p adminapi.RoutingPreset
	decode(f.t, rec, &p)
	return p
}

// adminPresetSlugs are the slugs of the presets listed at path, a disabled one marked.
func (f adminFixture) adminPresetSlugs(s session, path string) []string {
	f.t.Helper()
	var list adminapi.RoutingPresetList
	decode(f.t, f.do(http.MethodGet, path, "", as(s)), &list)
	var out []string
	for _, p := range list.Data {
		if p.Disabled {
			p.Slug += " (disabled)"
		}
		out = append(out, p.Slug)
	}
	return out
}

// TestAdminRoutingPresets: the admin manages the platform's and a site's presets and defaults under an idempotency
// key and a reason; a platform preset that limits a price names its currency and cannot be the platform's default, a
// site's preset names none and has no advanced settings, the system preset and the accounts' presets are not the
// admin's to change, and a preset a default points at is not disabled (docs/domains/routing.md「接口、权限与审计」).
//
//nolint:funlen // one fixture walks both layers from list and create through defaults, refusal and disable
func TestAdminRoutingPresets(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	site := "/sites/" + f.siteID()

	key := newKey()
	body := `{"slug":"cheap","name":"Cheap","currency":"USD","rules":{"default":{"max_price":{"prompt":"1"}},"advanced":{"order":["g1"]}},"reason":"a cheap baseline"}`
	var priced adminapi.RoutingPreset
	for i := range 2 {
		rec := f.change(s, http.MethodPost, "/routing-presets", body, key)
		expectAdmin(t, "create a platform preset", rec, http.StatusCreated, "")
		if i == 1 && rec.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatal("the second create was not replayed")
		}
		decode(t, rec, &priced)
	}
	cheap := priced.Id.String()
	open := f.adminPreset(s, "/routing-presets", `{"slug":"open","name":"Open","rules":{"default":{}},"reason":"open baseline"}`)
	official := f.adminPreset(s, site+"/routing-presets", `{"slug":"official","name":"Official","rules":{"default":{"classes":["official"]}},"reason":"site baseline"}`)
	account := uuid.NewV7().String()
	f.run(`INSERT INTO routing.presets (id, scope, site_id, account_id, slug, name, current_version) VALUES
			('` + account + `', 'account', (SELECT id FROM accounts.sites WHERE is_main), '` + uuid.NewV7().String() + `', 'theirs', 'Theirs', 1);
		INSERT INTO routing.preset_versions (preset_id, version, rules) VALUES ('` + account + `', 1, '{"default": {}}')`)

	var platformDefault, siteDefault adminapi.RoutingDefault
	decode(t, f.do(http.MethodGet, "/settings/routing", "", as(s)), &platformDefault)
	if platformDefault.PresetId == nil || platformDefault.PresetId.String() != systemPreset {
		t.Fatalf("the migration's platform default %+v", platformDefault)
	}
	platformVersion := strconv.FormatInt(platformDefault.Version, 10)

	for _, c := range []struct {
		what, method, path, body string
		status                   int
		code                     errorCode
	}{
		{"a platform price limit with no currency", http.MethodPost, "/routing-presets",
			`{"slug":"priced","name":"Priced","rules":{"default":{"max_price":{"prompt":"1"}}},"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		{"a site's preset with a currency", http.MethodPost, site + "/routing-presets",
			`{"slug":"eur","name":"EUR","currency":"USD","rules":{"default":{}},"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		{"a site's preset with advanced settings", http.MethodPost, site + "/routing-presets",
			`{"slug":"adv","name":"Adv","rules":{"default":{},"advanced":{"max_attempts":2}},"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		{"the platform's slug again", http.MethodPost, "/routing-presets",
			`{"slug":"open","name":"Again","rules":{"default":{}},"reason":"r"}`, http.StatusConflict, codeNameTaken},
		{"no reason", http.MethodPost, "/routing-presets", `{"slug":"why","name":"Why","rules":{"default":{}}}`, http.StatusBadRequest, codeInvalidRequest},
		{"an unknown site", http.MethodPost, "/sites/site_00000000000000000000000000/routing-presets",
			`{"slug":"x","name":"X","rules":{"default":{}},"reason":"r"}`, http.StatusNotFound, codeNotFound},
		{"the system preset", http.MethodPut, "/routing-presets/" + systemPreset,
			`{"name":"Mine","revision":1,"rules":{"default":{}},"reason":"r"}`, http.StatusForbidden, codeForbidden},
		{"an account's preset", http.MethodPut, "/routing-presets/" + account,
			`{"name":"Mine","revision":1,"rules":{"default":{}},"reason":"r"}`, http.StatusNotFound, codeNotFound},
		{"a stale revision", http.MethodPut, "/routing-presets/" + official.Id.String(),
			`{"name":"Official","revision":9,"rules":{"default":{}},"reason":"r"}`, http.StatusConflict, codeStaleRevision},
		{"a priced preset as the platform's default", http.MethodPut, "/settings/routing",
			`{"preset_id":"` + cheap + `","version":` + platformVersion + `,"reason":"r"}`, http.StatusBadRequest, codePresetNotSelectable},
		{"a site's preset as the platform's default", http.MethodPut, "/settings/routing",
			`{"preset_id":"` + official.Id.String() + `","version":` + platformVersion + `,"reason":"r"}`, http.StatusBadRequest, codePresetNotSelectable},
		{"an account's preset as the site's default", http.MethodPut, site + "/routing-default",
			`{"preset_id":"` + account + `","version":0,"reason":"r"}`, http.StatusBadRequest, codePresetNotSelectable},
	} {
		expectAdmin(t, c.what, f.change(s, c.method, c.path, c.body, newKey()), c.status, c.code)
	}
	expectAdmin(t, "read an account's preset", f.do(http.MethodGet, "/routing-presets/"+account, "", as(s)), http.StatusNotFound, codeNotFound)

	rec := f.change(s, http.MethodPut, "/routing-presets/"+official.Id.String(),
		`{"name":"Official, cheapest first","revision":1,"rules":{"default":{"classes":["official"],"sort":"price"}},"reason":"sort by price"}`, newKey())
	expectAdmin(t, "update the site's preset", rec, http.StatusOK, "")
	decode(t, rec, &official)
	if official.Revision != 2 || official.Version != 2 || official.Scope != adminapi.RoutingPresetScopeSite {
		t.Errorf("updated %+v", official)
	}

	rec = f.change(s, http.MethodPut, "/settings/routing", `{"preset_id":"`+open.Id.String()+`","version":`+platformVersion+`,"reason":"open"}`, newKey())
	expectAdmin(t, "set the platform's default", rec, http.StatusOK, "")
	rec = f.change(s, http.MethodPut, site+"/routing-default", `{"preset_id":"`+cheap+`","version":0,"reason":"cheap"}`, newKey())
	expectAdmin(t, "set the site's default to a platform preset", rec, http.StatusOK, "")
	decode(t, f.do(http.MethodGet, site+"/routing-default", "", as(s)), &siteDefault)
	if siteDefault.PresetId == nil || siteDefault.PresetId.String() != cheap || siteDefault.Version < 1 {
		t.Fatalf("the site's default %+v", siteDefault)
	}

	expectAdmin(t, "disable the site's default", f.change(s, http.MethodDelete, "/routing-presets/"+cheap, `{"revision":1,"reason":"r"}`, newKey()),
		http.StatusConflict, codePresetInUse)
	expectAdmin(t, "disable the site's preset", f.change(s, http.MethodDelete, "/routing-presets/"+official.Id.String(), `{"revision":2,"reason":"replaced"}`, newKey()),
		http.StatusNoContent, "")
	if got, want := f.adminPresetSlugs(s, "/routing-presets"), []string{"allow-all", "cheap", "open"}; !slices.Equal(got, want) {
		t.Errorf("the platform lists %v, want %v", got, want)
	}
	if got, want := f.adminPresetSlugs(s, site+"/routing-presets"), []string{"official (disabled)"}; !slices.Equal(got, want) {
		t.Errorf("the site lists %v, want %v", got, want)
	}
	if got, want := f.text(`SELECT string_agg(action || ':' || reason, ',' ORDER BY id) FROM platform.audit_log WHERE module = 'routing'`),
		"routing.preset.create:a cheap baseline,routing.preset.create:open baseline,routing.preset.create:site baseline,"+
			"routing.preset.update:sort by price,routing.default.update:open,routing.default.update:cheap,routing.preset.disable:replaced"; got != want {
		t.Errorf("audited %s, want %s", got, want)
	}
}
