package main

import (
	"net/http"
	"testing"

	"axiom/internal/platform/adminapi"
)

// TestAdminTiers: the admin adds a tier, renames it, makes it the default and retires the old one, and each refusal
// is the one the contract names: a taken name, a disabled default, a disabled tier as the default.
func TestAdminTiers(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	rec := f.change(owner, http.MethodPost, "/tiers", `{"name":"Pro","reason":"a paid tier"}`, newKey())
	expectAdmin(t, "create", rec, http.StatusCreated, "")
	var pro adminapi.TierRef
	decode(t, rec, &pro)
	old := ""
	rec = f.do(http.MethodGet, "/tiers", "", as(owner))
	expectAdmin(t, "list", rec, http.StatusOK, "")
	var tiers adminapi.TierList
	decode(t, rec, &tiers)
	for _, tier := range tiers.Data {
		if tier.Default != nil && *tier.Default {
			old = tier.Id
		}
	}
	if old == "" || pro.Default == nil || *pro.Default {
		t.Fatalf("the list names no default tier, or the new one is: %+v, %+v", tiers, pro)
	}
	for _, c := range []struct {
		name, method, path, body string
		status                   int
		code                     errorCode
	}{
		{"a taken name", http.MethodPost, "/tiers", `{"name":"pro","reason":"again"}`, http.StatusConflict, codeNameTaken},
		{"disable the default", http.MethodPatch, "/tiers/" + old, `{"disabled":true,"reason":"retire"}`, http.StatusConflict, codeConflict},
		{"rename and disable", http.MethodPatch, "/tiers/" + pro.Id, `{"name":"Plus","disabled":true,"reason":"later"}`, http.StatusOK, ""},
		{"a disabled default", http.MethodPut, "/tiers/" + pro.Id + "/default", `{"reason":"promote"}`, http.StatusUnprocessableEntity, codeUnprocessable},
		{"enable", http.MethodPatch, "/tiers/" + pro.Id, `{"disabled":false,"reason":"now"}`, http.StatusOK, ""},
		{"make it the default", http.MethodPut, "/tiers/" + pro.Id + "/default", `{"reason":"promote"}`, http.StatusNoContent, ""},
		{"retire the old default", http.MethodPatch, "/tiers/" + old, `{"disabled":true,"reason":"retire"}`, http.StatusOK, ""},
		{"an unknown tier", http.MethodPatch, "/tiers/tier_00000000000000000000000000", `{"name":"X","reason":"r"}`, http.StatusNotFound, codeNotFound},
	} {
		expectAdmin(t, c.name, f.change(owner, c.method, c.path, c.body, newKey()), c.status, c.code)
	}
	if got := f.text("SELECT t.name FROM accounts.sites s JOIN accounts.tiers t ON t.id = s.default_tier_id LIMIT 1"); got != "Plus" {
		t.Errorf("the default tier is %q, want Plus", got)
	}
	f.expectAudit("site.default_tier_change", map[string]string{"reason": "promote", "detail->>'after'": "Plus"})
}
