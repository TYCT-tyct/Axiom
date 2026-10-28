package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"testing"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
	axmail "axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
)

func passwordConsole(t *testing.T) consoleFixture {
	t.Helper()
	f := newConsoleFixture(t)
	logger := slog.New(slog.DiscardHandler)
	q := f.db.NewQueue(logger, pg.PlatformScope(), nil)
	mailer, err := axmail.Register(t.Context(), f.db, q, axmail.Config{Host: "localhost", Port: 1, From: mail.Address{Address: "no-reply@example.com"}}, logger)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := accounts.RegisterPasswordRecovery(f.db, q, mailer, pg.PlatformScope(), logger)
	if err != nil {
		t.Fatal(err)
	}
	f.run("INSERT INTO river.river_queue (name, paused_at) VALUES ('notifications', now()), ('archive', now()) ON CONFLICT (name) DO UPDATE SET paused_at = now()")
	if err := q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := q.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	svc, err := accounts.NewService(f.db, accounts.Config{
		Platform: pg.PlatformScope(), Hashes: macKeys(t), Keyring: masterKeys(t), Now: f.clock.Now, Recovery: recovery,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Load(t.Context(), logger); err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	f.routes = publicRoutes(consoleAPI{version: "test", accounts: svc, db: f.db}, &guard{contract: loadedContract(t), accounts: svc, logger: logger}, testFrontend(t), logger)
	return f
}

func TestRequestPasswordResetEqualResponse(t *testing.T) {
	t.Parallel()
	f := passwordConsole(t)
	var response string
	for _, email := range []string{ownerEmail, "unknown@example.com"} {
		rec := f.do(http.MethodPost, "/auth/password-reset", `{"email":"`+email+`"}`)
		expect(t, "request reset", rec, http.StatusAccepted, "")
		if rec.Body.String() != response {
			t.Fatalf("different response: %q vs %q", rec.Body.String(), response)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Fatal("request issued a session")
		}
	}
	for range 9 {
		expect(t, "email limit", f.do(http.MethodPost, "/auth/password-reset", `{"email":"`+ownerEmail+`"}`), http.StatusAccepted, "")
	}
	rec := f.do(http.MethodPost, "/auth/password-reset", `{"email":"`+ownerEmail+`"}`)
	expect(t, "email limit", rec, http.StatusTooManyRequests, consoleapi.ErrorCodeRateLimited)
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}

func TestConsoleChangePassword(t *testing.T) {
	t.Parallel()
	f := passwordConsole(t)
	s := f.owner()
	body := `{"current_password":"` + ownerPassword + `","new_password":"a different horse battery","language":"zh"}`
	expect(t, "no session", f.do(http.MethodPost, "/password", body), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	expect(t, "bad csrf", f.do(http.MethodPost, "/password", body, as(s), header("X-CSRF-Token", "wrong")), http.StatusForbidden, consoleapi.ErrorCodeOriginRejected)
	rec := f.do(http.MethodPost, "/password", strings.Replace(body, ownerPassword, "wrong password", 1), as(s))
	expect(t, "wrong password", rec, http.StatusUnauthorized, consoleapi.ErrorCodeInvalidCredentials)
	rec = f.do(http.MethodPost, "/password", body, as(s))
	expect(t, "change password", rec, http.StatusOK, "")
	var result struct {
		CSRF *string `json:"csrf_token"`
	}
	decode(t, rec, &result)
	if result.CSRF == nil || *result.CSRF == "" || *result.CSRF == s.csrf || len(rec.Result().Cookies()) != 1 {
		t.Fatal("missing replacement cookie or CSRF")
	}
	expect(t, "old session", f.do(http.MethodGet, "/session", "", as(s)), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
}

func TestConsoleResetPassword(t *testing.T) {
	t.Parallel()
	f := passwordConsole(t)
	old := f.owner()
	token := strings.Repeat("r", 43)
	f.run(`INSERT INTO accounts.password_reset_requests (site_id,email_hash,id,email,domain,language)
        SELECT site_id,sha256(convert_to(email,'UTF8')),gen_random_uuid(),NULL,'` + consoleHost + `','en' FROM accounts.users WHERE email = '` + ownerEmail + `';
        INSERT INTO accounts.password_reset_tokens (id,site_id,account_id,user_id,token_hash,factor_version,expires_at)
        SELECT r.id,u.site_id,u.account_id,u.id,sha256(convert_to('` + token + `','UTF8')),u.factor_version,r.expires_at
        FROM accounts.users u JOIN accounts.password_reset_requests r ON r.site_id = u.site_id WHERE u.email = '` + ownerEmail + `'`)
	body := `{"token":"` + token + `","new_password":"a different horse battery"}`
	expect(t, "invalid token", f.do(http.MethodPost, "/auth/password-reset/complete", strings.Replace(body, token, strings.Repeat("s", 43), 1)), http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
	rec := f.do(http.MethodPost, "/auth/password-reset/complete", body)
	expect(t, "reset", rec, http.StatusNoContent, "")
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("reset issued a session")
	}
	expect(t, "old session", f.do(http.MethodGet, "/session", "", as(old)), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	expect(t, "replay", f.do(http.MethodPost, "/auth/password-reset/complete", body), http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
}

func TestPasswordResetEmailDisabled(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	for _, email := range []string{ownerEmail, "unknown@example.com"} {
		expect(t, "email unavailable", f.do(http.MethodPost, "/auth/password-reset", `{"email":"`+email+`"}`), http.StatusServiceUnavailable, consoleapi.ErrorCodeUnavailable)
	}
}

func TestConsolePasswordLegacyInvalidEmail(t *testing.T) {
	t.Parallel()
	f := passwordConsole(t)
	s := f.owner()
	f.run("UPDATE accounts.users SET email = 'first,last@x.com' WHERE email = '" + ownerEmail + "'")
	expect(t, "legacy invalid mailbox", f.do(http.MethodPost, "/password", `{"current_password":"`+ownerPassword+`","new_password":"a different horse battery"}`, as(s)), http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
	expect(t, "session unchanged", f.do(http.MethodGet, "/session", "", as(s)), http.StatusOK, "")
}

func TestConsolePasswordReturnsReplacementCodes(t *testing.T) {
	t.Parallel()
	f := passwordConsole(t)
	_, s, _ := f.totpSetup(f.owner())
	rec := f.do(http.MethodPost, "/password", `{"current_password":"`+ownerPassword+`","new_password":"a different horse battery"}`, as(s))
	expect(t, "change with MFA", rec, http.StatusOK, "")
	var result struct {
		Codes *[]string `json:"recovery_codes"`
	}
	decode(t, rec, &result)
	if result.Codes == nil || len(*result.Codes) != 10 {
		t.Fatal("MFA password change did not return replacements")
	}
}
