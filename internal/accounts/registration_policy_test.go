package accounts_test

import (
	"errors"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestRegistrationRefusals: registration needs the invite code of an enabled user of the site, a site that registers
// by invitation, a console domain, a password of 12 characters or more, and an email not yet registered there.
func TestRegistrationRefusals(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	code := inviteCode(t, x.db, ":u1")
	for name, c := range map[string]struct {
		r    accounts.RegisterRequest
		want error
	}{
		"an unknown code": {accounts.RegisterRequest{
			Host: console, InviteCode: "nosuchcode0000", Email: "new@example.com", Password: password, ClientIP: client,
		}, accounts.ErrInvalidInvite},
		"another site's code": {accounts.RegisterRequest{
			Host: console, InviteCode: inviteCode(t, x.db, ":u2"), Email: "new@example.com", Password: password, ClientIP: client,
		}, accounts.ErrInvalidInvite},
		"a site that is closed": {accounts.RegisterRequest{
			Host: otherConsole, InviteCode: inviteCode(t, x.db, ":u2"), Email: "new@example.com", Password: password, ClientIP: client,
		}, accounts.ErrRegistrationClosed},
		"a short password": {accounts.RegisterRequest{
			Host: console, InviteCode: code, Email: "new@example.com", Password: "short", ClientIP: client,
		}, accounts.ErrWeakPassword},
		"an email registered": {accounts.RegisterRequest{
			Host: console, InviteCode: code, Email: " SOMEONE@example.com", Password: password, ClientIP: client,
		}, accounts.ErrEmailTaken},
		"the admin domain": {accounts.RegisterRequest{
			Host: admin, InviteCode: code, Email: "new@example.com", Password: password, ClientIP: client,
		}, accounts.ErrUnknownHost},
	} {
		if _, err := x.s.Register(t.Context(), c.r); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace("UPDATE accounts.users SET status = 'disabled' WHERE id = :u1")); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.Register(t.Context(), accounts.RegisterRequest{Host: console, InviteCode: code, Email: "new@example.com", Password: password, ClientIP: client}); !errors.Is(err, accounts.ErrInvalidInvite) {
		t.Errorf("a disabled inviter: %v", err)
	}
}

// TestOpenRegistrationDomainPolicy: an allow list is the whole set, a deny entry covers its subdomains and wins, and
// an address outside the policy is refused before any hash or mail.
func TestOpenRegistrationDomainPolicy(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 100, 1000)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`UPDATE accounts.site_settings SET registration_domain_allow = '{example.com}',
			registration_domain_deny = '{bad.example.com}' WHERE site_id = :s1`)); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		email string
		want  error
	}{
		"the allowed domain":           {"one@example.com", nil},
		"a subdomain of the allowed":   {"two@sub.example.com", nil},
		"a suffix that is not a label": {"three@badexample.com", accounts.ErrEmailDomainNotAllowed},
		"a denied subdomain":           {"four@bad.example.com", accounts.ErrEmailDomainNotAllowed},
		"another domain":               {"five@other.com", accounts.ErrEmailDomainNotAllowed},
	} {
		_, err := x.register(t, c.email, "", "en", client)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

// TestOpenRegistrationHumanCheck: the proof is bound to the site and the normalized address, and a site that turns it
// off takes an empty proof.
func TestOpenRegistrationHumanCheck(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, true, 100, 1000)
	if _, err := x.register(t, "one@example.com", "", "en", client); !errors.Is(err, accounts.ErrHumanCheck) {
		t.Fatalf("an empty proof: %v", err)
	}
	other := accounts.ProofNonce(accounts.SiteID(s1), "two@example.com", openBits)
	if _, err := x.register(t, "one@example.com", other, "en", client); !errors.Is(err, accounts.ErrHumanCheck) {
		t.Fatalf("another address's proof: %v", err)
	}
	good := accounts.ProofNonce(accounts.SiteID(s1), "one@example.com", openBits)
	if out, err := x.register(t, "one@example.com", good, "en", client); err != nil || !out.Pending {
		t.Fatalf("the solved proof: %+v, %v", out, err)
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`UPDATE accounts.site_settings SET registration_human_check = false WHERE site_id = :s1`)); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if out, err := x.register(t, "three@example.com", "", "en", client); err != nil || !out.Pending {
		t.Fatalf("a site without a check: %+v, %v", out, err)
	}
}

// TestOpenRegistrationDomainLimit: one domain's burst and hourly refill hold in the node's buckets.
func TestOpenRegistrationDomainLimit(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 1, 1)
	if out, err := x.register(t, "a@example.com", "", "en", client); err != nil || !out.Pending {
		t.Fatalf("first registration: %+v, %v", out, err)
	}
	if _, err := x.register(t, "b@example.com", "", "en", client); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Fatalf("the domain's second registration: %v", err)
	}
	x.clock.Advance(time.Hour)
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if out, err := x.register(t, "b@example.com", "", "en", client); err != nil || !out.Pending {
		t.Fatalf("after the domain refilled: %+v, %v", out, err)
	}
}

// TestOpenRegistrationIPLimit: the IP's registration attempts are the same bucket sign-in spends.
func TestOpenRegistrationIPLimit(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`UPDATE accounts.sites SET registration = 'open' WHERE id = :s1;
		 INSERT INTO accounts.site_settings (site_id, registration_human_check, registration_domain_burst,
			registration_domain_per_hour, ip_attempt_burst) VALUES (:s1, false, 100, 1000, 1)`)); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	ip := netip.MustParseAddr("198.51.100.9")
	if out, err := x.register(t, "c@example.com", "", "en", ip); err != nil || !out.Pending {
		t.Fatalf("first from the IP: %+v, %v", out, err)
	}
	if _, err := x.register(t, "d@example.com", "", "en", ip); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Fatalf("the IP's second registration: %v", err)
	}
	if out, err := x.register(t, "e@example.com", "", "en", netip.MustParseAddr("198.51.100.10")); err != nil || !out.Pending {
		t.Fatalf("another IP: %+v, %v", out, err)
	}
}

// TestOpenRegistrationUsesTheSharedDomainBucket: the domain's attempt is spent in the shared store when the node has
// one, with the same key the other nodes would use.
func TestOpenRegistrationUsesTheSharedDomainBucket(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`UPDATE accounts.sites SET registration = 'open' WHERE id = :s1;
		 INSERT INTO accounts.site_settings (site_id, registration_human_check, registration_domain_burst,
			registration_domain_per_hour) VALUES (:s1, false, 1, 1)`)); err != nil {
		t.Fatal(err)
	}
	namespace := kvtest.Namespace()
	s := cachedNode(t, x.db, kvtest.New(t).OpenIn(t, namespace, kv.Deps{Now: clientClock(x.clock)}), x.clock.Now)
	registration, _ := registrationFor(t, x)
	w := registrationWorld{x, registration, nil}
	s.UseRegistration(w.registration)
	w.s = s
	if err := s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if out, err := w.register(t, "shared@example.com", "", "en", client); err != nil || !out.Pending {
		t.Fatalf("first registration: %+v, %v", out, err)
	}
	key := s.SharedKeyForTest(accounts.SiteID(s1), "signup_domain", "example.com")
	if key == "" {
		t.Fatal("the shared key is empty")
	}
	if _, err := w.register(t, "shared2@example.com", "", "en", client); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Fatalf("the shared bucket did not hold: %v", err)
	}
}

// readRegistrationPolicy reads s1's policy in a read transaction.
func readRegistrationPolicy(t *testing.T, o settingsOf) accounts.RegistrationPolicy {
	t.Helper()
	var p accounts.RegistrationPolicy
	if err := o.db.ReadTx(t.Context(), platform, func(tx pg.Tx) (err error) {
		p, err = o.s.SiteRegistration(t.Context(), tx, accounts.SiteID(s1))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSiteRegistrationPolicy: the policy reads with code defaults, writes normalized domain lists, and audits one
// change; a hundred long domains fit an audit entry.
func TestSiteRegistrationPolicy(t *testing.T) {
	t.Parallel()
	o := newSettingsOf(t)
	want := accounts.DefaultRegistrationPolicy()
	want.Mode = "invite"
	if diff := cmp.Diff(want, readRegistrationPolicy(t, o)); diff != "" {
		t.Fatalf("the defaults and the site's mode (-want +got):\n%s", diff)
	}
	set := accounts.RegistrationPolicy{
		Mode: "open", HumanCheck: true, HumanCheckBits: 20, DomainBurst: 7, DomainPerHour: 70,
		DomainAllow: []string{" Example.COM ", "example.com"}, DomainDeny: []string{"Bad.Example.COM"},
	}
	if err := o.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return o.s.SetSiteRegistration(t.Context(), tx, operatorActor, accounts.SiteID(s1), set, "because")
	}); err != nil {
		t.Fatal(err)
	}
	want = accounts.RegistrationPolicy{
		Mode: "open", HumanCheck: true, HumanCheckBits: 20, DomainBurst: 7, DomainPerHour: 70,
		DomainAllow: []string{"example.com"}, DomainDeny: []string{"bad.example.com"},
	}
	if diff := cmp.Diff(want, readRegistrationPolicy(t, o)); diff != "" {
		t.Fatalf("stored policy (-want +got):\n%s", diff)
	}
	if n := count(t, o.db, "SELECT count(*) FROM platform.audit_log WHERE action = 'site_registration.update'"); n != 1 {
		t.Fatalf("audit entries = %d, want 1", n)
	}
	big := accounts.DefaultRegistrationPolicy()
	big.Mode = "invite"
	for i := range 100 {
		big.DomainAllow = append(big.DomainAllow, "d"+strconv.Itoa(i)+".example.com")
	}
	if err := o.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return o.s.SetSiteRegistration(t.Context(), tx, operatorActor, accounts.SiteID(s1), big, "large enough to exceed an audit entry if copied")
	}); err != nil {
		t.Fatalf("a policy with 100 domains: %v", err)
	}
	if n := count(t, o.db, "SELECT count(*) FROM platform.audit_log WHERE action = 'site_registration.update'"); n != 2 {
		t.Fatalf("audit entries after the large policy = %d, want 2", n)
	}
}

// TestSiteRegistrationPolicyRefusals: every value outside its bounds is refused by name.
func TestSiteRegistrationPolicyRefusals(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		change func(*accounts.RegistrationPolicy)
		field  string
	}{
		"mode":     {func(p *accounts.RegistrationPolicy) { p.Mode = "sometimes" }, "registration_mode"},
		"bits":     {func(p *accounts.RegistrationPolicy) { p.HumanCheckBits = 11 }, "registration_human_check_bits"},
		"burst":    {func(p *accounts.RegistrationPolicy) { p.DomainBurst = 0 }, "registration_domain_burst"},
		"per hour": {func(p *accounts.RegistrationPolicy) { p.DomainPerHour = 100_001 }, "registration_domain_per_hour"},
		"allow":    {func(p *accounts.RegistrationPolicy) { p.DomainAllow = []string{"-bad.example.com"} }, "registration_domain_allow"},
		"deny":     {func(p *accounts.RegistrationPolicy) { p.DomainDeny = []string{"not a domain"} }, "registration_domain_deny"},
		"too many": {func(p *accounts.RegistrationPolicy) { p.DomainAllow = make([]string, 101) }, "registration_domain_allow"},
	} {
		p := accounts.DefaultRegistrationPolicy()
		p.Mode = "invite"
		c.change(&p)
		if err := p.Check(); !refuses(err, c.field) {
			t.Errorf("%s: %v, want a refusal of %q", name, err, c.field)
		}
	}
}
