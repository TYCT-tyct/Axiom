package accounts_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"net/mail"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
	axmail "axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

type registrationWorld struct {
	site
	registration *accounts.Registration
	queue        *pg.Queue
}

// registrationSite is a seeded site with a mailer and the registration worker, in a queue that is not started: the
// test calls the worker itself, as password_reset_test.go does.
func registrationSite(t *testing.T) registrationWorld {
	t.Helper()
	x := newTestSite(t)
	registration, queue := registrationFor(t, x)
	x.s.UseRegistration(registration)
	return registrationWorld{x, registration, queue}
}

// registrationFor makes a mailer and the registration worker on a queue that is started with the notifications queue
// paused: the test calls the worker itself and no job races it.
func registrationFor(t *testing.T, x site) (*accounts.Registration, *pg.Queue) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	queue := x.db.NewQueue(logger, platform, nil)
	mailer, err := axmail.Register(t.Context(), x.db, queue, axmail.Config{
		Host: "localhost", Port: 1, From: mail.Address{Address: "no-reply@example.com"},
	}, nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec(t, x.db, platform, "INSERT INTO river.river_queue (name, paused_at) VALUES ('notifications', now()) ON CONFLICT (name) DO UPDATE SET paused_at = now()"); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := queue.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	registration, err := accounts.RegisterRegistration(x.db, queue, mailer, platform, logger)
	if err != nil {
		t.Fatal(err)
	}
	return registration, queue
}

// open turns s1's registration on with the given work and the given domain limits, then reloads the node.
func (x registrationWorld) open(t *testing.T, humanCheck bool, bits, burst, perHour int) {
	t.Helper()
	for _, statement := range []string{
		"UPDATE accounts.sites SET registration = 'open' WHERE id = :s1",
		`INSERT INTO accounts.site_settings (site_id, registration_human_check, registration_human_check_bits,
			registration_domain_burst, registration_domain_per_hour) VALUES (:s1, ` +
			boolText(humanCheck) + `, ` + itoa(bits) + `, ` + itoa(burst) + `, ` + itoa(perHour) + `)
		ON CONFLICT (site_id) DO UPDATE SET registration_human_check = excluded.registration_human_check,
			registration_human_check_bits = excluded.registration_human_check_bits,
			registration_domain_burst = excluded.registration_domain_burst,
			registration_domain_per_hour = excluded.registration_domain_per_hour`,
	} {
		if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(statement)); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func itoa(v int) string { return strconv.Itoa(v) }

func (x registrationWorld) register(t *testing.T, email, human, language string, ip netip.Addr) (accounts.RegisterResult, error) {
	t.Helper()
	return x.s.Register(t.Context(), accounts.RegisterRequest{
		Host: console, Email: email, Password: password, HumanCheck: human, Language: language, ClientIP: ip,
	})
}

func (x registrationWorld) pendingID(t *testing.T, email string) uuid.UUID {
	t.Helper()
	hash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	var id uuid.UUID
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM accounts.registration_requests
			WHERE site_id = $1 AND email_hash = $2`, s1, hash[:]).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (x registrationWorld) token(t *testing.T, email string) string {
	t.Helper()
	var body string
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT text_body FROM platform.mail_outbox
			WHERE recipient = $1 ORDER BY created_at DESC LIMIT 1`, "<"+strings.ToLower(strings.TrimSpace(email))+">").Scan(&body)
	}); err != nil {
		t.Fatal(err)
	}
	i := strings.LastIndex(body, "#token=")
	if i < 0 {
		t.Fatalf("the verification mail has no token: %q", body)
	}
	return strings.TrimSpace(body[i+len("#token="):])
}

// TestOpenRegistrationVerifiesEmail: an open registration makes no user until its link is clicked; the worker mails
// one single-use token, the click makes the user, account and workspace, and the token is spent.
func TestOpenRegistrationVerifiesEmail(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, true, 12, 100, 1000)
	const email = "new@example.com"
	nonce := accounts.ProofNonce(accounts.SiteID(s1), email, 12)
	out, err := x.register(t, " New@Example.com ", nonce, "zh", client)
	id := x.pendingID(t, email)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Pending || out.User != (accounts.UserID{}) {
		t.Fatalf("register = %+v, want a pending registration", out)
	}
	if got := count(t, x.db, "SELECT count(*) FROM accounts.users WHERE email = 'new@example.com'"); got != 0 {
		t.Fatalf("users before verification = %d, want 0", got)
	}
	if err := x.registration.ProcessRegistration(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	token := x.token(t, email)
	if token == "" {
		t.Fatal("empty token")
	}
	if _, err := x.s.VerifyEmail(t.Context(), console, token, "short"); !errors.Is(err, accounts.ErrWeakPassword) {
		t.Fatalf("a weak password at verification: %v", err)
	}
	user, err := x.s.VerifyEmail(t.Context(), console, token, password)
	if err != nil {
		t.Fatal(err)
	}
	if user == (accounts.UserID{}) {
		t.Fatal("verify returned no user")
	}
	if got := count(t, x.db, "SELECT count(*) FROM accounts.users WHERE id = '"+uuid.UUID(user).String()+"' AND email_verified_at IS NOT NULL"); got != 1 {
		t.Fatalf("verified users = %d, want 1", got)
	}
	if got := count(t, x.db, "SELECT count(*) FROM accounts.accounts WHERE site_id = '"+s1.String()+"' AND name = 'new@example.com'"); got != 1 {
		t.Fatalf("accounts = %d, want 1", got)
	}
	if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: email, Password: password, ClientIP: client}); err != nil {
		t.Fatalf("sign in after verification: %v", err)
	}
	if _, err := x.s.VerifyEmail(t.Context(), console, token, password); !errors.Is(err, accounts.ErrInvalidVerification) {
		t.Fatalf("the token was used twice: %v", err)
	}
	if err := x.registration.ProcessRegistration(t.Context(), id); err != nil {
		t.Fatalf("the worker failed on the spent request: %v", err)
	}
}

// TestOpenRegistrationDomainPolicy: an allow list is the whole set, a deny entry covers its subdomains and wins, and
// an address outside the policy is refused before any hash or mail.
func TestOpenRegistrationDomainPolicy(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 12, 100, 1000)
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
	x.open(t, true, 12, 100, 1000)
	if _, err := x.register(t, "one@example.com", "", "en", client); !errors.Is(err, accounts.ErrHumanCheck) {
		t.Fatalf("an empty proof: %v", err)
	}
	other := accounts.ProofNonce(accounts.SiteID(s1), "two@example.com", 12)
	if _, err := x.register(t, "one@example.com", other, "en", client); !errors.Is(err, accounts.ErrHumanCheck) {
		t.Fatalf("another address's proof: %v", err)
	}
	good := accounts.ProofNonce(accounts.SiteID(s1), "one@example.com", 12)
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

// TestOpenRegistrationRateLimits: one domain's burst and hourly refill hold through the node's buckets, and the IP's
// bucket is the same one sign-in uses.
func TestOpenRegistrationRateLimits(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 12, 1, 1)
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
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`UPDATE accounts.site_settings SET registration_domain_burst = 100, ip_attempt_burst = 1
			WHERE site_id = :s1`)); err != nil {
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

func count(t *testing.T, db *pg.DB, query string) int {
	t.Helper()
	var n int
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), query).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestOpenRegistrationWithoutMailRefuses: a site that turns open registration on at a node that cannot send mail
// answers busy instead of storing a request no one can complete.
func TestOpenRegistrationWithoutMailRefuses(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`UPDATE accounts.sites SET registration = 'open' WHERE id = :s1;
		 INSERT INTO accounts.site_settings (site_id, registration_human_check) VALUES (:s1, false)`)); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.Register(t.Context(), accounts.RegisterRequest{Host: console, Email: "no-mail@example.com", Password: password, ClientIP: client}); !errors.Is(err, accounts.ErrBusy) {
		t.Fatalf("without mail: %v", err)
	}
	if got := count(t, x.db, "SELECT count(*) FROM accounts.registration_requests"); got != 0 {
		t.Fatalf("pending registrations = %d, want 0", got)
	}
}

// TestSiteRegistrationPolicy: the policy reads with code defaults, writes normalized domain lists, and audits one
// change; every value outside its bounds is refused by name.
func TestSiteRegistrationPolicy(t *testing.T) {
	t.Parallel()
	o := newSettingsOf(t)
	read := func() accounts.RegistrationPolicy {
		var p accounts.RegistrationPolicy
		if err := o.db.ReadTx(t.Context(), platform, func(tx pg.Tx) (err error) {
			p, err = o.s.SiteRegistration(t.Context(), tx, accounts.SiteID(s1))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := read(); got.Mode != "invite" || !got.HumanCheck || got.HumanCheckBits != 18 || got.DomainBurst != 5 || got.DomainPerHour != 30 {
		t.Fatalf("the defaults and the site's mode: %+v", got)
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
	got := read()
	if got.Mode != "open" || got.HumanCheckBits != 20 || got.DomainBurst != 7 || got.DomainPerHour != 70 ||
		len(got.DomainAllow) != 1 || got.DomainAllow[0] != "example.com" || len(got.DomainDeny) != 1 || got.DomainDeny[0] != "bad.example.com" {
		t.Fatalf("stored policy: %+v", got)
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

// TestOpenRegistrationRechecksTheDomainAtVerification: a domain denied while its link is pending cannot create a
// user, and removing the deny leaves the unused token valid.
func TestOpenRegistrationRechecksTheDomainAtVerification(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 12, 100, 1000)
	const email = "blocked@example.com"
	if out, err := x.register(t, email, "", "en", client); err != nil || !out.Pending {
		t.Fatalf("register: %+v, %v", out, err)
	}
	if err := x.registration.ProcessRegistration(t.Context(), x.pendingID(t, email)); err != nil {
		t.Fatal(err)
	}
	token := x.token(t, email)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`UPDATE accounts.site_settings SET registration_domain_deny = '{example.com}' WHERE site_id = :s1`)); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.VerifyEmail(t.Context(), console, token, password); !errors.Is(err, accounts.ErrEmailDomainNotAllowed) {
		t.Fatalf("verify with the domain denied: %v", err)
	}
	if got := count(t, x.db, "SELECT count(*) FROM accounts.users WHERE email = '"+email+"'"); got != 0 {
		t.Fatalf("users after a denied verification = %d, want 0", got)
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`UPDATE accounts.site_settings SET registration_domain_deny = '{}' WHERE site_id = :s1`)); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.VerifyEmail(t.Context(), console, token, password); err != nil {
		t.Fatalf("verify after the deny was removed: %v", err)
	}
}
