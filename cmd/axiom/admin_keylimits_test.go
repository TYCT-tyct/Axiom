package main

import (
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
)

// keyLimitsView is a limits view as the tests state it: the limits set at the level, and what is in force there.
func keyLimitsView(own adminapi.KeyLimits, maxKeys, perHour int) adminapi.KeyLimitsView {
	return adminapi.KeyLimitsView{Limits: own, Effective: adminapi.EffectiveKeyLimits{MaxKeys: maxKeys, CreationsPerHour: perHour}}
}

func (f adminFixture) siteKeyLimits(s session, site string) adminapi.KeyLimitsView {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/sites/"+site+"/key-limits", "", as(s))
	expectAdmin(f.t, "getSiteKeyLimits", rec, http.StatusOK, "")
	var view adminapi.KeyLimitsView
	decode(f.t, rec, &view)
	return view
}

func (f adminFixture) accountKeyLimits(s session, account string) adminapi.KeyLimitsView {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/accounts/"+account, "", as(s))
	expectAdmin(f.t, "getAccount", rec, http.StatusOK, "")
	var detail adminapi.AccountDetail
	decode(f.t, rec, &detail)
	return detail.KeyLimits
}

// TestKeyLimits: a site's limits on the keys of an account, and an account's own over them, are read and replaced whole
// through their own operations, each change audited with its reason and the values it replaced. The settings
// operation, which replaces the site's settings whole and which a page opened before these limits existed still sends
// in its old shape, leaves them as they are.
func TestKeyLimits(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	site, account := f.siteID(), f.accountOf(ownerEmail)
	if diff := cmp.Diff(keyLimitsView(adminapi.KeyLimits{}, 100, 50), f.siteKeyLimits(s, site)); diff != "" {
		t.Errorf("a site that sets none (-want +got):\n%s", diff)
	}

	key := newKey()
	body := `{"limits":{"max_keys":10,"creations_per_hour":5},"reason":"tighten"}`
	expectAdmin(t, "updateSiteKeyLimits", f.change(s, http.MethodPut, "/sites/"+site+"/key-limits", body, key), http.StatusOK, "")
	siteOwn := adminapi.KeyLimits{MaxKeys: new(10), CreationsPerHour: new(5)}
	if diff := cmp.Diff(keyLimitsView(siteOwn, 10, 5), f.siteKeyLimits(s, site)); diff != "" {
		t.Errorf("the site's limits after the change (-want +got):\n%s", diff)
	}
	f.expectAudit("site_key_limits.update", map[string]string{
		"reason": "tighten",
		"coalesce(detail->'after'->>'max_keys', 'none')":  "10",
		"coalesce(detail->'before'->>'max_keys', 'none')": "none",
	})
	rec := f.change(s, http.MethodPut, "/sites/"+site+"/key-limits", body, key)
	if rec.Header().Get("Idempotency-Replayed") != "true" || f.auditCount("site_key_limits.update") != "1" {
		t.Errorf("the same request again: replayed=%q, %s audit entries, want a replay and 1", rec.Header().Get("Idempotency-Replayed"), f.auditCount("site_key_limits.update"))
	}

	// The request a page opened before these limits existed sends has none in it: it replaces the settings and takes
	// nothing else along.
	expectAdmin(t, "the settings in the shape of an older page",
		f.change(s, http.MethodPut, "/sites/"+site+"/settings", `{"settings":{"admin_idle_timeout_seconds":900},"reason":"older page"}`, newKey()), http.StatusOK, "")
	if diff := cmp.Diff(keyLimitsView(siteOwn, 10, 5), f.siteKeyLimits(s, site)); diff != "" {
		t.Errorf("the site's limits after its settings were replaced (-want +got):\n%s", diff)
	}

	// An account falls back to its site, limit by limit, and may be given more or less than the site.
	if diff := cmp.Diff(keyLimitsView(adminapi.KeyLimits{}, 10, 5), f.accountKeyLimits(s, account)); diff != "" {
		t.Errorf("an account that sets none (-want +got):\n%s", diff)
	}
	expectAdmin(t, "updateAccountKeyLimits", f.change(s, http.MethodPut, "/accounts/"+account+"/key-limits",
		`{"limits":{"max_keys":200},"reason":"a big customer"}`, newKey()), http.StatusOK, "")
	if diff := cmp.Diff(keyLimitsView(adminapi.KeyLimits{MaxKeys: new(200)}, 200, 5), f.accountKeyLimits(s, account)); diff != "" {
		t.Errorf("an account with a limit of its own (-want +got):\n%s", diff)
	}
	f.expectAudit("account_key_limits.update", map[string]string{
		"reason": "a big customer",
		"coalesce(detail->'after'->>'max_keys', 'none')":  "200",
		"coalesce(detail->'before'->>'max_keys', 'none')": "none",
		"target_id": account,
	})
	// A replacement is whole: what it leaves out is the site's again.
	expectAdmin(t, "clearing the account's limits", f.change(s, http.MethodPut, "/accounts/"+account+"/key-limits", `{"limits":{},"reason":"back"}`, newKey()), http.StatusOK, "")
	if diff := cmp.Diff(keyLimitsView(adminapi.KeyLimits{}, 10, 5), f.accountKeyLimits(s, account)); diff != "" {
		t.Errorf("an account after its limits were cleared (-want +got):\n%s", diff)
	}
}

// TestKeyLimitsRefusals: a limit outside 1 to 10,000, a missing reason or an account or site that is not there is
// refused as the contract says, with the part of the request named, and changes nothing.
func TestKeyLimitsRefusals(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	site, account := f.siteID(), f.accountOf(ownerEmail)
	for _, path := range []string{"/sites/" + site + "/key-limits", "/accounts/" + account + "/key-limits"} {
		for _, c := range []struct{ name, body, pointer string }{
			{"no keys at all", `{"limits":{"max_keys":0},"reason":"r"}`, "/limits/max_keys"},
			{"ten thousand and one keys", `{"limits":{"max_keys":10001},"reason":"r"}`, "/limits/max_keys"},
			{"none an hour", `{"limits":{"creations_per_hour":0},"reason":"r"}`, "/limits/creations_per_hour"},
			{"ten thousand and one an hour", `{"limits":{"creations_per_hour":10001},"reason":"r"}`, "/limits/creations_per_hour"},
			{"no reason", `{"limits":{}}`, "/reason"},
		} {
			rec := f.change(s, http.MethodPut, path, c.body, newKey())
			expectAdmin(t, c.name+" "+path, rec, http.StatusBadRequest, codeInvalidRequest)
			fields := errorDetailOf(t, rec).Fields
			if fields == nil || len(*fields) == 0 || (*fields)[0].Pointer != c.pointer {
				t.Errorf("%s %s: fields %v, want %s", c.name, path, fields, c.pointer)
			}
		}
	}
	if f.auditCount("site_key_limits.update") != "0" || f.auditCount("account_key_limits.update") != "0" {
		t.Error("a refused change was audited")
	}
	for _, path := range []string{
		"/sites/site_00000000000000000000000000/key-limits", "/accounts/acct_00000000000000000000000000/key-limits",
	} {
		expectAdmin(t, "a limit change at "+path, f.change(s, http.MethodPut, path, `{"limits":{},"reason":"r"}`, newKey()), http.StatusNotFound, codeNotFound)
	}
}
