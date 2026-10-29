package main

import (
	"net/http"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

func TestAdminBusinessRateLimits(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	session := f.owner()
	account := f.accountOf(ownerEmail)
	tier := accounts.TierID(uuid.MustParse(f.text("SELECT tier_id::text FROM accounts.accounts LIMIT 1"))).TypeID()
	for _, path := range []string{"/rate-limits/tier/" + tier, "/rate-limits/account/" + account} {
		rec := f.do(http.MethodGet, path, "", as(session))
		expectAdmin(t, "getRateLimit", rec, http.StatusOK, "")
		var view adminapi.RateLimitView
		decode(t, rec, &view)
		if view.Requests != nil || view.EffectiveRequests == nil || *view.EffectiveRequests != accounts.DefaultRateRequests {
			t.Fatalf("default: %s", rec.Body)
		}
	}
	path := "/rate-limits/tier/" + tier
	key := newKey()
	body := `{"requests":2,"reason":"protect capacity"}`
	rec := f.change(session, http.MethodPut, path, body, key)
	expectAdmin(t, "setRateLimit", rec, http.StatusOK, "")
	f.expectAudit("rate_limit.update", map[string]string{"reason": "protect capacity", "coalesce(detail->>'before', 'none')": "none", "detail->>'after'": "2"})
	rec = f.change(session, http.MethodPut, path, body, key)
	if rec.Header().Get("Idempotency-Replayed") != "true" || f.auditCount("rate_limit.update") != "1" {
		t.Fatalf("replay: %v %s", rec.Header(), rec.Body)
	}
	rec = f.do(http.MethodGet, "/rate-limits/account/"+account, "", as(session))
	var view adminapi.RateLimitView
	decode(t, rec, &view)
	if view.Requests != nil || view.EffectiveRequests == nil || *view.EffectiveRequests != 2 {
		t.Fatalf("inherit: %s", rec.Body)
	}
	for _, setting := range []string{`0`, `3`, `null`} {
		rec = f.change(session, http.MethodPut, "/rate-limits/account/"+account, `{"requests":`+setting+`,"reason":"adjust"}`, newKey())
		expectAdmin(t, "setRateLimit", rec, http.StatusOK, "")
	}
}

func TestAdminBusinessRateLimitValidation(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	session := f.owner()
	account := f.accountOf(ownerEmail)
	path := "/rate-limits/account/" + account
	body := `{"requests":2,"reason":"capacity"}`
	for _, setting := range []string{`-1`, `1000000001`, `1.5`} {
		rec := f.change(session, http.MethodPut, path, `{"requests":`+setting+`,"reason":"invalid"}`, newKey())
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid %s: %d %s", setting, rec.Code, rec.Body)
		}
	}
	rec := f.change(session, http.MethodPut, "/rate-limits/key/"+account, body, newKey())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("wrong target: %d %s", rec.Code, rec.Body)
	}
}

func TestAdminBusinessKeyRateLimit(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	session := f.owner()
	site, err := accounts.ParseSiteID(f.siteID())
	if err != nil {
		t.Fatal(err)
	}
	account, err := accounts.ParseAccountID(f.accountOf(ownerEmail))
	if err != nil {
		t.Fatal(err)
	}
	user, err := accounts.ParseUserID(f.userOf(ownerEmail))
	if err != nil {
		t.Fatal(err)
	}
	var created accounts.CreatedKey
	err = f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) (err error) {
		created, err = f.svc.CreateKey(t.Context(), tx, audit.System("rate.test"), accounts.KeyRequest{Site: site, Account: account, CreatedBy: user, Name: "test"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/rate-limits/key/" + created.ID.TypeID()
	rec := f.change(session, http.MethodPut, path, `{"requests":1,"reason":"key allowance"}`, newKey())
	expectAdmin(t, "setRateLimit", rec, http.StatusOK, "")
	rec = f.do(http.MethodGet, path, "", as(session))
	expectAdmin(t, "getRateLimit", rec, http.StatusOK, "")
	var view adminapi.RateLimitView
	decode(t, rec, &view)
	if view.Requests == nil || *view.Requests != 1 || view.EffectiveRequests == nil || *view.EffectiveRequests != 1 {
		t.Fatalf("key limit: %s", rec.Body)
	}
}
