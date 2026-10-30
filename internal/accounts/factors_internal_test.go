package accounts_test

import (
	"errors"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/pgtest"
)

// TestReplaceTOTPInternalErrorIsNotACode: a database failure while spending the current code is an internal error, not
// a bad code: it must not spend the user's failure budget or write a bad_code security event, and the retry must work.
func TestReplaceTOTPInternalErrorIsNotACode(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p0 := mustPrincipal(t, x, console, x.signIn(t, console))
	key, _ := x.s.BeginTOTP(p0)
	mfa, _, err := x.s.ConfirmTOTP(t.Context(), p0, accounts.FactorChange{
		Secret: key, Password: password, Code: accounts.TOTPCode(key, x.clock.Now()),
	})
	if err != nil {
		t.Fatal(err)
	}
	p := mustPrincipal(t, x, console, mfa)
	if err := pgtest.Run(t.Context(), x.database.Superuser, `INSERT INTO accounts.site_settings (site_id, user_code_failures)
		SELECT site_id, 1 FROM accounts.users WHERE id = '`+uuidString(x.user)+`'
		ON CONFLICT (site_id) DO UPDATE SET user_code_failures = 1`,
		`CREATE FUNCTION accounts.reject_totp_step() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'injected failure'; END $$`,
		`CREATE TRIGGER reject_totp_step BEFORE UPDATE OF totp_last_step ON accounts.users
			FOR EACH ROW EXECUTE FUNCTION accounts.reject_totp_step()`); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	secret, _ := x.s.BeginTOTP(p)
	x.advance(t)
	request := accounts.FactorChange{
		Secret: secret, CurrentCode: accounts.TOTPCode(key, x.clock.Now()), Code: accounts.TOTPCode(secret, x.clock.Now()),
	}
	if _, _, err := x.s.ReplaceTOTP(t.Context(), p, request); err == nil || errors.Is(err, accounts.ErrBadCode) {
		t.Fatalf("the injected database failure: %v, want an internal error", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action IN ('security.factor_failed', 'user.totp_replace')"); got[0] != "0" {
		t.Errorf("%s audit entries after the internal error", got[0])
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser,
		"DROP TRIGGER reject_totp_step ON accounts.users", "DROP FUNCTION accounts.reject_totp_step()"); err != nil {
		t.Fatal(err)
	}
	issued, codes, err := x.s.ReplaceTOTP(t.Context(), p, request)
	if err != nil || len(codes) != 10 {
		t.Fatalf("the retry after the internal error: %v, %d codes", err, len(codes))
	}
	if _, err := x.principal(t, console, issued); err != nil {
		t.Errorf("the session after the retry: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'user.totp_replace'"); got[0] != "1" {
		t.Errorf("%s replacement audit entries after the retry", got[0])
	}
}
