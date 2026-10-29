package accounts_test

import (
	"errors"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestSecurityFailuresDoNotWaitForSignalConsumer(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	inPlatform(t, x.db, func(tx pg.Tx) error {
		// The platform signal consumer holds this lock for its whole batch.
		if _, err := tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(316005)`); err != nil {
			return err
		}
		_, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: "wrong"})
		if !errors.Is(err, accounts.ErrBadCredentials) {
			t.Errorf("failure waited for global signal lock: %v", err)
		}
		return nil
	})
	if got := texts(t, x.db, platform, `SELECT count(*)::text FROM platform.alert_signals`)[0]; got != "0" {
		t.Fatal("failure persisted signal", got)
	}
}

func TestSecurityFailureNoiseAndBounds(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	revoked := x.signIn(t, console)
	principal, err := x.principal(t, console, revoked)
	if err != nil {
		t.Fatal(err)
	}
	if err = x.s.SignOut(t.Context(), principal); err != nil {
		t.Fatal(err)
	}
	expired := x.signIn(t, console)
	if err = pgtest.Run(t.Context(), x.database.Superuser, `UPDATE accounts.sessions SET last_seen_at=now()-interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	for _, issued := range []accounts.Issued{revoked, expired, {}} {
		for range 3 {
			if _, err := x.principal(t, console, issued); !errors.Is(err, accounts.ErrNoSession) {
				t.Fatal(err)
			}
		}
	}
	for _, r := range []accounts.RegisterRequest{
		{Host: console, Email: "invalid", Password: password},
		{Host: console, Email: "new@example.com", Password: "weak"},
	} {
		if _, err := x.s.Register(t.Context(), r); err == nil {
			t.Fatal("form error accepted")
		}
	}
	if got := texts(t, x.db, platform, `SELECT count(*)::text FROM accounts.security_failures`)[0]; got != "0" {
		t.Fatal("ordinary browser/form state counted", got)
	}
}

func TestForgedSessionWritesAreBounded(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	before := texts(t, x.db, platform, `SELECT count(*)::text FROM platform.audit_log`)[0]
	for range 50 {
		if _, err := x.principal(t, console, accounts.Issued{Token: "forged"}); !errors.Is(err, accounts.ErrNoSession) {
			t.Fatal(err)
		}
	}
	if got := texts(t, x.db, platform, `SELECT sum(failures+completed_peak)::text FROM accounts.security_failures`)[0]; got != "20" {
		t.Fatal("forged cookie writes not IP bounded", got)
	}
	if got := texts(t, x.db, platform, `SELECT count(*)::text FROM platform.audit_log`)[0]; got != before {
		t.Fatal("forged cookie audit flood", got)
	}
}

func TestThrottledFailuresDoNotWrite(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	// Sign-in's separate source exhausts its failure budget; later refusals must do no writes.
	for range 10 {
		if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: "wrong"}); !errors.Is(err, accounts.ErrBadCredentials) {
			t.Fatal(err)
		}
	}
	counts := texts(t, x.db, platform, `SELECT (failures+completed_peak)::text FROM accounts.security_failures`)[0]
	before := texts(t, x.db, platform, `SELECT count(*)::text FROM platform.audit_log`)[0]
	for range 10 {
		if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: "wrong"}); !errors.Is(err, accounts.ErrTooManyAttempts) {
			t.Fatal(err)
		}
	}
	if got := texts(t, x.db, platform, `SELECT count(*)::text FROM platform.audit_log`)[0]; got != before {
		t.Fatal("throttled attempts wrote audits", got)
	}
	if got := texts(t, x.db, platform, `SELECT (failures+completed_peak)::text FROM accounts.security_failures`)[0]; got != counts {
		t.Fatal("throttled attempts changed counts", got)
	}
}

func TestRolePermissionsRollingCompatibility(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	// This is the old node's exact projection; its registry lacks both new names.
	for _, name := range texts(t, x.db, platform, `SELECT unnest(permissions) FROM accounts.roles`) {
		if name == "balance.top_up" || name == "users.manage" {
			t.Fatal("old snapshot would reject", name)
		}
	}
	for _, name := range texts(t, x.db, platform, `SELECT unnest(permissions || additional_permissions) FROM accounts.roles`) {
		if _, ok := accounts.PermissionNamed(name); !ok {
			t.Fatal("migration granted undeclared permission", name)
		}
	}
}

func TestUnknownPermissionsAreIgnored(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, `UPDATE accounts.roles SET additional_permissions=array_append(additional_permissions,'future.permission')`); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := x.principal(t, console, x.signIn(t, console))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.Authorize(t.Context(), p, accounts.TopUpBalance, accounts.AccountTarget(p.AccountID())); err != nil {
		t.Fatal("known permission lost", err)
	}
	info, err := x.s.SessionInfo(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range info.Permissions {
		if name == "future.permission" {
			t.Fatal("unknown permission exposed")
		}
	}
}

func TestMFAAuditNamesIssuedSession(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	_, _, codes := x.enableTOTP(t, console, x.signIn(t, console))
	issued, err := x.verify(t, console, x.signIn(t, console), codes[0])
	if err != nil {
		t.Fatal(err)
	}
	p, err := x.principal(t, console, issued)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, `SELECT detail->>'session_id' FROM platform.audit_log WHERE action='security.mfa' ORDER BY id DESC LIMIT 1`)[0]; got != p.SessionID().TypeID() {
		t.Fatal("MFA audit names wrong session", got)
	}
}

func TestSecurityCounterDoesNotRewind(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	// Model an older statement acquiring the row after a newer minute's writer.
	if _, err := exec(t, x.db, platform, ids.Replace(`INSERT INTO accounts.security_failures(site_id,started_at,failures) VALUES(:s1,now()+interval '1 hour',1)`)); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: "wrong"}); !errors.Is(err, accounts.ErrBadCredentials) {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, `SELECT (started_at>now()+interval '30 minutes' AND failures=2)::text FROM accounts.security_failures`)[0]; got != "true" {
		t.Fatal("older statement rewound counter", got)
	}
}
