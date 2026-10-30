package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pgtest"
)

// TestOpenRegistrationHTTP: an open site accepts a registration with 202, the worker puts a link in the outbox, the
// link verifies with 204, and the new user can sign in. The address is not signed in by the registration itself.
func TestOpenRegistrationHTTP(t *testing.T) {
	t.Parallel()
	c, _ := ownerFixture.get(t, buildOwnerDatabase)
	if err := pgtest.Run(c.t.Context(), c.database.Superuser,
		`UPDATE accounts.sites SET registration = 'open' WHERE is_main`,
		`INSERT INTO accounts.site_settings (site_id, registration_human_check, registration_domain_burst, registration_domain_per_hour)
		 SELECT id, false, 100, 1000 FROM accounts.sites WHERE is_main`); err != nil {
		t.Fatal(err)
	}
	f := startNodeWithRegistration(c, &testClock{now: time.Now()})
	if err := f.queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := f.queue.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	const email, chosen = "open@example.com", "a first horse battery"
	// A password supplied before the address is proved is ignored: the click chooses the real one.
	rec := f.do(http.MethodPost, "/auth/register", `{"email":"`+email+`","password":"an attacker chosen password","language":"en"}`)
	expect(t, "register", rec, http.StatusAccepted, "")
	var pending consoleapi.RegistrationPending
	decode(t, rec, &pending)
	if pending.Status != consoleapi.VerificationSent {
		t.Fatalf("status %q", pending.Status)
	}
	body := ""
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(body, "#token=") && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		body = f.text(`SELECT coalesce((SELECT text_body FROM platform.mail_outbox
			WHERE recipient = '<` + email + `>' ORDER BY created_at DESC LIMIT 1), '')`)
	}
	const prefix = "https://" + consoleHost + "/console/verify-email#token="
	if !strings.Contains(body, prefix) {
		t.Fatalf("the verification link does not open the console page: %q", body)
	}
	token := strings.TrimSpace(body[strings.LastIndex(body, "#token=")+len("#token="):])
	if token == "" {
		t.Fatal("no verification link in the outbox")
	}
	// A document published while the link is pending is accepted on the verification page, with the new user.
	if err := pgtest.Run(t.Context(), c.database.Superuser, `INSERT INTO accounts.legal_documents (site_id, kind, version, body)
		SELECT id, 'terms', 1, 'Placeholder terms of service for tests: this is not a real agreement.' FROM accounts.sites WHERE is_main`); err != nil {
		t.Fatal(err)
	}
	rec = f.do(http.MethodPost, "/auth/verify-email", `{"token":"`+token+`","password":"`+chosen+`"}`)
	expect(t, "verifyEmail without consent", rec, http.StatusForbidden, consoleapi.ErrorCodeConsentRequired)
	rec = f.do(http.MethodPost, "/auth/verify-email", `{"token":"`+token+`","password":"`+chosen+`","consent":[{"kind":"terms","version":1}]}`)
	expect(t, "verifyEmail", rec, http.StatusNoContent, "")
	f.signIn(email, chosen)
	if got := f.text(`SELECT count(*)::text FROM accounts.users WHERE email = '` + email + `' AND email_verified_at IS NOT NULL`); got != "1" {
		t.Fatalf("verified users %s, want 1", got)
	}
	if got := f.text(`SELECT a.kind || ' ' || a.version FROM accounts.legal_acceptances a
		JOIN accounts.users u ON u.id = a.user_id WHERE u.email = '` + email + `'`); got != "terms 1" {
		t.Errorf("the verification's acceptance: %q", got)
	}
	rec = f.do(http.MethodPost, "/auth/verify-email", `{"token":"`+token+`","password":"`+chosen+`"}`)
	expect(t, "verifyEmail again", rec, http.StatusUnprocessableEntity, consoleapi.ErrorCode(codeInvalidVerification))
}
