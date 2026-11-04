package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
)

// readSession reads getSession with the session.
func (f adminFixture) readSession(s session) adminapi.Session {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/session", "", as(s))
	expectAdmin(f.t, "getSession", rec, http.StatusOK, "")
	var got adminapi.Session
	decode(f.t, rec, &got)
	return got
}

// TestAdminSignInSetsTheCookie: signing in on the admin domain sets the session cookie, which a page cannot read and
// only HTTPS carries, and the session says who it is.
func TestAdminSignInSetsTheCookie(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	rec := f.do(http.MethodPost, "/auth/sign-in", `{"email":"Owner@Example.com","password":"`+ownerPassword+`"}`)
	expectAdmin(t, "sign-in", rec, http.StatusOK, "")
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Name != "__Host-axiom_session" {
		t.Fatalf("the session cookie: %+v", cookies)
	}
	s := f.sessionOf(rec)
	got := f.readSession(s)
	if got.Level != "password" || got.MfaEnabled || got.CsrfToken != s.csrf || !got.ExpiresAt.After(time.Now()) ||
		!strings.HasPrefix(got.User.Id, "user_") || !strings.HasPrefix(got.Account, "acct_") || got.User.Email != ownerEmail {
		t.Errorf("the password session: %+v", got)
	}
}

// TestAdminSessionPermissions: getSession tells what the session may do on the platform, which is the permissions of
// the user's platform role, and of those only the ones the session's level allows.
func TestAdminSessionPermissions(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	wantPassword := []string{
		"api_keys.manage", "api_keys.view", "balance.top_up", "balance.view", "calls.view", "catalog.view", "pricing.view", "suppliers.view",
		"users.view", "workspaces.manage",
	}
	if diff := cmp.Diff(wantPassword, f.readSession(f.signIn(ownerEmail)).Permissions); diff != "" {
		t.Errorf("the password session's permissions (-want +got):\n%s", diff)
	}
	wantAll := []string{
		"api_keys.manage", "api_keys.view", "audit.view", "balance.top_up", "balance.view", "calls.inspect", "calls.search", "calls.view", "catalog.manage", "catalog.view", "credits.grant",
		"operations.approve", "operations.manage", "platform_grants.manage", "pricing.manage", "pricing.view", "sites.manage", "suppliers.manage", "suppliers.view",
		"users.manage", "users.view", "workspaces.manage",
	}
	got := f.readSession(f.verified(ownerEmail))
	if diff := cmp.Diff(wantAll, got.Permissions); got.Level != "mfa" || !got.MfaEnabled || diff != "" {
		t.Errorf("the verified session: level %q, mfa %v, permissions (-want +got):\n%s", got.Level, got.MfaEnabled, diff)
	}
}

// TestAdminSignOut: signing out expires the cookie and ends the session.
func TestAdminSignOut(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	rec := f.do(http.MethodPost, "/auth/sign-out", "", as(s))
	expectAdmin(t, "signOut", rec, http.StatusNoContent, "")
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 || c[0].Value != "" {
		t.Errorf("signing out left the cookie %+v", c)
	}
	expectAdmin(t, "getSession after signing out", f.do(http.MethodGet, "/session", "", as(s)), http.StatusUnauthorized, codeUnauthenticated)
}

// TestAdminSessionOfAUserWithoutAGrant: anyone with an account on the main site may sign in at the admin domain, and
// the session tells them they may do nothing there.
func TestAdminSessionOfAUserWithoutAGrant(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	f.addUser("nobody@example.com", "")
	s := f.verified("nobody@example.com")
	rec := f.do(http.MethodGet, "/session", "", as(s))
	expectAdmin(t, "getSession", rec, http.StatusOK, "")
	var got adminapi.Session
	decode(t, rec, &got)
	if len(got.Permissions) != 0 {
		t.Errorf("a user with no platform grant holds %v", got.Permissions)
	}
}

// TestAdminTwoStepVerification: the second factor is the same as the console's, on the admin's own sessions: a sign-in
// with it waits for a code, a pending session may read itself and nothing else, and a TOTP code passes it.
func TestAdminTwoStepVerification(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.signIn(ownerEmail)
	rec := f.do(http.MethodPost, "/auth/totp/setup", "", as(s))
	var setup adminapi.TotpSetup
	decode(t, rec, &setup)
	if !strings.HasPrefix(setup.Uri, "otpauth://totp/") || !strings.Contains(setup.Uri, setup.Secret) {
		t.Fatalf("setup: %+v", setup)
	}
	confirm := `{"secret":"` + setup.Secret + `","password":"` + ownerPassword + `","code":"` + totpCode(t, setup.Secret, f.clock.Now()) + `"}`
	rec = f.do(http.MethodPost, "/auth/totp/confirm", confirm, as(s))
	expectAdmin(t, "confirmTotpSetup", rec, http.StatusOK, "")
	var changed adminapi.FactorChange
	decode(t, rec, &changed)
	if len(changed.RecoveryCodes) != 10 {
		t.Fatalf("%d recovery codes", len(changed.RecoveryCodes))
	}
	expectAdmin(t, "the session from before the change", f.do(http.MethodGet, "/session", "", as(s)), http.StatusUnauthorized, codeUnauthenticated)

	rec = f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+ownerEmail+`","password":"`+ownerPassword+`"}`)
	var grant adminapi.SessionGrant
	decode(t, rec, &grant)
	if grant.Status != adminapi.SessionGrantStatusMfaRequired {
		t.Fatalf("sign-in with two-step verification: %+v", grant)
	}
	pending := f.sessionOf(rec)
	var info adminapi.Session
	decode(t, f.do(http.MethodGet, "/session", "", as(pending)), &info)
	if info.Level != "pending_mfa" || len(info.Permissions) != 0 {
		t.Errorf("the pending session: %+v", info)
	}
	expectAdmin(t, "a pending session listing users", f.do(http.MethodGet, "/users", "", as(pending)), http.StatusUnauthorized, codeUnauthenticated)
	expectAdmin(t, "a wrong code", f.do(http.MethodPost, "/auth/mfa", `{"code":"abcd-abcd-abcd-abcd"}`, as(pending)), http.StatusUnauthorized, codeInvalidCode)

	f.clock.Advance(time.Minute)
	code := `{"code":"` + totpCode(t, setup.Secret, f.clock.Now()) + `"}`
	rec = f.do(http.MethodPost, "/auth/mfa", code, as(pending))
	expectAdmin(t, "a TOTP code", rec, http.StatusOK, "")
	passed := f.sessionOf(rec)
	expectAdmin(t, "the platform with the new session", f.do(http.MethodGet, "/users", "", as(passed)), http.StatusOK, "")
	expectAdmin(t, "the code on the pending session again", f.do(http.MethodPost, "/auth/mfa", code, as(pending)), http.StatusUnauthorized, "")
}

// TestSessionsBelongToTheirSurface: the cookie of a console session opens nothing on the admin and the admin's opens
// nothing on the console, though both are the same user's.
func TestSessionsBelongToTheirSurface(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	admin := f.owner()
	logger := slog.New(slog.DiscardHandler)
	contract, err := consoleContract()
	if err != nil {
		t.Fatal(err)
	}
	console := publicRoutes(consoleAPI{version: "test", accounts: f.svc, db: f.db}, &guard{contract: contract, accounts: f.svc, logger: logger}, testFrontend(f.t), logger)

	signIn := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+consoleHost+"/api/console/v1/auth/sign-in",
		strings.NewReader(`{"email":"`+ownerEmail+`","password":"`+ownerPassword+`"}`))
	signIn.RemoteAddr = "192.0.2.1:4000"
	signIn.Header.Set("Origin", "https://"+consoleHost)
	signIn.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	console.ServeHTTP(rec, signIn)
	if rec.Code != http.StatusOK {
		t.Fatalf("console sign-in: %d %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("console sign-in set %d cookies", len(cookies))
	}
	expectAdmin(t, "a console session at the admin", f.do(http.MethodGet, "/session", "", func(r *http.Request) { r.AddCookie(cookies[0]) }),
		http.StatusUnauthorized, codeUnauthenticated)

	read := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+consoleHost+"/api/console/v1/session", nil)
	read.AddCookie(admin.cookie)
	rec = httptest.NewRecorder()
	console.ServeHTTP(rec, read)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("an admin session at the console: %d %s", rec.Code, rec.Body)
	}
}

// TestAdminSessionsEnd: a session past its end, or idle for longer than the site allows, is no session.
func TestAdminSessionsEnd(t *testing.T) {
	t.Parallel()
	for name, statement := range map[string]string{
		"expired": `UPDATE accounts.sessions SET expires_at = now() - interval '1 minute'`,
		"idle":    `UPDATE accounts.sessions SET last_seen_at = now() - interval '13 hours'`,
		"revoked": `UPDATE accounts.sessions SET revoked_at = now()`,
	} {
		f := newAdminFixture(t)
		s := f.owner()
		expectAdmin(t, "before", f.do(http.MethodGet, "/session", "", as(s)), http.StatusOK, "")
		f.run(statement)
		expectAdmin(t, name+" session", f.do(http.MethodGet, "/session", "", as(s)), http.StatusUnauthorized, codeUnauthenticated)
	}
}

// TestAdminReadsMemberNamesExactly: the guard checks a body by the contract's exact names, so the handler must read it
// by them too: a member named "EMAIL" beside "email" is one nobody asked for, not a second email that wins.
func TestAdminReadsMemberNamesExactly(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	body := `{"email":"` + ownerEmail + `","EMAIL":"nobody@example.com","password":"` + ownerPassword + `"}`
	expectAdmin(t, "sign-in with a member named in capitals", f.do(http.MethodPost, "/auth/sign-in", body), http.StatusOK, "")
}
