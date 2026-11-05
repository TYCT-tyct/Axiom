package main

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

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

// adminLayers is the main site's path, the platform presets "cheap" (priced in USD) and "open", the site's preset
// "official", an account's preset of the site, and the version of the platform's default the migration wrote.
type adminLayers struct {
	f                     adminFixture
	s                     session
	site                  string
	cheap, open, official adminapi.RoutingPreset
	account               string
	platformVersion       string
}

func newAdminLayers(t *testing.T) adminLayers {
	t.Helper()
	f := newAdminFixture(t)
	l := adminLayers{f: f, s: f.owner(), site: "/sites/" + f.siteID(), account: uuid.NewV7().String()}
	key := newKey()
	body := `{"slug":"cheap","name":"Cheap","currency":"USD","rules":{"default":{"max_price":{"prompt":"1"}},"advanced":{"order":["g1"]}},"reason":"a cheap baseline"}`
	expectAdmin(t, "create a platform preset", f.change(l.s, http.MethodPost, "/routing-presets", body, key), http.StatusCreated, "")
	rec := f.change(l.s, http.MethodPost, "/routing-presets", body, key)
	expectAdmin(t, "create it again", rec, http.StatusCreated, "")
	if rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("the second create was not replayed")
	}
	decode(t, rec, &l.cheap)
	l.open = f.adminPreset(l.s, "/routing-presets", `{"slug":"open","name":"Open","rules":{"default":{}},"reason":"open baseline"}`)
	l.official = f.adminPreset(l.s, l.site+"/routing-presets", `{"slug":"official","name":"Official","rules":{"default":{"classes":["official"]}},"reason":"site baseline"}`)
	f.run(`INSERT INTO routing.presets (id, scope, site_id, account_id, slug, name, current_version) VALUES
			('` + l.account + `', 'account', (SELECT id FROM accounts.sites WHERE is_main), '` + uuid.NewV7().String() + `', 'theirs', 'Theirs', 1);
		INSERT INTO routing.preset_versions (preset_id, version, rules) VALUES ('` + l.account + `', 1, '{"default": {}}')`)
	var d adminapi.RoutingDefault
	decode(t, f.do(http.MethodGet, "/settings/routing", "", as(l.s)), &d)
	if d.PresetId == nil || d.PresetId.String() != systemPreset {
		t.Fatalf("the migration's platform default %+v", d)
	}
	l.platformVersion = strconv.FormatInt(d.Version, 10)
	return l
}

// TestAdminRoutingPresetRefusals: a platform preset that limits a price names its currency and cannot be the
// platform's default, a site's preset names none and has no advanced settings, a change gives a reason, and the
// system preset and the accounts' presets are not the admin's to change or choose (docs/domains/routing.md「接口、
// 权限与审计」).
func TestAdminRoutingPresetRefusals(t *testing.T) {
	t.Parallel()
	l := newAdminLayers(t)
	for _, c := range []struct {
		what, method, path, body string
		status                   int
		code                     errorCode
	}{
		{
			"a platform price limit with no currency", http.MethodPost, "/routing-presets",
			`{"slug":"priced","name":"Priced","rules":{"default":{"max_price":{"prompt":"1"}}},"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest,
		},
		{
			"a site's preset with a currency", http.MethodPost, l.site + "/routing-presets",
			`{"slug":"eur","name":"EUR","currency":"USD","rules":{"default":{}},"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest,
		},
		{
			"a site's preset with advanced settings", http.MethodPost, l.site + "/routing-presets",
			`{"slug":"adv","name":"Adv","rules":{"default":{},"advanced":{"max_attempts":2}},"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest,
		},
		{
			"the platform's slug again", http.MethodPost, "/routing-presets",
			`{"slug":"open","name":"Again","rules":{"default":{}},"reason":"r"}`, http.StatusConflict, codeNameTaken,
		},
		{"no reason", http.MethodPost, "/routing-presets", `{"slug":"why","name":"Why","rules":{"default":{}}}`, http.StatusBadRequest, codeInvalidRequest},
		{
			"an unknown site", http.MethodPost, "/sites/site_00000000000000000000000000/routing-presets",
			`{"slug":"x","name":"X","rules":{"default":{}},"reason":"r"}`, http.StatusNotFound, codeNotFound,
		},
		{
			"the system preset", http.MethodPut, "/routing-presets/" + systemPreset,
			`{"name":"Mine","revision":1,"rules":{"default":{}},"reason":"r"}`, http.StatusForbidden, codeForbidden,
		},
		{
			"an account's preset", http.MethodPut, "/routing-presets/" + l.account,
			`{"name":"Mine","revision":1,"rules":{"default":{}},"reason":"r"}`, http.StatusNotFound, codeNotFound,
		},
		{"disabling an account's preset", http.MethodDelete, "/routing-presets/" + l.account, `{"revision":1,"reason":"r"}`, http.StatusNotFound, codeNotFound},
		{
			"a stale revision", http.MethodPut, "/routing-presets/" + l.official.Id.String(),
			`{"name":"Official","revision":9,"rules":{"default":{}},"reason":"r"}`, http.StatusConflict, codeStaleRevision,
		},
		{
			"a priced preset as the platform's default", http.MethodPut, "/settings/routing",
			`{"preset_id":"` + l.cheap.Id.String() + `","version":` + l.platformVersion + `,"reason":"r"}`, http.StatusBadRequest, codePresetNotSelectable,
		},
		{
			"a site's preset as the platform's default", http.MethodPut, "/settings/routing",
			`{"preset_id":"` + l.official.Id.String() + `","version":` + l.platformVersion + `,"reason":"r"}`, http.StatusBadRequest, codePresetNotSelectable,
		},
		{
			"an account's preset as the site's default", http.MethodPut, l.site + "/routing-default",
			`{"preset_id":"` + l.account + `","version":0,"reason":"r"}`, http.StatusBadRequest, codePresetNotSelectable,
		},
	} {
		expectAdmin(t, c.what, l.f.change(l.s, c.method, c.path, c.body, newKey()), c.status, c.code)
	}
	expectAdmin(t, "read an account's preset", l.f.do(http.MethodGet, "/routing-presets/"+l.account, "", as(l.s)), http.StatusNotFound, codeNotFound)
	if n := l.f.auditCount("routing.preset.create"); n != "3" {
		t.Errorf("%s presets made, want the 3 made before the refusals", n)
	}
}

// TestAdminRoutingPresets: the admin changes a site's preset into a new version, points the platform's default at a
// preset that limits no price and the site's at a priced platform one, cannot disable a preset a default points at,
// lists each layer's own presets, and every change is audited with its reason (docs/domains/routing.md「接口、权限与
// 审计」).
func TestAdminRoutingPresets(t *testing.T) {
	t.Parallel()
	l := newAdminLayers(t)
	f, s, official := l.f, l.s, l.official
	rec := f.change(s, http.MethodPut, "/routing-presets/"+official.Id.String(),
		`{"name":"Official, cheapest first","revision":1,"rules":{"default":{"classes":["official"],"sort":"price"}},"reason":"sort by price"}`, newKey())
	expectAdmin(t, "update the site's preset", rec, http.StatusOK, "")
	decode(t, rec, &official)
	if official.Revision != 2 || official.Version != 2 || official.Scope != adminapi.RoutingPresetScopeSite {
		t.Errorf("updated %+v", official)
	}
	expectAdmin(t, "set the platform's default", f.change(s, http.MethodPut, "/settings/routing",
		`{"preset_id":"`+l.open.Id.String()+`","version":`+l.platformVersion+`,"reason":"open"}`, newKey()), http.StatusOK, "")
	expectAdmin(t, "set the site's default to a platform preset", f.change(s, http.MethodPut, l.site+"/routing-default",
		`{"preset_id":"`+l.cheap.Id.String()+`","version":0,"reason":"cheap"}`, newKey()), http.StatusOK, "")
	var d adminapi.RoutingDefault
	decode(t, f.do(http.MethodGet, l.site+"/routing-default", "", as(s)), &d)
	if d.PresetId == nil || *d.PresetId != l.cheap.Id || d.Version < 1 {
		t.Fatalf("the site's default %+v", d)
	}
	// The two defaults stay on presets the platform's and the site's keys can use.
	expectAdmin(t, "prices on the platform's default", f.change(s, http.MethodPut, "/routing-presets/"+l.open.Id.String(),
		`{"name":"Open","currency":"USD","revision":1,"rules":{"default":{"max_price":{"prompt":"1"}}},"reason":"r"}`, newKey()), http.StatusBadRequest, codeInvalidRequest)
	expectAdmin(t, "the site's default in another currency", f.change(s, http.MethodPut, "/routing-presets/"+l.cheap.Id.String(),
		`{"name":"Cheap","currency":"EUR","revision":1,"rules":{"default":{"max_price":{"prompt":"1"}}},"reason":"r"}`, newKey()), http.StatusBadRequest, codeInvalidRequest)
	expectAdmin(t, "disable the site's default", f.change(s, http.MethodDelete, "/routing-presets/"+l.cheap.Id.String(), `{"revision":1,"reason":"r"}`, newKey()),
		http.StatusConflict, codePresetInUse)
	expectAdmin(t, "disable the site's preset", f.change(s, http.MethodDelete, "/routing-presets/"+official.Id.String(), `{"revision":2,"reason":"replaced"}`, newKey()),
		http.StatusNoContent, "")
	for path, want := range map[string][]string{
		"/routing-presets":          {"allow-all", "cheap", "open"},
		l.site + "/routing-presets": {"official (disabled)"},
	} {
		if got := f.adminPresetSlugs(s, path); !slices.Equal(got, want) {
			t.Errorf("%s lists %v, want %v", path, got, want)
		}
	}
	if got, want := f.text(`SELECT string_agg(action || ':' || reason, ',' ORDER BY id) FROM platform.audit_log WHERE module = 'routing'`),
		"routing.preset.create:a cheap baseline,routing.preset.create:open baseline,routing.preset.create:site baseline,"+
			"routing.preset.update:sort by price,routing.default.update:open,routing.default.update:cheap,routing.preset.disable:replaced"; got != want {
		t.Errorf("audited %s, want %s", got, want)
	}
}

// boundKeys is the SQL that writes n keys of a new account of the main site bound to preset, deleted ones when
// deleted, made by a user of the site.
func boundKeys(preset string, n int, deleted bool) string {
	account, workspace, gone := uuid.NewV7().String(), uuid.NewV7().String(), "NULL"
	if deleted {
		gone = "now()"
	}
	return `INSERT INTO accounts.accounts (id, site_id, kind, name, status, tier_id)
			SELECT '` + account + `', id, 'personal', 'Bound', 'active', default_tier_id FROM accounts.sites WHERE is_main;
		INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default)
			SELECT '` + workspace + `', site_id, id, 'Default', 'active', true FROM accounts.accounts WHERE id = '` + account + `';
		INSERT INTO accounts.api_keys (id, site_id, account_id, workspace_id, kind, name, prefix, last4, secret_hash,
			secret_version, created_by, status, preset_id, deleted_at)
			SELECT uuidv7(), a.site_id, a.id, '` + workspace + `', 'call', 'k', 'sk-ax-v1-', 'abcd',
				sha256(convert_to(gen_random_uuid()::text, 'UTF8')), 1, (SELECT id FROM accounts.users WHERE site_id = a.site_id LIMIT 1),
				'active', '` + preset + `', ` + gone + `
			FROM accounts.accounts a, generate_series(1, ` + strconv.Itoa(n) + `) WHERE a.id = '` + account + `'`
}

// TestAdminRoutingPresetKeys: the admin's presets carry the keys of every account bound to them, deleted keys left
// out, when listed and read; the system preset carries none, since every unbound key follows it too.
func TestAdminRoutingPresetKeys(t *testing.T) {
	t.Parallel()
	l := newAdminLayers(t)
	f, s := l.f, l.s
	f.run(boundKeys(l.open.Id.String(), 2, false), boundKeys(l.open.Id.String(), 3, false),
		boundKeys(l.open.Id.String(), 1, true), boundKeys(l.official.Id.String(), 1, false))
	var list adminapi.RoutingPresetList
	decode(t, f.do(http.MethodGet, "/routing-presets", "", as(s)), &list)
	got := map[string]*int64{}
	for _, p := range list.Data {
		got[p.Slug] = p.Keys
	}
	var one adminapi.RoutingPreset
	decode(t, f.do(http.MethodGet, "/routing-presets/"+l.official.Id.String(), "", as(s)), &one)
	got["read official"] = one.Keys
	for slug, want := range map[string]int64{"open": 5, "cheap": 0, "read official": 1} {
		if got[slug] == nil || *got[slug] != want {
			t.Errorf("%s has keys %v, want %d", slug, got[slug], want)
		}
	}
	if got["allow-all"] != nil {
		t.Errorf("the system preset counted %d keys", *got["allow-all"])
	}
}

// TestAdminRoutingPresetLimits: the admin reads the limits with their defaults, saves some once under one idempotency
// key with one audit entry, and the next platform preset is held to them; a change against a revision that moved on is
// 409 stale_revision, a live limit above its layer's total is refused by name, and saving none goes back to the
// defaults.
func TestAdminRoutingPresetLimits(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	const path = "/settings/routing-limits"
	layer := func(active, total int64) *adminapi.RoutingLayerLimits {
		return &adminapi.RoutingLayerLimits{Active: new(active), Total: new(total)}
	}
	f.wantLimits(s, adminapi.RoutingPresetLimitsView{Defaults: adminapi.RoutingPresetLimits{
		Platform: layer(50, 500), Site: layer(50, 500), Account: layer(20, 200), Workspace: layer(10, 100), KeptVersions: new(int64(50)),
		Global: &adminapi.RoutingGlobalBudget{Presets: new(int64(100_000)), Bytes: new(int64(256 << 20))},
	}})

	body := `{"limits":{"platform":{"active":1},"kept_versions":60},"rev":0,"reason":"one platform preset"}`
	key := newKey()
	expectAdmin(t, "save", f.change(s, http.MethodPut, path, body, key), http.StatusOK, "")
	if rec := f.change(s, http.MethodPut, path, body, key); rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("the second save was not replayed: %d %s", rec.Code, rec.Body)
	}
	if n := f.auditCount("routing.limits.update"); n != "1" {
		t.Fatalf("%s audit entries, want 1", n)
	}
	f.wantLimits(s, adminapi.RoutingPresetLimitsView{Rev: 1, Limits: adminapi.RoutingPresetLimits{
		Platform: &adminapi.RoutingLayerLimits{Active: new(int64(1))}, KeptVersions: new(int64(60)),
	}})
	expectAdmin(t, "a change against the revision before", f.change(s, http.MethodPut, path,
		`{"limits":{},"rev":0,"reason":"r"}`, newKey()), http.StatusConflict, "stale_revision")
	// The system preset is the platform's one live preset.
	expectAdmin(t, "a platform preset over the limit", f.change(s, http.MethodPost, "/routing-presets",
		`{"slug":"over","name":"Over","rules":{"default":{}},"reason":"r"}`, newKey()), http.StatusConflict, "preset_quota")

	rec := f.change(s, http.MethodPut, path, `{"limits":{"account":{"active":300}},"rev":1,"reason":"r"}`, newKey())
	expectAdmin(t, "a live limit above the default total", rec, http.StatusBadRequest, "")
	if !strings.Contains(rec.Body.String(), "/limits/account/active") {
		t.Errorf("the refusal does not name the field: %s", rec.Body)
	}
	expectAdmin(t, "save none", f.change(s, http.MethodPut, path, `{"limits":{},"rev":1,"reason":"defaults"}`, newKey()), http.StatusOK, "")
	f.adminPreset(s, "/routing-presets", `{"slug":"back","name":"Back","rules":{"default":{}},"reason":"r"}`)
}

// wantLimits checks the limits the admin reads: the saved ones as want has them, and the defaults when want names
// them.
func (f adminFixture) wantLimits(s session, want adminapi.RoutingPresetLimitsView) {
	f.t.Helper()
	var got adminapi.RoutingPresetLimitsView
	decode(f.t, f.do(http.MethodGet, "/settings/routing-limits", "", as(s)), &got)
	if got.Rev != want.Rev {
		f.t.Errorf("the limits are at rev %d, want %d", got.Rev, want.Rev)
	}
	if diff := cmp.Diff(want.Limits, got.Limits); diff != "" {
		f.t.Errorf("the saved limits (-want +got):\n%s", diff)
	}
	if want.Defaults != (adminapi.RoutingPresetLimits{}) {
		if diff := cmp.Diff(want.Defaults, got.Defaults); diff != "" {
			f.t.Errorf("the defaults (-want +got):\n%s", diff)
		}
	}
}

// TestAdminRoutingPresetSuspension: the platform suspends routing presets with a reason, once however often it is
// asked, and while they are suspended a preset write is refused 503 `presets_suspended`; resumed, it passes again.
func TestAdminRoutingPresetSuspension(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	const path = "/settings/routing-suspension"
	read := func() (got adminapi.RoutingPresetSuspension) {
		decode(t, f.do(http.MethodGet, path, "", as(s)), &got)
		return got
	}
	if got := read(); got.Suspended || got.Since != nil {
		t.Fatalf("presets start suspended: %+v", got)
	}
	expectAdmin(t, "suspend without a reason", f.change(s, http.MethodPut, path, `{"suspended":true}`, newKey()), http.StatusBadRequest, "")
	key := newKey()
	expectAdmin(t, "suspend", f.change(s, http.MethodPut, path, `{"suspended":true,"reason":"incident"}`, key), http.StatusOK, "")
	if rec := f.change(s, http.MethodPut, path, `{"suspended":true,"reason":"incident"}`, key); rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("the second suspension was not replayed: %d %s", rec.Code, rec.Body)
	}
	suspended := read()
	if !suspended.Suspended || suspended.Since == nil {
		t.Fatalf("presets after the suspension: %+v", suspended)
	}
	expectAdmin(t, "suspend again", f.change(s, http.MethodPut, path, `{"suspended":true,"reason":"still"}`, newKey()), http.StatusOK, "")
	if again := read(); !again.Since.Equal(*suspended.Since) {
		t.Errorf("suspending again moved the time to %v from %v", again.Since, suspended.Since)
	}
	if n := f.auditCount("routing.presets.suspend"); n != "1" {
		t.Errorf("%s suspensions audited, want 1", n)
	}
	const preset = `{"slug":"later","name":"Later","rules":{"default":{}},"reason":"r"}`
	expectAdmin(t, "a preset while suspended", f.change(s, http.MethodPost, "/routing-presets", preset, newKey()), http.StatusServiceUnavailable, "presets_suspended")

	expectAdmin(t, "resume", f.change(s, http.MethodPut, path, `{"suspended":false,"reason":"fixed"}`, newKey()), http.StatusOK, "")
	if got := read(); got.Suspended || got.Since != nil {
		t.Errorf("presets after resuming: %+v", got)
	}
	f.adminPreset(s, "/routing-presets", preset)
}
