package accounts_test

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestChangePasswordEndsOtherSessionsAndTokens(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	a, b := x.signIn(t, console), x.signIn(t, console)
	p, err := x.principal(t, console, a)
	if err != nil {
		t.Fatal(err)
	}
	token := x.token(t)
	for _, c := range []struct {
		old, new string
		want     error
	}{
		{"wrong password", changedPassword, accounts.ErrBadCredentials},
		{password, "short", accounts.ErrWeakPassword},
	} {
		if _, err := x.s.ChangePassword(t.Context(), p, accounts.PasswordChange{CurrentPassword: c.old, NewPassword: c.new}); !errors.Is(err, c.want) {
			t.Fatalf("invalid change: %v", err)
		}
	}
	issued, err := x.s.ChangePassword(t.Context(), p, accounts.PasswordChange{CurrentPassword: password, NewPassword: changedPassword})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.principal(t, console, issued); err != nil {
		t.Fatal(err)
	}
	for _, old := range []accounts.Issued{a, b} {
		if _, err := x.principal(t, console, old); !errors.Is(err, accounts.ErrNoSession) {
			t.Fatalf("old session: %v", err)
		}
	}
	if err := x.reset(t, console, token, password); !errors.Is(err, accounts.ErrInvalidReset) {
		t.Fatalf("token survived password change: %v", err)
	}
	if _, err := x.s.ChangePassword(t.Context(), p, accounts.PasswordChange{CurrentPassword: changedPassword, NewPassword: password}); !errors.Is(err, accounts.ErrBadCredentials) {
		t.Fatalf("stale principal: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'user.password_set'"); got[0] != "1" {
		t.Fatal(got)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox WHERE subject LIKE '%password changed%'"); got[0] != "1" {
		t.Fatal(got)
	}
}

func TestPasswordChangesPreserveMFA(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	_, mfa, codes := x.enableTOTP(t, console, x.signIn(t, console))
	pending := x.signIn(t, console)
	p, err := x.s.AuthenticateAny(t.Context(), x.request(t, console, "https://"+console, pending))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.ChangePassword(t.Context(), p, accounts.PasswordChange{CurrentPassword: password, NewPassword: changedPassword}); !errors.Is(err, accounts.ErrNoSession) {
		t.Fatalf("pending: %v", err)
	}
	p, err = x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := x.s.ChangePassword(t.Context(), p, accounts.PasswordChange{CurrentPassword: password, NewPassword: changedPassword})
	if err != nil {
		t.Fatal(err)
	}
	p, err = x.principal(t, console, issued)
	if err != nil || !p.Verified() {
		t.Fatalf("replacement MFA: %v", err)
	}
	// Reset ends even the verified session, but leaves TOTP enabled and old recovery codes unusable.
	if err := x.reset(t, console, x.token(t), password); err != nil {
		t.Fatal(err)
	}
	if _, err := x.principal(t, console, issued); !errors.Is(err, accounts.ErrNoSession) {
		t.Fatal(err)
	}
	signIn := x.signIn(t, console)
	if !signIn.Pending {
		t.Fatal("reset bypassed MFA")
	}
	if _, err := x.verify(t, console, signIn, codes[0]); !errors.Is(err, accounts.ErrBadCode) {
		t.Fatalf("old recovery code: %v", err)
	}
}

func TestPasswordResetAndChangeRollback(t *testing.T) {
	t.Parallel()
	for _, flow := range []string{"reset audit", "reset outbox", "change outbox", "issuance outbox"} {
		t.Run(flow, func(t *testing.T) { testPasswordRollback(t, flow) })
	}
}

func TestPasswordResetTenantAndTerminalPolicies(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	token := x.token(t)
	if got := texts(t, x.db, pg.NewScope(false, nil, []uuid.UUID{a2}), "SELECT count(*)::text FROM accounts.password_reset_tokens"); got[0] != "0" {
		t.Fatal("other tenant read tokens")
	}
	if got := texts(t, x.db, pg.NewScope(false, nil, []uuid.UUID{a1}), "SELECT count(*)::text FROM accounts.password_reset_requests"); got[0] != "0" {
		t.Fatal("account read site request addresses")
	}
	if err := x.reset(t, console, token, changedPassword); err != nil {
		t.Fatal(err)
	}
	if n, err := exec(t, x.db, platform, "UPDATE accounts.password_reset_tokens SET used_at = NULL"); err != nil || n != 0 {
		t.Fatalf("revived token: %d, %v", n, err)
	}
	err := x.db.Tx(t.Context(), pg.NewScope(false, []uuid.UUID{s2}, nil), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "INSERT INTO accounts.password_reset_requests (site_id,email_hash,id,email,language) VALUES ($1,sha256('a'),$2,'a@example.com','en')", s1, uuid.NewV7())
		return err
	})
	if err == nil {
		t.Fatal("cross-site request inserted")
	}
}

func testPasswordRollback(t *testing.T, flow string) {
	t.Parallel()
	x := recoverySite(t)
	old := x.signIn(t, console)
	p, err := x.principal(t, console, old)
	if err != nil {
		t.Fatal(err)
	}
	token := x.token(t)
	before := x.factorVersion(t)
	switch flow {
	case "reset audit":
		if err := pgtest.Run(t.Context(), x.database.Superuser, "DELETE FROM platform.tenants WHERE tenant_id = '"+uuid.UUID(p.AccountID()).String()+"'"); err != nil {
			t.Fatal(err)
		}
	default:
		if err := x.queue.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	switch flow {
	case "change outbox":
		_, err = x.s.ChangePassword(t.Context(), p, accounts.PasswordChange{CurrentPassword: password, NewPassword: changedPassword})
	case "issuance outbox":
		// Write the accepted request through SQL after the stopped queue refuses the public entry point.
		id := uuid.NewV7()
		if _, err := exec(t, x.db, platform, "UPDATE accounts.password_reset_requests SET id = '"+id.String()+"', email = 'someone@example.com'"); err != nil {
			t.Fatal(err)
		}
		err = x.recovery.ProcessPasswordReset(t.Context(), id)
	default:
		err = x.reset(t, console, token, changedPassword)
	}
	checkPasswordRollback(t, x, old, before, err)
}

func checkPasswordRollback(t *testing.T, x recoveryWorld, old accounts.Issued, before string, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("failure committed")
	}
	if got := x.factorVersion(t); got != before {
		t.Fatalf("factor version %s, was %s", got, before)
	}
	if _, err := x.principal(t, console, old); err != nil {
		t.Fatalf("old session after rollback: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.password_reset_tokens WHERE used_at IS NOT NULL"); got[0] != "0" {
		t.Fatal("rollback spent token")
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'user.password_set'"); got[0] != "0" {
		t.Fatal("rollback left audit")
	}
}

func TestChangePasswordRequiresVerifiedMFASession(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	_, mfa, _ := x.enableTOTP(t, console, x.signIn(t, console))
	if err := pgtest.Run(t.Context(), x.database.Superuser, "UPDATE accounts.sessions SET level = 'password' WHERE level = 'mfa'"); err != nil {
		t.Fatal(err)
	}
	p, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	before := x.factorVersion(t)
	if _, err := x.s.ChangePassword(t.Context(), p, accounts.PasswordChange{CurrentPassword: password, NewPassword: changedPassword}); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("password-level session with TOTP enabled: %v", err)
	}
	if got := x.factorVersion(t); got != before {
		t.Fatal("MFA refusal changed password state")
	}
}
