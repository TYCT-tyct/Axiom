package accounts_test

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

func TestPasswordResetExpiryRemovesPersonalData(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	x.token(t)
	for _, table := range []string{"password_reset_requests", "password_reset_tokens"} {
		if n, err := exec(t, x.db, platform, "DELETE FROM accounts."+table); err != nil || n != 0 {
			t.Fatalf("deleted active %s: %d %v", table, n, err)
		}
	}
	x.expireUnknownResetRequest(t)
	if _, err := exec(t, x.db, platform, "UPDATE accounts.password_reset_requests SET expires_at = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if err := x.reset(t, console, strings.Repeat("x", 43), changedPassword); !errors.Is(err, accounts.ErrInvalidReset) {
		t.Fatal(err)
	}
	// Token expiry is immutable to the application; the test advances the database state as its clock would.
	if err := x.admin(t).Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "UPDATE accounts.password_reset_tokens SET expires_at = now() - interval '1 second'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.recovery.PurgePasswordResets(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"password_reset_requests", "password_reset_tokens"} {
		if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts."+table); got[0] != "0" {
			t.Fatal(got)
		}
	}
}

func TestPasswordResetUsesRegisteredRequestDomain(t *testing.T) {
	t.Parallel()
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "preferred", true: "disabled fallback"}[disabled], func(t *testing.T) { testRequestedResetDomain(t, disabled) })
	}
}

func TestPasswordEmailValidationAndPermanentFailure(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	for _, email := range []string{"first,last@x.com", "a@b@c"} {
		if _, err := x.s.Register(t.Context(), accounts.RegisterRequest{Host: console, Email: email, Password: password}); !errors.Is(err, accounts.ErrBadEmail) {
			t.Fatalf("register: %v", err)
		}
		if err := x.s.RequestPasswordReset(t.Context(), accounts.PasswordResetRequest{Host: console, Email: email}); !errors.Is(err, accounts.ErrBadEmail) {
			t.Fatalf("request: %v", err)
		}
	}
	id := x.requestReset(t, x.email, "en")
	if _, err := exec(t, x.db, platform, "UPDATE accounts.site_domains SET disabled_at = now() WHERE surface = 'console'"); err != nil {
		t.Fatal(err)
	}
	if err := x.recovery.ProcessPasswordReset(t.Context(), id); err == nil {
		t.Fatal("missing-domain failure was lost")
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.password_reset_requests WHERE email IS NOT NULL"); got[0] != "0" {
		t.Fatal("permanent failure retained email")
	}
}

func (x recoveryWorld) expireUnknownResetRequest(t *testing.T) {
	t.Helper()
	x.requestReset(t, "unknown@example.com", "en")
	if _, err := exec(t, x.db, platform, "UPDATE accounts.password_reset_requests SET expires_at = now() - interval '1 second' WHERE email IS NOT NULL"); err != nil {
		t.Fatal(err)
	}
	if err := x.recovery.PurgePasswordResets(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.password_reset_requests"); got[0] != "1" {
		t.Fatal(got)
	}
}

func testRequestedResetDomain(t *testing.T, disabled bool) {
	x := recoverySite(t)
	if _, err := exec(t, x.db, platform, ids.Replace("INSERT INTO accounts.site_domains VALUES ('z-brand.example',:s1,'console','https://z-brand.example',NULL)")); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	id := x.requestReset(t, x.email, "en", "z-brand.example")
	want := "https://z-brand.example/"
	if disabled {
		if _, err := exec(t, x.db, platform, "UPDATE accounts.site_domains SET disabled_at = now() WHERE domain = 'z-brand.example'"); err != nil {
			t.Fatal(err)
		}
		want = "https://main.example/"
	}
	if err := x.recovery.ProcessPasswordReset(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT text_body FROM platform.mail_outbox"); len(got) != 1 || !strings.Contains(got[0], want) {
		t.Fatalf("link: %q", got)
	}
}
