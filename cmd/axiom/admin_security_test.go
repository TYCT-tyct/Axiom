package main

import (
	"net/http"
	"testing"

	"axiom/internal/platform/adminapi"
)

func TestLoginLimitsAdmin(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	path := "/sites/" + f.siteID() + "/login-limits"
	rec := f.do(http.MethodGet, path, "", as(s))
	expectAdmin(t, "get limits", rec, 200, "")
	var view adminapi.LoginLimitsView
	decode(t, rec, &view)
	if view.Defaults == nil || view.Defaults.EmailSignInFailures == nil || *view.Defaults.EmailSignInFailures != 10 {
		t.Fatalf("defaults %+v", view)
	}
	key := newKey()
	body := `{"limits":{"email_sign_in_failures":1,"ip_attempt_burst":100},"reason":"tighten"}`
	expectAdmin(t, "set limits", f.change(s, http.MethodPut, path, body, key), 200, "")
	replay := f.change(s, http.MethodPut, path, body, key)
	if replay.Header().Get("Idempotency-Replayed") != "true" || f.auditCount("site_login_limits.update") != "1" {
		t.Fatal("not replayed")
	}
	f.expectAudit("site_login_limits.update", map[string]string{"detail->'after'->>'email_sign_in_failures'": "1"})
	expectAdmin(t, "old page", f.change(s, http.MethodPut, "/sites/"+f.siteID()+"/settings", `{"settings":{},"reason":"old page"}`, newKey()), 200, "")
	if got := f.text(`SELECT email_sign_in_failures::text FROM accounts.site_settings`); got != "1" {
		t.Fatal(got)
	}
	for _, field := range []string{"ip_attempt_burst", "ip_attempts_per_minute", "email_sign_in_failures", "user_code_failures", "challenge_code_attempts"} {
		expectAdmin(t, field, f.change(s, http.MethodPut, path, `{"limits":{"`+field+`":0},"reason":"bad"}`, newKey()), 400, codeInvalidRequest)
		expectAdmin(t, field, f.change(s, http.MethodPut, path, `{"limits":{"`+field+`":10001},"reason":"bad"}`, newKey()), 400, codeInvalidRequest)
	}
	// New limits affect the existing login buckets without waiting for a snapshot tick.
	for i, want := range []int{401, 429} {
		rec := f.do(http.MethodPost, "/auth/sign-in", `{"email":"absent@example.com","password":"wrong"}`)
		if rec.Code != want {
			t.Fatalf("attempt %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	expectAdmin(t, "reset", f.change(s, http.MethodPut, path, `{"limits":{},"reason":"default"}`, newKey()), 200, "")
}

func TestAdminAccountUserAndGrantStates(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	email := "operator@example.com"
	f.addUser(email, "support")
	victim := f.verified(email)
	user, account := f.userOf(email), f.accountOf(email)
	for _, suspended := range []string{"true", "false"} {
		expectAdmin(t, "suspension", f.change(owner, http.MethodPut, "/accounts/"+account+"/suspension", `{"suspended":`+suspended+`,"reason":"review"}`, newKey()), 204, "")
		expectAdmin(t, "sign-in while suspended="+suspended, f.do(http.MethodGet, "/users", "", as(victim)), 200, "")
	}
	// Neither suspension state affects this user's existing sign-in.
	expectAdmin(t, "sign-in retained", f.do(http.MethodGet, "/users", "", as(victim)), 200, "")
	path := "/users/" + user + "/sign-in"
	key := newKey()
	body := `{"disabled":true,"reason":"pause"}`
	expectAdmin(t, "disable", f.change(owner, http.MethodPut, path, body, key), 204, "")
	expectAdmin(t, "replay disable", f.change(owner, http.MethodPut, path, body, key), 204, "")
	expectAdmin(t, "revoked session", f.do(http.MethodGet, "/users", "", as(victim)), 401, codeUnauthenticated)
	expectAdmin(t, "restore", f.change(owner, http.MethodPut, path, `{"disabled":false,"reason":"restore"}`, newKey()), 204, "")
	expectAdmin(t, "old session stays revoked", f.do(http.MethodGet, "/users", "", as(victim)), 401, codeUnauthenticated)
	if f.auditCount("user.sign_in_change") != "2" || f.auditCount("account.status_change") != "2" {
		t.Fatal("wrong audit count")
	}
	grant := "/users/" + user + "/platform-grants/support"
	for _, revoked := range []string{"true", "true", "false"} {
		expectAdmin(t, "grant", f.change(owner, http.MethodPut, grant, `{"revoked":`+revoked+`,"reason":"review"}`, newKey()), 204, "")
	}
	if f.auditCount("platform_grant.status_change") != "2" {
		t.Fatal("duplicate grant audit")
	}
	rec := f.do(http.MethodGet, "/users/"+user+"/platform-grants", "", as(owner))
	expectAdmin(t, "grants", rec, 200, "")
	var list adminapi.PlatformGrantList
	decode(t, rec, &list)
	if list.Data == nil || len(*list.Data) != 1 || *(*list.Data)[0].Revoked {
		t.Fatal("grant not restored")
	}
	expectAdmin(t, "restore nonexistent grant", f.change(owner, http.MethodPut, "/users/"+user+"/platform-grants/admin", `{"revoked":false,"reason":"no history"}`, newKey()), 404, codeNotFound)
	expectAdmin(t, "unprivileged mutation", f.change(victim, http.MethodPut, path, body, newKey()), 401, codeUnauthenticated)
}

func TestGrantRestoreCannotEscalate(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	f.addUser("limited@example.com", "support")
	f.addUser("target@example.com", "admin")
	f.run(`UPDATE accounts.roles SET permissions=array_append(permissions,'platform_grants.manage') WHERE name='support'`)
	if err := f.svc.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	limited := f.verified("limited@example.com")
	targetSession := f.verified("target@example.com")
	target := f.userOf("target@example.com")
	path := "/users/" + target + "/platform-grants/admin"
	expectAdmin(t, "target authorized", f.do(http.MethodGet, "/users", "", as(targetSession)), 200, "")
	// A limited grant manager can remove a dangerous grant but cannot recreate it.
	expectAdmin(t, "revoke", f.change(limited, http.MethodPut, path, `{"revoked":true,"reason":"contain"}`, newKey()), 204, "")
	if rec := f.do(http.MethodGet, "/users", "", as(targetSession)); rec.Code == 200 {
		t.Fatal("revoked grant still authorized")
	}
	expectAdmin(t, "deny escalation", f.change(limited, http.MethodPut, path, `{"revoked":false,"reason":"escalate"}`, newKey()), 403, codeForbidden)
	if f.auditCount("platform_grant.status_change") != "1" {
		t.Fatal("failed restore wrote audit")
	}
	expectAdmin(t, "owner restores", f.change(owner, http.MethodPut, path, `{"revoked":false,"reason":"restore"}`, newKey()), 204, "")
	expectAdmin(t, "target restored", f.do(http.MethodGet, "/users", "", as(targetSession)), 200, "")
	if got := f.text(`SELECT count(*)::text FROM accounts.platform_grants g JOIN accounts.users u ON u.id=g.user_id WHERE u.email='target@example.com'`); got != "2" {
		t.Fatal("restore rewrote terminal grant", got)
	}
}
