package accounts_test

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/accounts"
)

func TestEmailChangeDomainPolicy(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, email, policy string
		allowed             bool
	}{
		{"allowed", " NEXT@EXAMPLE.COM ", "registration_domain_allow='{example.com}'", true},
		{"allowed subdomain", "next@sub.example.com", "registration_domain_allow='{example.com}'", true},
		{"outside allow", "next@gmail.com", "registration_domain_allow='{example.com}'", false},
		{"denied", "next@example.com", "registration_domain_deny='{example.com}'", false},
		{"deny wins", "next@bad.example.com", "registration_domain_allow='{example.com}', registration_domain_deny='{bad.example.com}'", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			testEmailChangeDomainPolicy(t, c.email, c.policy, c.allowed)
		})
	}
}

func testEmailChangeDomainPolicy(t *testing.T, email, policy string, allowed bool) {
	t.Helper()
	x := recoverySite(t)
	p, _ := profilePrincipal(t, x)
	if _, err := exec(t, x.db, platform, ids.Replace("INSERT INTO accounts.site_settings (site_id) VALUES (:s1) ON CONFLICT DO NOTHING")); err != nil {
		t.Fatal(err)
	}
	// Issue before the restriction, so confirmation must recheck the pending address.
	token := emailChangeToken(t, x, p, email)
	setEmailChangePolicy(t, x, policy)
	var want error
	if !allowed {
		want = accounts.ErrEmailDomainNotAllowed
	}
	if err := confirmEmailChange(t, x, p, token); !errors.Is(err, want) {
		t.Fatalf("confirmation: %v, want %v", err, want)
	}
	if allowed {
		return
	}
	if got := profileEmail(t, x); got != strings.ToLower(x.email) {
		t.Fatalf("rejected confirmation changed email: %s", got)
	}
	if err := x.s.RequestEmailChange(t.Context(), p, email, password, "en"); !errors.Is(err, want) {
		t.Fatalf("request: %v, want %v", err, want)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox"); got[0] != "1" {
		t.Fatalf("rejected change sent mail: %v", got)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action='user.email_changed'"); got[0] != "0" {
		t.Fatalf("rejected change audited success: %v", got)
	}
	// Removing the restriction leaves the unconsumed link usable.
	setEmailChangePolicy(t, x, "registration_domain_allow='{}', registration_domain_deny='{}'")
	if err := confirmEmailChange(t, x, p, token); err != nil {
		t.Fatal(err)
	}
}

func setEmailChangePolicy(t *testing.T, x recoveryWorld, policy string) {
	t.Helper()
	if _, err := exec(t, x.db, platform, ids.Replace("UPDATE accounts.site_settings SET "+policy+" WHERE site_id=:s1")); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestEmailChangeRejectsInvalidNewEmailBeforeBudget(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, _ := profilePrincipal(t, x)
	for range 11 {
		if err := x.s.RequestEmailChange(t.Context(), p, "foo bar@x.com", password, "en"); !errors.Is(err, accounts.ErrBadEmail) {
			t.Fatalf("invalid new email: %v", err)
		}
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox UNION ALL SELECT count(*)::text FROM accounts.email_changes"); got[0] != "0" || got[1] != "0" {
		t.Fatalf("invalid address queued work: %v", got)
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
