package accounts_test

import (
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func profilePrincipal(t *testing.T, x recoveryWorld) (accounts.Principal, accounts.Issued) {
	t.Helper()
	issued := x.signIn(t, console)
	p, err := x.principal(t, console, issued)
	if err != nil {
		t.Fatal(err)
	}
	return p, issued
}

func emailChangeToken(t *testing.T, x recoveryWorld, p accounts.Principal, email string) string {
	t.Helper()
	if err := x.s.RequestEmailChange(t.Context(), p, email, password, "en"); err != nil {
		t.Fatal(err)
	}
	rows := texts(t, x.db, platform, "SELECT text_body FROM platform.mail_outbox WHERE subject LIKE '%verify your email%' ORDER BY created_at DESC, id DESC LIMIT 1")
	_, token, ok := strings.Cut(rows[0], "#email-change-token=")
	if !ok {
		t.Fatalf("missing confirmation link: %s", rows[0])
	}
	return token
}

func confirmEmailChange(t *testing.T, x recoveryWorld, p accounts.Principal, token string) error {
	t.Helper()
	_, _, err := x.s.ConfirmEmailChange(t.Context(), p, accounts.EmailChangeConfirmation{Token: token, ClientIP: client, UserAgent: "profile test"})
	return err
}

func profileEmail(t *testing.T, x recoveryWorld) string {
	t.Helper()
	return texts(t, x.db, platform, "SELECT email FROM accounts.users WHERE id = '"+uuid.UUID(x.user).String()+"'")[0]
}

func TestDisplayName(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, _ := profilePrincipal(t, x)
	for _, name := range []string{"", " \t\n", strings.Repeat("界", 101), "a\x00b"} {
		if err := x.s.ChangeDisplayName(t.Context(), p, name); !errors.Is(err, accounts.ErrProfileName) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"  New name  ", "  " + strings.Repeat("界", 100) + "  "} {
		if err := x.s.ChangeDisplayName(t.Context(), p, name); err != nil {
			t.Fatal(err)
		}
		current, err := x.s.DisplayName(t.Context(), p)
		if err != nil || current != strings.TrimSpace(name) {
			t.Fatalf("display name: %q, %v", current, err)
		}
	}
	if err := x.s.ChangeDisplayName(t.Context(), p, strings.Repeat("界", 100)); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action='user.name_changed'"); got[0] != "2" {
		t.Fatal(got)
	}
	if err := x.s.ChangeDisplayName(t.Context(), accounts.Principal{}, "name"); !errors.Is(err, accounts.ErrNoSession) {
		t.Fatal(err)
	}
}

func TestEmailChangeLifecycle(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, issued := profilePrincipal(t, x)
	other := x.signIn(t, console)
	reset := x.token(t)
	token := emailChangeToken(t, x, p, " NEW@Example.com ")
	if got := profileEmail(t, x); got != strings.ToLower(x.email) {
		t.Fatal(got)
	}
	// Both login and recovery continue to use the old confirmed address while pending.
	x.signIn(t, console)
	replacement, _, err := x.s.ConfirmEmailChange(t.Context(), p, accounts.EmailChangeConfirmation{Token: token, ClientIP: client})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.principal(t, console, replacement); err != nil {
		t.Fatalf("replacement session: %v", err)
	}
	if got := profileEmail(t, x); got != "new@example.com" {
		t.Fatal(got)
	}
	for _, session := range []accounts.Issued{issued, other} {
		if _, err := x.principal(t, console, session); !errors.Is(err, accounts.ErrNoSession) {
			t.Fatalf("old session survived: %v", err)
		}
	}
	if err := x.reset(t, console, reset, changedPassword); !errors.Is(err, accounts.ErrInvalidReset) {
		t.Fatalf("old mailbox reset: %v", err)
	}
	if err := confirmEmailChange(t, x, p, token); !errors.Is(err, accounts.ErrEmailChangeInvalid) {
		t.Fatal(err)
	}
	checkEmailChangeResult(t, x)
}

func checkEmailChangeResult(t *testing.T, x recoveryWorld) {
	t.Helper()
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox WHERE recipient='<someone@example.com>' AND subject='Email address changed'"); got[0] != "1" {
		t.Fatal(got)
	}
	audit := texts(t, x.db, platform, "SELECT detail::text FROM platform.audit_log WHERE action='user.email_changed'")
	if len(audit) != 1 || strings.Contains(audit[0], "someone") || strings.Contains(audit[0], "new@") || !strings.Contains(audit[0], "***@example.com") {
		t.Fatal(audit)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.email_changes"); got[0] != "0" {
		t.Fatal(got)
	}
	if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: password}); !errors.Is(err, accounts.ErrBadCredentials) {
		t.Fatalf("old login: %v", err)
	}
	if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: "new@example.com", Password: password}); err != nil {
		t.Fatal(err)
	}
}

func TestEmailChangeRefusals(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, _ := profilePrincipal(t, x)
	taken := texts(t, x.db, platform, ids.Replace("SELECT email FROM accounts.users WHERE id=:u1"))[0]
	for _, c := range []struct {
		email, password string
		want            error
	}{
		{taken, "wrong password", accounts.ErrBadCredentials},
		{taken, password, accounts.ErrEmailTaken},
		{"bad address", password, accounts.ErrBadEmail},
	} {
		if err := x.s.RequestEmailChange(t.Context(), p, c.email, c.password, "en"); !errors.Is(err, c.want) {
			t.Fatalf("request: %v", err)
		}
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox"); got[0] != "0" {
		t.Fatal(got)
	}
	first := emailChangeToken(t, x, p, "first@example.com")
	second := emailChangeToken(t, x, p, "second@example.com")
	if err := confirmEmailChange(t, x, p, first); !errors.Is(err, accounts.ErrEmailChangeInvalid) {
		t.Fatal(err)
	}
	x.clock.Advance(24 * time.Hour)
	if err := confirmEmailChange(t, x, p, second); !errors.Is(err, accounts.ErrEmailChangeExpired) {
		t.Fatal(err)
	}
	if got := profileEmail(t, x); got != strings.ToLower(x.email) {
		t.Fatal(got)
	}
}

func TestEmailChangeConcurrentConfirmation(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, _ := profilePrincipal(t, x)
	token := emailChangeToken(t, x, p, "next@example.com")
	ready, release := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "SELECT 1 FROM accounts.users WHERE id=$1 FOR UPDATE", uuid.UUID(p.UserID())); err != nil {
				return err
			}
			close(ready)
			<-release
			return nil
		})
	}()
	select {
	case <-ready:
	case err := <-held:
		t.Fatal(err)
	}
	result := make(chan error, 2)
	for range 2 {
		go func() { result <- confirmEmailChange(t, x, p, token) }()
	}
	func() {
		defer close(release)
		awaitWaitingQuery(t, x.admin(t), "-- name: LockUser%", 2)
	}()
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	wins := 0
	for range 2 {
		err := <-result
		if err == nil {
			wins++
		} else if !errors.Is(err, accounts.ErrEmailChangeInvalid) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("successful confirmations: %d", wins)
	}
	got := texts(t, x.db, platform, `SELECT count(*)::text FROM platform.audit_log WHERE action='user.email_changed'
        UNION ALL SELECT count(*)::text FROM platform.mail_outbox WHERE subject='Email address changed'`)
	if strings.Join(got, ",") != "1,1" {
		t.Fatalf("audit and notification counts: %v", got)
	}
}

func TestEmailChangeConfirmationConflictAndRollback(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"taken meanwhile", "outbox", "audit", "session"} {
		t.Run(scenario, func(t *testing.T) { testEmailChangeRollback(t, scenario) })
	}
}

func testEmailChangeRollback(t *testing.T, scenario string) {
	t.Helper()

	x := recoverySite(t)
	p, original := profilePrincipal(t, x)
	version := x.factorVersion(t)
	token := emailChangeToken(t, x, p, "next@example.com")
	setup := map[string]string{
		"taken meanwhile": ids.Replace("UPDATE accounts.users SET email='next@example.com' WHERE id=:u1"),
		"outbox":          "ALTER TABLE platform.mail_outbox ADD CONSTRAINT reject_profile_mail CHECK (false) NOT VALID",
		"audit":           "ALTER TABLE platform.audit_log ADD CONSTRAINT reject_profile_audit CHECK (action <> 'user.email_changed') NOT VALID",
		"session":         "ALTER TABLE accounts.sessions ADD CONSTRAINT reject_profile_session CHECK (id = '" + uuid.UUID(original.SessionID()).String() + "') NOT VALID",
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, setup[scenario]); err != nil {
		t.Fatal(err)
	}
	err := confirmEmailChange(t, x, p, token)
	if err == nil {
		t.Fatal("confirmation succeeded")
	}
	if scenario == "taken meanwhile" && !errors.Is(err, accounts.ErrEmailTaken) {
		t.Fatal(err)
	}
	assertEmailChangeRolledBack(t, x, original, version)
}

func assertEmailChangeRolledBack(t *testing.T, x recoveryWorld, original accounts.Issued, version string) {
	t.Helper()
	if got := profileEmail(t, x); got != strings.ToLower(x.email) {
		t.Fatal(got)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.email_changes"); got[0] != "1" {
		t.Fatal(got)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action='user.email_changed'"); got[0] != "0" {
		t.Fatal(got)
	}
	if x.factorVersion(t) != version {
		t.Fatal("failed confirmation changed the factor version")
	}
	if _, err := x.principal(t, console, original); err != nil {
		t.Fatalf("failed confirmation ended the session: %v", err)
	}
}

func TestEmailChangeMFAAndTenant(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	oldP, _ := profilePrincipal(t, x)
	_, mfa, _ := x.enableTOTP(t, console, x.signIn(t, console))
	// A stale password-only principal cannot use newly enabled factors to request mail.
	if err := x.s.RequestEmailChange(t.Context(), oldP, "next@example.com", password, "en"); !errors.Is(err, accounts.ErrBadCredentials) {
		t.Fatal(err)
	}
	pending := x.signIn(t, console)
	p, err := x.s.AuthenticateAny(t.Context(), x.request(t, console, "https://"+console, pending), client)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.s.RequestEmailChange(t.Context(), p, "next@example.com", password, "en"); !errors.Is(err, accounts.ErrNoSession) {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox"); got[0] != "0" {
		t.Fatal(got)
	}
	p, err = x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	token := emailChangeToken(t, x, p, "next@example.com")
	if err := x.db.Tx(t.Context(), pg.AccountScope(a1), func(tx pg.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM accounts.email_changes").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("another account read pending email")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := confirmEmailChange(t, x, accounts.Principal{}, token); !errors.Is(err, accounts.ErrNoSession) {
		t.Fatal(err)
	}
	if err := confirmEmailChange(t, x, p, token); err != nil {
		t.Fatal(err)
	}
}

func TestEmailChangeExpiryCleanup(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, _ := profilePrincipal(t, x)
	token := emailChangeToken(t, x, p, "next@example.com")
	if _, err := exec(t, x.db, platform, "UPDATE accounts.email_changes SET expires_at=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if err := x.recovery.PurgePasswordResets(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.email_changes"); got[0] != "0" {
		t.Fatal(got)
	}
	if err := confirmEmailChange(t, x, p, token); !errors.Is(err, accounts.ErrEmailChangeInvalid) {
		t.Fatal(err)
	}
}

func TestEmailChangeSameAddressOnAnotherSite(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, _ := profilePrincipal(t, x)
	email := texts(t, x.db, platform, ids.Replace("SELECT email FROM accounts.users WHERE site_id=:s2 LIMIT 1"))[0]
	token := emailChangeToken(t, x, p, email)
	if err := confirmEmailChange(t, x, p, token); err != nil {
		t.Fatal(err)
	}
	if got := profileEmail(t, x); got != email {
		t.Fatal(got)
	}
}

func TestEmailChangeRequiresProvedMFA(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	_, mfa, _ := x.enableTOTP(t, console, x.signIn(t, console))
	verified, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	token := emailChangeToken(t, x, verified, "next@example.com")
	if err := x.admin(t).Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "UPDATE accounts.sessions SET level='password' WHERE id=$1", uuid.UUID(mfa.SessionID()))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.s.RequestEmailChange(t.Context(), p, "next@example.com", password, "en"); !errors.Is(err, accounts.ErrMFARequired) {
		t.Fatal(err)
	}
	if err := confirmEmailChange(t, x, p, token); !errors.Is(err, accounts.ErrMFARequired) {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox"); got[0] != "1" {
		t.Fatal(got)
	}
}

func TestEmailChangeFactorChangeInvalidatesPending(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, _ := profilePrincipal(t, x)
	token := emailChangeToken(t, x, p, "next@example.com")
	_, mfa, _ := x.enableTOTP(t, console, x.signIn(t, console))
	p, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirmEmailChange(t, x, p, token); !errors.Is(err, accounts.ErrEmailChangeInvalid) {
		t.Fatal(err)
	}
}

func TestEmailChangeRequestRollback(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, _ := profilePrincipal(t, x)
	first := emailChangeToken(t, x, p, "first@example.com")
	if err := pgtest.Run(t.Context(), x.database.Superuser, "ALTER TABLE platform.mail_outbox ADD CONSTRAINT reject_profile_mail CHECK (false) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if err := x.s.RequestEmailChange(t.Context(), p, "second@example.com", password, "en"); err == nil {
		t.Fatal("request survived outbox failure")
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, "ALTER TABLE platform.mail_outbox DROP CONSTRAINT reject_profile_mail"); err != nil {
		t.Fatal(err)
	}
	if err := confirmEmailChange(t, x, p, first); err != nil {
		t.Fatal(err)
	}
	if got := profileEmail(t, x); got != "first@example.com" {
		t.Fatal(got)
	}
}

func TestEmailChangeCannotConfirmAnotherUser(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, _ := profilePrincipal(t, x)
	token := emailChangeToken(t, x, p, "next@example.com")
	if _, err := x.s.Register(t.Context(), accounts.RegisterRequest{Host: console, InviteCode: inviteCode(t, x.db, ":u1"), Email: "peer@example.com", Password: password}); err != nil {
		t.Fatal(err)
	}
	issued, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: "peer@example.com", Password: password})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := x.principal(t, console, issued)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirmEmailChange(t, x, peer, token); !errors.Is(err, accounts.ErrEmailChangeInvalid) {
		t.Fatal(err)
	}
	if got := profileEmail(t, x); got != strings.ToLower(x.email) {
		t.Fatal(got)
	}
}
