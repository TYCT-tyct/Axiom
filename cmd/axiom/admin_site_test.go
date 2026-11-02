package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
)

// getSettings reads the settings of the site.
func (f adminFixture) getSettings(s session, site string) adminapi.SiteSettingsView {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/sites/"+site+"/settings", "", as(s))
	expectAdmin(f.t, "getSiteSettings", rec, http.StatusOK, "")
	var view adminapi.SiteSettingsView
	decode(f.t, rec, &view)
	return view
}

// TestSiteSettings: a site tells its own settings and the defaults that apply where it sets none; a PUT replaces the
// site's own values whole, so what it leaves out falls back to the default; each change is audited with its reason, the
// values it replaced and the values it set.
func TestSiteSettings(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	site := f.siteID()
	defaults := adminapi.SiteSettings{
		ConsoleIdleTimeoutSeconds: new(7 * 24 * 3600), ConsoleMaxAgeSeconds: new(30 * 24 * 3600),
		AdminIdleTimeoutSeconds: new(1800), AdminMaxAgeSeconds: new(12 * 3600),
		NewKeyDailyLimit:  &adminapi.Money{Amount: "10000000000", Currency: "USD"},
		ModelPricesPublic: new(true),
		BillingPreference: new(adminapi.BillingPreferenceName("subscription_first")),
	}
	if diff := cmp.Diff(adminapi.SiteSettingsView{Defaults: defaults}, f.getSettings(s, site)); diff != "" {
		t.Errorf("the settings of a site that sets none (-want +got):\n%s", diff)
	}

	body := `{"settings":{"console_idle_timeout_seconds":3600,"admin_max_age_seconds":7200,"new_key_daily_limit":{"amount":"5000000000","currency":"USD"}},"reason":"tighten"}`
	key := newKey()
	rec := f.change(s, http.MethodPut, "/sites/"+site+"/settings", body, key)
	expectAdmin(t, "updateSiteSettings", rec, http.StatusOK, "")
	want := adminapi.SiteSettingsView{
		Settings: adminapi.SiteSettings{
			ConsoleIdleTimeoutSeconds: new(3600), AdminMaxAgeSeconds: new(7200),
			NewKeyDailyLimit: &adminapi.Money{Amount: "5000000000", Currency: "USD"},
		},
		Defaults: defaults,
	}
	var got adminapi.SiteSettingsView
	decode(t, rec, &got)
	if diff := cmp.Diff(want, got); diff != "" || rec.Header().Get("Idempotency-Replayed") != "" {
		t.Errorf("the settings as stored (-want +got) %q:\n%s", rec.Header().Get("Idempotency-Replayed"), diff)
	}
	if diff := cmp.Diff(want, f.getSettings(s, site)); diff != "" {
		t.Errorf("getSiteSettings after the change (-want +got):\n%s", diff)
	}
	f.expectAudit("site_settings.update", map[string]string{
		"actor_kind || ':' || actor_id": "user:" + f.text("SELECT id::text FROM accounts.users WHERE email = '"+ownerEmail+"'"),
		"reason":                        "tighten",
		"coalesce(detail->'after'->>'admin_max_age_seconds', 'none')":      "7200",
		"coalesce(detail->'after'->>'new_key_daily_limit', 'none')":        "5000000000",
		"coalesce(detail->'after'->>'admin_idle_timeout_seconds', 'none')": "none",
		"coalesce(detail->'after'->>'model_prices_public', 'none')":        "none",
		"coalesce(detail->'before'->>'admin_max_age_seconds', 'none')":     "none",
	})

	// The same request again, after a lost response, is not done again.
	rec = f.change(s, http.MethodPut, "/sites/"+site+"/settings", body, key)
	expectAdmin(t, "the same request again", rec, http.StatusOK, "")
	var again adminapi.SiteSettingsView
	decode(t, rec, &again)
	if diff := cmp.Diff(want, again); diff != "" || rec.Header().Get("Idempotency-Replayed") != "true" || f.auditCount("site_settings.update") != "1" {
		t.Errorf("the replay (-want +got) replayed=%q audits=%s:\n%s", rec.Header().Get("Idempotency-Replayed"), f.auditCount("site_settings.update"), diff)
	}
	// The key is the first request's: another body, another operation or another operator is refused and changes nothing.
	expectAdmin(t, "the key with another body", f.change(s, http.MethodPut, "/sites/"+site+"/settings", strings.Replace(body, "3600", "3700", 1), key),
		http.StatusConflict, codeIdempotencyReuse)
	expectAdmin(t, "the key for another operation", f.change(s, http.MethodPost, someAccount+"/credit-grants",
		`{"amount":{"amount":"1","currency":"USD"},"reason":"r"}`, key), http.StatusConflict, codeIdempotencyReuse)

	// A PUT replaces the site's own values: the ones it leaves out are the defaults again.
	rec = f.change(s, http.MethodPut, "/sites/"+site+"/settings", `{"settings":{"admin_idle_timeout_seconds":900},"reason":"loosen"}`, newKey())
	expectAdmin(t, "a replacement", rec, http.StatusOK, "")
	if diff := cmp.Diff(adminapi.SiteSettingsView{Settings: adminapi.SiteSettings{AdminIdleTimeoutSeconds: new(900)}, Defaults: defaults}, f.getSettings(s, site)); diff != "" {
		t.Errorf("the settings after a replacement (-want +got):\n%s", diff)
	}
	if f.auditCount("site_settings.update") != "2" {
		t.Errorf("%s audit entries, want 2", f.auditCount("site_settings.update"))
	}

	checkModelPricesVisibility(t, f, s, site, defaults)
}

// checkModelPricesVisibility hides the site's prices, checks the stored value and the audit entry, then saves another
// setting from a page built before the field existed and checks the stored false survives.
func checkModelPricesVisibility(t *testing.T, f adminFixture, s session, site string, defaults adminapi.SiteSettings) {
	t.Helper()
	hidden := false
	rec := f.change(s, http.MethodPut, "/sites/"+site+"/settings", `{"settings":{"model_prices_public":false},"reason":"members only"}`, newKey())
	expectAdmin(t, "prices kept to members", rec, http.StatusOK, "")
	wantHidden := adminapi.SiteSettingsView{Settings: adminapi.SiteSettings{ModelPricesPublic: &hidden}, Defaults: defaults}
	if diff := cmp.Diff(wantHidden, f.getSettings(s, site)); diff != "" {
		t.Errorf("the settings after hiding prices (-want +got):\n%s", diff)
	}
	if got := f.text(`SELECT coalesce(detail->'after'->>'model_prices_public', 'none') FROM platform.audit_log
		WHERE action = 'site_settings.update' ORDER BY id DESC LIMIT 1`); got != "false" {
		t.Errorf("the audit entry says model_prices_public %q, want false", got)
	}
	if got := f.text(`SELECT reason FROM platform.audit_log
		WHERE action = 'site_settings.update' ORDER BY id DESC LIMIT 1`); got != "members only" {
		t.Errorf("the audit entry's reason is %q, want members only", got)
	}
	rec = f.change(s, http.MethodPut, "/sites/"+site+"/settings", `{"settings":{"admin_idle_timeout_seconds":900},"reason":"an old page"}`, newKey())
	expectAdmin(t, "an old page's replacement", rec, http.StatusOK, "")
	wantOld := adminapi.SiteSettingsView{
		Settings: adminapi.SiteSettings{AdminIdleTimeoutSeconds: new(900), ModelPricesPublic: &hidden},
		Defaults: defaults,
	}
	if diff := cmp.Diff(wantOld, f.getSettings(s, site)); diff != "" {
		t.Errorf("the settings after an old page's replacement (-want +got):\n%s", diff)
	}
	if f.auditCount("site_settings.update") != "4" {
		t.Errorf("%s audit entries, want 4", f.auditCount("site_settings.update"))
	}
}

// TestSiteSettingsRefusals: a setting outside its bounds, in a currency that is not the site's, or for a site that is
// not there is refused as the contract says, with the part of the request named, and changes nothing; the key of a
// refused request is free for the corrected one.
func TestSiteSettingsRefusals(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	site := f.siteID()
	key := newKey()
	for _, c := range []struct{ name, body, pointer string }{
		{"a session shorter than 5 minutes", `{"settings":{"admin_idle_timeout_seconds":299},"reason":"r"}`, "/settings/admin_idle_timeout_seconds"},
		{"an admin session longer than a day", `{"settings":{"admin_max_age_seconds":86401},"reason":"r"}`, "/settings/admin_max_age_seconds"},
		{"a console idle time past 30 days", `{"settings":{"console_idle_timeout_seconds":2592001},"reason":"r"}`, "/settings/console_idle_timeout_seconds"},
		{"a billing preference outside the four", `{"settings":{"billing_preference":"subscription"},"reason":"r"}`, "/settings/billing_preference"},
		{"a limit in another currency", `{"settings":{"new_key_daily_limit":{"amount":"1","currency":"CNY"}},"reason":"r"}`, "/settings/new_key_daily_limit/currency"},
		{"a limit over a million units", `{"settings":{"new_key_daily_limit":{"amount":"1000000000000001","currency":"USD"}},"reason":"r"}`, "/settings/new_key_daily_limit"},
		{"a negative limit", `{"settings":{"new_key_daily_limit":{"amount":"-1","currency":"USD"}},"reason":"r"}`, "/settings/new_key_daily_limit"},
		{"an amount past 64 bits", `{"settings":{"new_key_daily_limit":{"amount":"9999999999999999999","currency":"USD"}},"reason":"r"}`, "/settings/new_key_daily_limit/amount"},
		{"no reason", `{"settings":{}}`, "/reason"},
		{"an empty reason", `{"settings":{},"reason":""}`, "/reason"},
	} {
		rec := f.change(s, http.MethodPut, "/sites/"+site+"/settings", c.body, key)
		expectAdmin(t, c.name, rec, http.StatusBadRequest, codeInvalidRequest)
		fields := errorDetailOf(t, rec).Fields
		if fields == nil || len(*fields) == 0 || (*fields)[0].Pointer != c.pointer {
			t.Errorf("%s: fields %v, want %s", c.name, fields, c.pointer)
		}
	}
	if f.auditCount("site_settings.update") != "0" {
		t.Error("a refused change was audited")
	}
	expectAdmin(t, "the corrected request with the same key", f.change(s, http.MethodPut, "/sites/"+site+"/settings", `{"settings":{},"reason":"r"}`, key), http.StatusOK, "")

	other := "site_00000000000000000000000000"
	for _, id := range []string{other, "site_zzzzzzzzzzzzzzzzzzzzzzzzzz", "acct_00000000000000000000000000"} {
		expectAdmin(t, "getSiteSettings of "+id, f.do(http.MethodGet, "/sites/"+id+"/settings", "", as(s)), http.StatusNotFound, codeNotFound)
		expectAdmin(t, "updateSiteSettings of "+id, f.change(s, http.MethodPut, "/sites/"+id+"/settings", `{"settings":{},"reason":"r"}`, newKey()), http.StatusNotFound, codeNotFound)
	}
}

// TestShorterSessionsEndOnTheNextRequest: a site that shortens its sessions shortens the ones already made, by their
// idle time and by their age, from their next request on; it never lengthens one.
func TestShorterSessionsEndOnTheNextRequest(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	f.addUser("idle@example.com", "viewer")
	f.addUser("old@example.com", "viewer")
	owner, idle, old := f.owner(), f.verified("idle@example.com"), f.verified("old@example.com")
	f.run(`UPDATE accounts.sessions SET created_at = now() - interval '2 hours'
		WHERE user_id = (SELECT id FROM accounts.users WHERE email = 'old@example.com')`)
	for _, s := range []session{idle, old} {
		expectAdmin(t, "before", f.do(http.MethodGet, "/users", "", as(s)), http.StatusOK, "")
	}
	// A request touches its session, so the idle one is aged after its last: nothing may use it before the change.
	f.run(`UPDATE accounts.sessions SET last_seen_at = now() - interval '10 minutes'
		WHERE user_id = (SELECT id FROM accounts.users WHERE email = 'idle@example.com')`)
	site := f.siteID()
	rec := f.change(owner, http.MethodPut, "/sites/"+site+"/settings",
		`{"settings":{"admin_idle_timeout_seconds":300,"admin_max_age_seconds":3600},"reason":"shorter"}`, newKey())
	expectAdmin(t, "updateSiteSettings", rec, http.StatusOK, "")
	// The handler's scope reaches every account's sessions: each one carries the shorter lifetimes itself.
	if n := f.text(`SELECT count(*)::text FROM accounts.sessions WHERE surface = 'admin' AND revoked_at IS NULL
		AND (idle_timeout > interval '300 seconds' OR expires_at > created_at + interval '3600 seconds')`); n != "0" {
		t.Errorf("%s admin sessions still hold the longer lifetimes", n)
	}
	expectAdmin(t, "the session idle past the new idle time", f.do(http.MethodGet, "/users", "", as(idle)), http.StatusUnauthorized, codeUnauthenticated)
	expectAdmin(t, "the session older than the new age", f.do(http.MethodGet, "/users", "", as(old)), http.StatusUnauthorized, codeUnauthenticated)
	expectAdmin(t, "the owner's, new enough and in use", f.do(http.MethodGet, "/users", "", as(owner)), http.StatusOK, "")
	var info adminapi.Session
	decode(t, f.do(http.MethodGet, "/session", "", as(owner)), &info)
	if left := int(time.Until(info.ExpiresAt).Seconds()); left > 300 {
		t.Errorf("getSession says the session ends in %d seconds, more than the idle time of 300", left)
	}
}

// TestTiers: a site's tiers by name, the main site's unless the request names one.
func TestTiers(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	f.run(`INSERT INTO accounts.tiers (id, site_id, name) SELECT uuidv7(), id, 'Beta' FROM accounts.sites WHERE is_main`)
	rec := f.do(http.MethodGet, "/tiers", "", as(s))
	expectAdmin(t, "listTiers", rec, http.StatusOK, "")
	var list adminapi.TierList
	decode(t, rec, &list)
	if len(list.Data) != 2 || list.Data[0].Name != "Beta" || list.Data[1].Name != "Default" || !strings.HasPrefix(list.Data[0].Id, "tier_") {
		t.Errorf("the tiers: %+v", list)
	}
	named := f.do(http.MethodGet, "/tiers?site_id="+f.siteID(), "", as(s))
	if named.Body.String() != rec.Body.String() {
		t.Errorf("the main site named: %s, want %s", named.Body, rec.Body)
	}
	child, childTier := uuid.NewV7(), uuid.NewV7()
	f.run("BEGIN",
		fmt.Sprintf(`INSERT INTO accounts.sites (id,parent_site_id,owner_account_id,name,status,key_prefix,registration,default_tier_id)
			SELECT '%s',id,(SELECT a.id FROM accounts.accounts a WHERE a.site_id=s.id LIMIT 1),'Other','active','sk-other-','closed','%s' FROM accounts.sites s WHERE s.is_main`, child, childTier),
		fmt.Sprintf(`INSERT INTO accounts.tiers(id,site_id,name) VALUES ('%s','%s','Other')`, childTier, child), "COMMIT")
	rec = f.do(http.MethodGet, "/tiers?site_id="+accounts.SiteID(child).TypeID(), "", as(s))
	expectAdmin(t, "a second site's tiers", rec, http.StatusOK, "")
	decode(t, rec, &list)
	if len(list.Data) != 1 || list.Data[0].Name != "Other" {
		t.Errorf("the second site's tiers: %+v", list)
	}
	rec = f.do(http.MethodGet, "/tiers?site_id=site_00000000000000000000000000", "", as(s))
	expectAdmin(t, "a site with no tiers", rec, http.StatusOK, "")
	decode(t, rec, &list)
	if len(list.Data) != 0 {
		t.Errorf("a site that is not there has tiers: %+v", list)
	}
	expectAdmin(t, "a malformed site", f.do(http.MethodGet, "/tiers?site_id=nonsense", "", as(s)), http.StatusBadRequest, codeInvalidRequest)
	expectAdmin(t, "a site ID that is no ID", f.do(http.MethodGet, "/tiers?site_id=site_zzzzzzzzzzzzzzzzzzzzzzzzzz", "", as(s)), http.StatusBadRequest, codeInvalidRequest)
}

// TestSiteSettingsLogAFailedReload: a change that is saved still succeeds when this node cannot reload the sites at
// once, and the failure is logged for whoever looks into the old settings it keeps using.
func TestSiteSettingsLogAFailedReload(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	f.run("ALTER TABLE accounts.roles RENAME TO roles_away")
	rec := f.change(s, http.MethodPut, "/sites/"+f.siteID()+"/settings", `{"settings":{},"reason":"r"}`, newKey())
	expectAdmin(t, "a change whose reload fails", rec, http.StatusOK, "")
	if !strings.Contains(f.logs.String(), "did not reload") {
		t.Errorf("the failed reload was not logged: %s", f.logs)
	}
}

// TestSiteRegistration: the policy reads with the code defaults; a PUT replaces every value, normalizes domain lists
// and audits the change; a value outside its bounds is refused by name, and a repeated key replays the stored result.
func TestSiteRegistration(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	site := f.siteID()
	checkRegistrationDefaults(t, f, s, site)
	checkRegistrationWrite(t, f, s, site)
	checkRegistrationRefusals(t, f, s, site)
}

// checkRegistrationDefaults reads the policy a site that never set one has.
func checkRegistrationDefaults(t *testing.T, f adminFixture, s session, site string) {
	t.Helper()
	rec := f.do(http.MethodGet, "/sites/"+site+"/registration", "", as(s))
	expectAdmin(t, "getSiteRegistration", rec, http.StatusOK, "")
	var got adminapi.RegistrationPolicyView
	decode(t, rec, &got)
	if got.Policy.Mode != "invite" || !got.Policy.HumanCheck || got.Policy.HumanCheckBits != 18 || got.Policy.DomainBurst != 5 || got.Policy.DomainPerHour != 30 {
		t.Errorf("the site's policy: %+v", got.Policy)
	}
}

// checkRegistrationWrite replaces the policy, reads the normalized lists back, checks the audit entry, and replays the
// same key.
func checkRegistrationWrite(t *testing.T, f adminFixture, s session, site string) {
	t.Helper()
	body := `{"policy":{"mode":"open","human_check":true,"human_check_bits":20,"domain_burst":7,"domain_per_hour":70,"domain_allow":[" Example.COM ","example.com"],"domain_deny":["Bad.Example.COM"]},"reason":"open registration"}`
	key := newKey()
	rec := f.change(s, http.MethodPut, "/sites/"+site+"/registration", body, key)
	expectAdmin(t, "updateSiteRegistration", rec, http.StatusOK, "")
	var updated adminapi.RegistrationPolicyView
	decode(t, rec, &updated)
	if updated.Policy.Mode != "open" || updated.Policy.HumanCheckBits != 20 || updated.Policy.DomainBurst != 7 ||
		updated.Policy.DomainPerHour != 70 || len(updated.Policy.DomainAllow) != 1 || updated.Policy.DomainAllow[0] != "example.com" ||
		len(updated.Policy.DomainDeny) != 1 || updated.Policy.DomainDeny[0] != "bad.example.com" {
		t.Errorf("the stored policy: %+v", updated.Policy)
	}
	if f.auditCount("site_registration.update") != "1" {
		t.Fatalf("%s audit entries, want 1", f.auditCount("site_registration.update"))
	}
	f.expectAudit("site_registration.update", map[string]string{
		"reason": "open registration",
		"coalesce(detail->'after'->>'mode', 'none')":             "open",
		"coalesce(detail->'after'->>'human_check_bits', 'none')": "20",
	})
	rec = f.change(s, http.MethodPut, "/sites/"+site+"/registration", body, key)
	expectAdmin(t, "the same registration request again", rec, http.StatusOK, "")
	if rec.Header().Get("Idempotency-Replayed") != "true" || f.auditCount("site_registration.update") != "1" {
		t.Errorf("replay header %q, audits %s", rec.Header().Get("Idempotency-Replayed"), f.auditCount("site_registration.update"))
	}
}

// checkRegistrationRefusals names the offending field of an out-of-bounds difficulty and reports a site or account
// that is not there.
func checkRegistrationRefusals(t *testing.T, f adminFixture, s session, site string) {
	t.Helper()
	rec := f.change(s, http.MethodPut, "/sites/"+site+"/registration",
		`{"policy":{"mode":"open","human_check":true,"human_check_bits":11,"domain_burst":7,"domain_per_hour":70,"domain_allow":[],"domain_deny":[]},"reason":"r"}`, newKey())
	expectAdmin(t, "a difficulty outside its bounds", rec, http.StatusBadRequest, codeInvalidRequest)
	fields := errorDetailOf(t, rec).Fields
	if fields == nil || len(*fields) == 0 || (*fields)[0].Pointer != "/policy/human_check_bits" {
		t.Errorf("fields %v, want /policy/human_check_bits", fields)
	}
	for _, id := range []string{"site_00000000000000000000000000", "acct_00000000000000000000000000"} {
		expectAdmin(t, "getSiteRegistration of "+id, f.do(http.MethodGet, "/sites/"+id+"/registration", "", as(s)), http.StatusNotFound, codeNotFound)
	}
}

// TestSiteBrand: a site with none reads as the default palette; a PUT stores a colour lower case and audits it, an empty
// colour clears it, a value of another form names its field, and a repeated key replays the stored result.
func TestSiteBrand(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	site := f.siteID()

	rec := f.do(http.MethodGet, "/sites/"+site+"/brand", "", as(s))
	expectAdmin(t, "getSiteBrand", rec, http.StatusOK, "")
	var got adminapi.SiteBrand
	decode(t, rec, &got)
	if got.BrandColor != nil {
		t.Errorf("a site with no brand colour: %+v", got)
	}

	body := `{"brand_color":"#335DDF","reason":"the site's colour"}`
	key := newKey()
	rec = f.change(s, http.MethodPut, "/sites/"+site+"/brand", body, key)
	expectAdmin(t, "updateSiteBrand", rec, http.StatusOK, "")
	var updated adminapi.SiteBrand
	decode(t, rec, &updated)
	if updated.BrandColor == nil || *updated.BrandColor != "#335ddf" {
		t.Errorf("the stored colour: %+v", updated)
	}
	if f.auditCount("site_brand.update") != "1" {
		t.Fatalf("%s audit entries, want 1", f.auditCount("site_brand.update"))
	}
	f.expectAudit("site_brand.update", map[string]string{
		"reason": "the site's colour",
		"coalesce(detail->'after'->>'brand_color', 'none')": "#335ddf",
	})

	rec = f.change(s, http.MethodPut, "/sites/"+site+"/brand", body, key)
	expectAdmin(t, "the same brand request again", rec, http.StatusOK, "")
	if rec.Header().Get("Idempotency-Replayed") != "true" || f.auditCount("site_brand.update") != "1" {
		t.Errorf("replay header %q, audits %s", rec.Header().Get("Idempotency-Replayed"), f.auditCount("site_brand.update"))
	}

	rec = f.change(s, http.MethodPut, "/sites/"+site+"/brand", `{"brand_color":"","reason":"back to the default"}`, newKey())
	expectAdmin(t, "clearing the brand colour", rec, http.StatusOK, "")
	var cleared adminapi.SiteBrand
	decode(t, rec, &cleared)
	if cleared.BrandColor != nil {
		t.Errorf("the colour after clearing: %+v", cleared)
	}

	rec = f.change(s, http.MethodPut, "/sites/"+site+"/brand", `{"brand_color":"blue","reason":"r"}`, newKey())
	expectAdmin(t, "a colour of another form", rec, http.StatusBadRequest, codeInvalidRequest)
	fields := errorDetailOf(t, rec).Fields
	if fields == nil || len(*fields) == 0 || (*fields)[0].Pointer != "/brand_color" {
		t.Errorf("fields %v, want /brand_color", fields)
	}
	for _, id := range []string{"site_00000000000000000000000000", "acct_00000000000000000000000000"} {
		expectAdmin(t, "getSiteBrand of "+id, f.do(http.MethodGet, "/sites/"+id+"/brand", "", as(s)), http.StatusNotFound, codeNotFound)
	}
}
