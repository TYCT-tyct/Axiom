package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pgtest"
)

// TestSessionCookie: signing in sets a session cookie a page cannot read and that only HTTPS carries.
func TestSessionCookie(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	rec := f.do(http.MethodPost, "/auth/sign-in", `{"email":"Owner@Example.com","password":"`+ownerPassword+`"}`)
	expect(t, "sign-in", rec, http.StatusOK, "")
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("%d cookies, want 1", len(cookies))
	}
	type attributes struct {
		Name, Path, Domain string
		HTTPOnly, Secure   bool
		SameSite           http.SameSite
	}
	c := cookies[0]
	got := attributes{c.Name, c.Path, c.Domain, c.HttpOnly, c.Secure, c.SameSite}
	want := attributes{Name: "__Host-axiom_session", Path: "/", HTTPOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("the session cookie (-want +got):\n%s", diff)
	}
}

// TestSessionLifecycle: getSession tells the page who it is and what it may do, and signing out expires the cookie and
// ends the session.
func TestSessionLifecycle(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	rec := f.do(http.MethodGet, "/session", "", as(s))
	expect(t, "getSession", rec, http.StatusOK, "")
	var got consoleapi.Session
	decode(t, rec, &got)
	want := consoleapi.Session{
		Level: "password", CsrfToken: s.csrf, ExpiresAt: got.ExpiresAt, Account: got.Account,
		Permissions: []string{"api_keys.manage", "api_keys.view", "balance.top_up", "balance.view", "calls.view", "workspaces.manage"},
	}
	want.User.Email, want.User.Id, want.User.InviteCode = ownerEmail, got.User.Id, got.User.InviteCode
	if diff := cmp.Diff(want, got); diff != "" || !got.ExpiresAt.After(time.Now()) {
		t.Errorf("session (-want +got):\n%s", diff)
	}
	if !strings.HasPrefix(got.User.Id, "user_") || !strings.HasPrefix(got.Account, "acct_") || len(got.User.InviteCode) < 6 {
		t.Errorf("IDs and invite code: %+v", got)
	}

	rec = f.do(http.MethodPost, "/auth/sign-out", "", as(s))
	expect(t, "signOut", rec, http.StatusNoContent, "")
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 || c[0].Value != "" {
		t.Errorf("signing out left the cookie %+v", c)
	}
	expect(t, "getSession after signing out", f.do(http.MethodGet, "/session", "", as(s)), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
}

// TestSignInRefusals: the answer does not tell a wrong password from an unknown email, and the limit on failures per
// email is a 429 that says how long to wait.
func TestSignInRefusals(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	for _, email := range []string{ownerEmail, "nobody@example.com"} {
		rec := f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+email+`","password":"not the password"}`)
		expect(t, "sign-in as "+email, rec, http.StatusUnauthorized, consoleapi.ErrorCodeInvalidCredentials)
	}
	var last *httptest.ResponseRecorder
	for range 12 {
		last = f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+ownerEmail+`","password":"not the password"}`)
	}
	expect(t, "the failures past the limit", last, http.StatusTooManyRequests, consoleapi.ErrorCodeRateLimited)
	if seconds, err := strconv.Atoi(last.Header().Get("Retry-After")); err != nil || seconds < 1 {
		t.Errorf("Retry-After %q, want whole seconds of at least 1", last.Header().Get("Retry-After"))
	}
	if strings.Contains(last.Body.String(), ownerEmail) {
		t.Errorf("the refusal repeats the email: %s", last.Body)
	}
}

// TestRegistration: a user registers with another's invite code, gets a balance row at once, signs in; the code,
// the address and the password are each refused as the contract says.
func TestRegistration(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	owner := f.owner()
	var info consoleapi.Session
	decode(t, f.do(http.MethodGet, "/session", "", as(owner)), &info)
	register := func(code, email, password string) *httptest.ResponseRecorder {
		return f.do(http.MethodPost, "/auth/register", `{"invite_code":"`+code+`","email":"`+email+`","password":"`+password+`"}`)
	}
	code := info.User.InviteCode
	rec := register(code, "New@Example.com", "another horse battery")
	expect(t, "register", rec, http.StatusCreated, "")
	var registered consoleapi.Registered
	decode(t, rec, &registered)
	if !strings.HasPrefix(registered.UserId, "user_") {
		t.Errorf("registered %+v", registered)
	}
	newcomer := f.signIn("new@example.com", "another horse battery")
	balance := f.do(http.MethodGet, "/balance", "", as(newcomer))
	var money consoleapi.Balance
	decode(t, balance, &money)
	if balance.Code != http.StatusOK || money.Balance.Amount != "0" || money.Balance.Currency != "USD" || money.Available.Amount != "0" {
		t.Errorf("the new account's balance: %d %s", balance.Code, balance.Body)
	}
	for _, c := range []struct {
		name, code, email, password string
		status                      int
		want                        consoleapi.ErrorCode
	}{
		{"a taken address in another case", code, "NEW@example.com", "another horse battery", http.StatusConflict, consoleapi.ErrorCodeEmailTaken},
		{"an unknown invite code", "nosuchcode", "x@example.com", "another horse battery", http.StatusUnprocessableEntity, consoleapi.ErrorCodeInvalidInvite},
		{"an address without an @", code, "not-an-address", "another horse battery", http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable},
		{"a short password", code, "y@example.com", "short", http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest},
	} {
		expect(t, c.name, register(c.code, c.email, c.password), c.status, c.want)
	}
}

// totpSetup turns on two-step verification for the session's user and returns the secret, the session that replaces
// it and the recovery codes.
func (f consoleFixture) totpSetup(s session) (string, session, []string) {
	f.t.Helper()
	rec := f.do(http.MethodPost, "/auth/totp/setup", "", as(s))
	expect(f.t, "beginTotpSetup", rec, http.StatusOK, "")
	var setup consoleapi.TotpSetup
	decode(f.t, rec, &setup)
	if !strings.HasPrefix(setup.Uri, "otpauth://totp/") || !strings.Contains(setup.Uri, setup.Secret) {
		f.t.Fatalf("setup: %+v", setup)
	}
	confirm := `{"secret":"` + setup.Secret + `","password":"` + ownerPassword + `","code":"` + totpCode(f.t, setup.Secret, f.clock.Now()) + `"}`
	rec = f.do(http.MethodPost, "/auth/totp/confirm", confirm, as(s))
	expect(f.t, "confirmTotpSetup", rec, http.StatusOK, "")
	var changed consoleapi.FactorChange
	decode(f.t, rec, &changed)
	if len(changed.RecoveryCodes) != 10 {
		f.t.Fatalf("%d recovery codes", len(changed.RecoveryCodes))
	}
	return setup.Secret, session{rec.Result().Cookies()[0], changed.CsrfToken}, changed.RecoveryCodes
}

// TestTwoStepVerification: after the factor is confirmed the old session is gone and the new one holds all the
// permissions; the next sign-in waits for a code, and a pending session may read itself and nothing else; a recovery
// code passes it once; recovery codes can be made again only by a session that passed verification, with a fresh code.
func TestTwoStepVerification(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	old := f.owner()
	secret, verified, codes := f.totpSetup(old)
	expect(t, "the session from before the change", f.do(http.MethodGet, "/session", "", as(old)), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)

	rec := f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+ownerEmail+`","password":"`+ownerPassword+`"}`)
	var grant consoleapi.SessionGrant
	decode(t, rec, &grant)
	if grant.Status != consoleapi.SessionGrantStatusMfaRequired {
		t.Fatalf("sign-in with two-step verification: %+v", grant)
	}
	pending := f.sessionOf(rec)
	var info consoleapi.Session
	decode(t, f.do(http.MethodGet, "/session", "", as(pending)), &info)
	if info.Level != "pending_mfa" || len(info.Permissions) != 0 {
		t.Errorf("the pending session: %+v", info)
	}
	expect(t, "a pending session listing keys", f.do(http.MethodGet, "/keys", "", as(pending)), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	expect(t, "a wrong code", f.do(http.MethodPost, "/auth/mfa", `{"code":"abcd-abcd-abcd-abcd"}`, as(pending)), http.StatusUnauthorized, consoleapi.ErrorCodeInvalidCode)

	rec = f.do(http.MethodPost, "/auth/mfa", `{"code":"`+codes[0]+`"}`, as(pending))
	expect(t, "a recovery code", rec, http.StatusOK, "")
	passed := f.sessionOf(rec)
	expect(t, "the recovery code again", f.do(http.MethodPost, "/auth/mfa", `{"code":"`+codes[0]+`"}`, as(pending)), http.StatusUnauthorized, "")

	f.clock.Advance(time.Minute)
	code := `{"code":"` + totpCode(t, secret, f.clock.Now()) + `"}`
	rec = f.do(http.MethodPost, "/auth/recovery-codes", code, as(passed))
	expect(t, "regenerating recovery codes", rec, http.StatusOK, "")
	expect(t, "the session that asked, replaced", f.do(http.MethodGet, "/session", "", as(passed)), http.StatusUnauthorized, "")
	expect(t, "the session from the confirmation, replaced", f.do(http.MethodGet, "/session", "", as(verified)), http.StatusUnauthorized, "")
}

// TestTwoStepVerificationStaysOn: once it is on, confirming another secret is a 409, as the contract says, and changes
// nothing: the first secret and the recovery codes still work, and the session that asked is still the user's.
func TestTwoStepVerificationStaysOn(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	secret, verified, codes := f.totpSetup(f.owner())
	rec := f.do(http.MethodPost, "/auth/totp/setup", "", as(verified))
	expect(t, "beginTotpSetup", rec, http.StatusOK, "")
	var other consoleapi.TotpSetup
	decode(t, rec, &other)
	f.clock.Advance(time.Minute)
	confirm := `{"secret":"` + other.Secret + `","password":"` + ownerPassword + `","code":"` + totpCode(t, other.Secret, f.clock.Now()) + `"}`
	expect(t, "confirming a second secret", f.do(http.MethodPost, "/auth/totp/confirm", confirm, as(verified)), http.StatusConflict, consoleapi.ErrorCodeConflict)
	expect(t, "the session that asked", f.do(http.MethodGet, "/session", "", as(verified)), http.StatusOK, "")

	rec = f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+ownerEmail+`","password":"`+ownerPassword+`"}`)
	pending := f.sessionOf(rec)
	expect(t, "a code of the first secret", f.do(http.MethodPost, "/auth/mfa", `{"code":"`+totpCode(t, secret, f.clock.Now())+`"}`, as(pending)), http.StatusOK, "")
	rec = f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+ownerEmail+`","password":"`+ownerPassword+`"}`)
	expect(t, "a recovery code from before", f.do(http.MethodPost, "/auth/mfa", `{"code":"`+codes[0]+`"}`, as(f.sessionOf(rec))), http.StatusOK, "")
}

// sessions lists the signed-in devices as s.
func (f consoleFixture) sessions(s session) []consoleapi.SessionDevice {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/sessions", "", as(s))
	expect(f.t, "listSessions", rec, http.StatusOK, "")
	var list consoleapi.SessionList
	decode(f.t, rec, &list)
	if list.Sessions == nil {
		f.t.Fatal("the list has no sessions member")
	}
	return *list.Sessions
}

// sessionView is what the list decides about a device; presence of the other fields is checked on the way.
type sessionView struct {
	Current bool
	Client  string
	IPGroup string
}

func sessionViews(t *testing.T, devices []consoleapi.SessionDevice) []sessionView {
	t.Helper()
	views := make([]sessionView, 0, len(devices))
	for _, device := range devices {
		if device.Id == nil || *device.Id == "" || device.Current == nil || device.CreatedAt == nil || device.LastSeenAt == nil ||
			device.Client == nil || device.IpGroup == nil {
			t.Fatalf("a device is missing a field: %+v", device)
		}
		views = append(views, sessionView{Current: *device.Current, Client: *device.Client, IPGroup: *device.IpGroup})
	}
	return views
}

// TestSessionDevices: the list shows this user's live console sessions newest first with the grouped address and a
// client summary, and marks the asking one.
func TestSessionDevices(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	chrome := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	f.signIn(ownerEmail, ownerPassword, header("User-Agent", chrome))
	second := f.signIn(ownerEmail, ownerPassword, header("User-Agent", "curl/8.5.0"))
	devices := f.sessions(second)
	if len(devices) != 2 {
		t.Fatalf("%d devices, want the two console sessions: %+v", len(devices), devices)
	}
	want := []sessionView{{Current: true, Client: "curl", IPGroup: "192.0.2.1"}, {Client: "Chrome on Windows", IPGroup: "192.0.2.1"}}
	if diff := cmp.Diff(want, sessionViews(t, devices)); diff != "" {
		t.Errorf("devices (-want +got):\n%s", diff)
	}
}

// TestRevokeSession: the owner can end another session by ID; the asking session and a malformed ID are 404s and keep
// the asking session signed in.
func TestRevokeSession(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	first := f.signIn(ownerEmail, ownerPassword)
	second := f.signIn(ownerEmail, ownerPassword)
	devices := f.sessions(second)
	var other, current string
	for _, device := range devices {
		if device.Current != nil && !*device.Current && device.Id != nil {
			other = *device.Id
		}
		if device.Current != nil && *device.Current && device.Id != nil {
			current = *device.Id
		}
	}
	if other == "" || current == "" {
		t.Fatalf("the listed devices have no other and current IDs: %+v", devices)
	}
	expect(t, "revokeSession", f.do(http.MethodDelete, "/sessions/"+other, "", as(second)), http.StatusNoContent, "")
	expect(t, "the ended session", f.do(http.MethodGet, "/session", "", as(first)), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	expect(t, "the asking session", f.do(http.MethodGet, "/session", "", as(second)), http.StatusOK, "")
	if len(f.sessions(second)) != 1 {
		t.Error("the ended session is still listed")
	}
	expect(t, "ending the asking session", f.do(http.MethodDelete, "/sessions/"+current, "", as(second)), http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	expect(t, "a malformed ID", f.do(http.MethodDelete, "/sessions/nope", "", as(second)), http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	expect(t, "the asking session after the refusals", f.do(http.MethodGet, "/session", "", as(second)), http.StatusOK, "")
}

// TestRevokeOtherSessions: one call ends the other console sessions and leaves the asking one; a second call has
// nothing to do.
func TestRevokeOtherSessions(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	current := f.owner()
	first := f.signIn(ownerEmail, ownerPassword)
	second := f.signIn(ownerEmail, ownerPassword)
	rec := f.do(http.MethodDelete, "/sessions", "", as(current))
	expect(t, "revokeOtherSessions", rec, http.StatusOK, "")
	var ended consoleapi.SessionRevocation
	decode(t, rec, &ended)
	if ended.Revoked == nil || *ended.Revoked != 2 {
		t.Errorf("revoked: %+v, want 2", ended.Revoked)
	}
	expect(t, "the asking session", f.do(http.MethodGet, "/session", "", as(current)), http.StatusOK, "")
	expect(t, "the first other session", f.do(http.MethodGet, "/session", "", as(first)), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	expect(t, "the second other session", f.do(http.MethodGet, "/session", "", as(second)), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	rec = f.do(http.MethodDelete, "/sessions", "", as(current))
	expect(t, "revokeOtherSessions again", rec, http.StatusOK, "")
	decode(t, rec, &ended)
	if ended.Revoked == nil || *ended.Revoked != 0 {
		t.Errorf("revoked on the second call: %+v, want 0", ended.Revoked)
	}
}

// TestReplaceTotp: a session that passed two-step verification replaces the secret with a current code and a code from
// the new secret; the old secret, old recovery codes and old session stop working and the response's session works.
func TestReplaceTotp(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	old := f.owner()
	oldSecret, verified, codes := f.totpSetup(old)
	rec := f.do(http.MethodPost, "/auth/totp/setup", "", as(verified))
	expect(t, "beginTotpSetup", rec, http.StatusOK, "")
	var setup consoleapi.TotpSetup
	decode(t, rec, &setup)
	f.clock.Advance(30 * time.Second)
	body := `{"secret":"` + setup.Secret + `","current_code":"` + totpCode(t, oldSecret, f.clock.Now()) + `","code":"` + totpCode(t, setup.Secret, f.clock.Now()) + `"}`
	rec = f.do(http.MethodPost, "/auth/totp/replace", body, as(verified))
	expect(t, "replaceTotp", rec, http.StatusOK, "")
	var changed consoleapi.FactorChange
	decode(t, rec, &changed)
	if len(changed.RecoveryCodes) != 10 || changed.CsrfToken == "" {
		t.Fatalf("factor change: %+v", changed)
	}
	replaced := session{rec.Result().Cookies()[0], changed.CsrfToken}
	expect(t, "the session that replaced the factor", f.do(http.MethodGet, "/session", "", as(verified)), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	expect(t, "the session the response issued", f.do(http.MethodGet, "/session", "", as(replaced)), http.StatusOK, "")

	rec = f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+ownerEmail+`","password":"`+ownerPassword+`"}`)
	pending := f.sessionOf(rec)
	expect(t, "a code of the old secret", f.do(http.MethodPost, "/auth/mfa", `{"code":"`+totpCode(t, oldSecret, f.clock.Now())+`"}`, as(pending)), http.StatusUnauthorized, consoleapi.ErrorCodeInvalidCode)
	rec = f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+ownerEmail+`","password":"`+ownerPassword+`"}`)
	expect(t, "an old recovery code", f.do(http.MethodPost, "/auth/mfa", `{"code":"`+codes[0]+`"}`, as(f.sessionOf(rec))), http.StatusUnauthorized, consoleapi.ErrorCodeInvalidCode)
	f.clock.Advance(30 * time.Second)
	rec = f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+ownerEmail+`","password":"`+ownerPassword+`"}`)
	expect(t, "a code of the new secret", f.do(http.MethodPost, "/auth/mfa", `{"code":"`+totpCode(t, setup.Secret, f.clock.Now())+`"}`, as(f.sessionOf(rec))), http.StatusOK, "")

	other := newConsoleFixture(t)
	passwordLevel := other.owner()
	expect(t, "replacing with a password session", other.do(http.MethodPost, "/auth/totp/replace", body, as(passwordLevel)), http.StatusForbidden, consoleapi.ErrorCodeMfaRequired)
}

// TestLevelsAreEnforced: an operation that needs two-step verification refuses a session that proved only the
// password, with the code that says why, before it looks at the body.
func TestLevelsAreEnforced(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	rec := f.do(http.MethodPost, "/auth/recovery-codes", `{"code":"123456"}`, as(f.owner()))
	expect(t, "regenerating with a password session", rec, http.StatusForbidden, consoleapi.ErrorCodeMfaRequired)
}

// TestReplaceTotpInternalError: a stored secret that cannot be opened is an internal error, not an invalid code, and
// the session that tried stays signed in.
func TestReplaceTotpInternalError(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	old := f.owner()
	oldSecret, verified, _ := f.totpSetup(old)
	if err := pgtest.Run(t.Context(), f.database.Superuser,
		"UPDATE accounts.users SET totp_ciphertext = decode('00', 'hex') WHERE email = '"+ownerEmail+"'"); err != nil {
		t.Fatal(err)
	}
	rec := f.do(http.MethodPost, "/auth/totp/setup", "", as(verified))
	expect(t, "beginTotpSetup", rec, http.StatusOK, "")
	var setup consoleapi.TotpSetup
	decode(t, rec, &setup)
	f.clock.Advance(30 * time.Second)
	body := `{"secret":"` + setup.Secret + `","current_code":"` + totpCode(t, oldSecret, f.clock.Now()) + `","code":"` + totpCode(t, setup.Secret, f.clock.Now()) + `"}`
	expect(t, "replaceTotp with an unreadable secret", f.do(http.MethodPost, "/auth/totp/replace", body, as(verified)), http.StatusInternalServerError, consoleapi.ErrorCodeInternal)
	expect(t, "the session after the internal error", f.do(http.MethodGet, "/session", "", as(verified)), http.StatusOK, "")
}
