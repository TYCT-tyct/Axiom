package accounts_test

import (
	"errors"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/accounts"
)

func TestEmailChangeRejectsInvalidCurrentEmailBeforeBudget(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, _ := profilePrincipal(t, x)
	user := uuid.UUID(p.UserID()).String()
	if _, err := exec(t, x.db, platform, "UPDATE accounts.users SET email='a@b@c' WHERE id='"+user+"'"); err != nil {
		t.Fatal(err)
	}
	for range 11 {
		if err := x.s.RequestEmailChange(t.Context(), p, "next@example.com", password, "en"); !errors.Is(err, accounts.ErrBadEmail) {
			t.Fatalf("invalid current email: %v", err)
		}
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox UNION ALL SELECT count(*)::text FROM accounts.email_changes"); strings.Join(got, ",") != "0,0" {
		t.Fatalf("invalid address queued work: %v", got)
	}
	if _, err := exec(t, x.db, platform, "UPDATE accounts.users SET email='someone@example.com' WHERE id='"+user+"'"); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		if err := x.s.RequestEmailChange(t.Context(), p, "next@example.com", password, "en"); err != nil {
			t.Fatalf("invalid address spent sending budget: %v", err)
		}
	}
	if err := x.s.RequestEmailChange(t.Context(), p, "next@example.com", password, "en"); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Fatal(err)
	}
}

func TestEmailChangeRotatesMFARecoveryCodes(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	_, mfa, codes := x.enableTOTP(t, console, x.signIn(t, console))
	other, err := x.verify(t, console, x.signIn(t, console), codes[0])
	if err != nil {
		t.Fatal(err)
	}
	p, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	token := emailChangeToken(t, x, p, "next@example.com")
	issued, replacements, err := x.s.ConfirmEmailChange(t.Context(), p, accounts.EmailChangeConfirmation{Token: token, ClientIP: client})
	if err != nil {
		t.Fatal(err)
	}
	current, err := x.principal(t, console, issued)
	if err != nil || !current.Verified() || len(replacements) != 10 {
		t.Fatalf("replacement MFA session: %v, %d codes", err, len(replacements))
	}
	if _, err := x.principal(t, console, other); !errors.Is(err, accounts.ErrNoSession) {
		t.Fatalf("other MFA session survived: %v", err)
	}
	x.email = "next@example.com"
	challenge := x.signIn(t, console)
	if _, err := x.verify(t, console, challenge, codes[1]); !errors.Is(err, accounts.ErrBadCode) {
		t.Fatalf("old recovery code survived: %v", err)
	}
	if _, err := x.verify(t, console, challenge, replacements[0]); err != nil {
		t.Fatalf("replacement recovery code: %v", err)
	}
}
