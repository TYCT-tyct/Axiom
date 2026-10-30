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
	axmail "axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// openBits is the proof of work the tests open a site at: low enough that ProofNonce solves it at once.
const openBits = 12

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
func (x registrationWorld) open(t *testing.T, humanCheck bool, burst, perHour int) {
	t.Helper()
	x.openAt(t, s1, humanCheck, burst, perHour)
}

func (x registrationWorld) openAt(t *testing.T, site uuid.UUID, humanCheck bool, burst, perHour int) {
	t.Helper()
	key := ":s1"
	if site == s2 {
		key = ":s2"
	} else if site != s1 {
		t.Fatalf("openAt: unknown site %s", site)
	}
	for _, statement := range []string{
		"UPDATE accounts.sites SET registration = 'open' WHERE id = " + key,
		`INSERT INTO accounts.site_settings (site_id, registration_human_check, registration_human_check_bits,
			registration_domain_burst, registration_domain_per_hour) VALUES (` + key + `, ` +
			boolText(humanCheck) + `, ` + itoa(openBits) + `, ` + itoa(burst) + `, ` + itoa(perHour) + `)
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

// registerPending registers email at host with no proof, and fails the test unless it became a pending registration.
func (x registrationWorld) registerPending(t *testing.T, host, email string) {
	t.Helper()
	out, err := x.s.Register(t.Context(), accounts.RegisterRequest{Host: host, Email: email, ClientIP: client})
	if err != nil || !out.Pending {
		t.Fatalf("register %s at %s: %+v, %v", email, host, out, err)
	}
}

// verifyAt verifies token at host, and fails the test unless it makes the user.
func (x registrationWorld) verifyAt(t *testing.T, host, token string) {
	t.Helper()
	if _, err := x.s.VerifyEmail(t.Context(), host, token, password, nil, client); err != nil {
		t.Fatalf("verify at %s: %v", host, err)
	}
}

func (x registrationWorld) pendingID(t *testing.T, email string) uuid.UUID {
	t.Helper()
	return x.pendingIDAt(t, s1, email)
}

func (x registrationWorld) pendingIDAt(t *testing.T, site uuid.UUID, email string) uuid.UUID {
	t.Helper()
	hash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	var id uuid.UUID
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM accounts.registration_requests
			WHERE site_id = $1 AND email_hash = $2`, site, hash[:]).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (x registrationWorld) mail(t *testing.T, email string) string {
	t.Helper()
	var body string
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT text_body FROM platform.mail_outbox
			WHERE recipient = $1 ORDER BY created_at DESC LIMIT 1`, "<"+strings.ToLower(strings.TrimSpace(email))+">").Scan(&body)
	}); err != nil {
		t.Fatal(err)
	}
	return body
}

func (x registrationWorld) token(t *testing.T, email string) string {
	t.Helper()
	body := x.mail(t, email)
	_, token, ok := strings.CutLast(body, "#token=")
	if !ok {
		t.Fatalf("the verification mail has no token: %q", body)
	}
	return strings.TrimSpace(token)
}

// TestOpenRegistrationWaitsForVerification: an open registration makes no user until its link is clicked; the worker
// mails one link to the console page, and the page's password is checked there.
func TestOpenRegistrationWaitsForVerification(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, true, 100, 1000)
	const email = "new@example.com"
	nonce := accounts.ProofNonce(accounts.SiteID(s1), email, openBits)
	out, err := x.register(t, " New@Example.com ", nonce, "zh", client)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Pending || out.User != (accounts.UserID{}) {
		t.Fatalf("register = %+v, want a pending registration", out)
	}
	if got := count(t, x.db, "SELECT count(*) FROM accounts.users WHERE email = 'new@example.com'"); got != 0 {
		t.Fatalf("users before verification = %d, want 0", got)
	}
	if err := x.registration.ProcessRegistration(t.Context(), x.pendingID(t, email)); err != nil {
		t.Fatal(err)
	}
	if body := x.mail(t, email); !strings.Contains(body, "https://"+console+"/console/verify-email#token=") {
		t.Fatalf("the verification link does not open the console page: %q", body)
	}
	if _, err := x.s.VerifyEmail(t.Context(), console, x.token(t, email), "short", nil, client); !errors.Is(err, accounts.ErrWeakPassword) {
		t.Fatalf("a weak password at verification: %v", err)
	}
}

// TestVerificationCreatesOneUser: the click makes the user, account and workspace with a verified address, the token
// is single-use, and a redelivered job leaves the spent request alone.
func TestVerificationCreatesOneUser(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 100, 1000)
	const email = "new@example.com"
	x.registerPending(t, console, email)
	id := x.pendingID(t, email)
	if err := x.registration.ProcessRegistration(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	token := x.token(t, email)
	user, err := x.s.VerifyEmail(t.Context(), console, token, password, nil, client)
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
	if _, err := x.s.VerifyEmail(t.Context(), console, token, password, nil, client); !errors.Is(err, accounts.ErrInvalidVerification) {
		t.Fatalf("the token was used twice: %v", err)
	}
	if err := x.registration.ProcessRegistration(t.Context(), id); err != nil {
		t.Fatalf("the worker failed on the spent request: %v", err)
	}
}

// TestVerificationOfATakenEmailDeletesTheRequest: an address registered while its link was pending is refused with
// ErrEmailTaken, and the request is deleted so the next attempt gets the same answer instead of finding it again.
func TestVerificationOfATakenEmailDeletesTheRequest(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 100, 1000)
	const email = "taken@example.com"
	x.registerPending(t, console, email)
	if err := x.registration.ProcessRegistration(t.Context(), x.pendingID(t, email)); err != nil {
		t.Fatal(err)
	}
	token := x.token(t, email)
	// The site cannot take an invitation while it registers openly, so it goes back to invitation and the same address
	// is registered there: the pending request now names a taken address.
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`UPDATE accounts.sites SET registration = 'invite' WHERE id = :s1`)); err != nil {
		t.Fatal(err)
	}
	if err := x.s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.Register(t.Context(), accounts.RegisterRequest{
		Host: console, InviteCode: inviteCode(t, x.db, ":u1"), Email: email, Password: password, ClientIP: client,
	}); err != nil {
		t.Fatalf("an invitation for the same address: %v", err)
	}
	if _, err := x.s.VerifyEmail(t.Context(), console, token, password, nil, client); !errors.Is(err, accounts.ErrEmailTaken) {
		t.Fatalf("verify of a taken address: %v", err)
	}
	if got := count(t, x.db, "SELECT count(*) FROM accounts.registration_requests"); got != 0 {
		t.Fatalf("pending requests after a taken address = %d, want 0", got)
	}
}

// TestRegistrationRequestTenantScopesOneRequest: the lookup by id returns only the named row. A missing id must not
// borrow another site's scope, and two pending requests on two sites must each issue and consume their own link.
func TestRegistrationRequestTenantScopesOneRequest(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 100, 1000)
	x.openAt(t, s2, false, 100, 1000)
	const first, second = "first@example.com", "second@example.com"
	x.registerPending(t, console, first)
	x.registerPending(t, otherConsole, second)
	var ran bool
	found, err := x.db.TenantTx(t.Context(), "accounts.registration_request_tenant", uuid.NewV7(), func(pg.Tx) error {
		ran = true
		return nil
	})
	if err != nil || found || ran {
		t.Fatalf("the lookup for a missing request = %v, %v, ran %v; want it not found", found, err, ran)
	}
	for _, id := range []uuid.UUID{x.pendingIDAt(t, s1, first), x.pendingIDAt(t, s2, second)} {
		if err := x.registration.ProcessRegistration(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	firstToken, secondToken := x.token(t, first), x.token(t, second)
	if firstToken == secondToken {
		t.Fatal("two pending requests got the same token")
	}
	x.verifyAt(t, console, firstToken)
	x.verifyAt(t, otherConsole, secondToken)
	if got := count(t, x.db, "SELECT count(*) FROM accounts.users WHERE email IN ('"+first+"', '"+second+"')"); got != 2 {
		t.Fatalf("verified users = %d, want 2", got)
	}
}

// TestRegistrationRetriesAfterATenantLookupFailure: a database error while learning the tenant must fail the job, not
// mark it done; the same pending request succeeds when the database is available again.
func TestRegistrationRetriesAfterATenantLookupFailure(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 100, 1000)
	const email = "retry@example.com"
	if _, err := x.s.Register(t.Context(), accounts.RegisterRequest{Host: console, Email: email, ClientIP: client}); err != nil {
		t.Fatal(err)
	}
	id := x.pendingID(t, email)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := x.registration.ProcessRegistration(ctx, id); err == nil {
		t.Fatal("a canceled lookup was reported as a completed job")
	}
	if got := count(t, x.db, "SELECT count(*) FROM platform.mail_outbox"); got != 0 {
		t.Fatalf("mail after a failed lookup = %d, want 0", got)
	}
	if err := x.registration.ProcessRegistration(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if x.token(t, email) == "" {
		t.Fatal("the retry sent no token")
	}
}

// TestVerifyEmailSpendsTheIPBudget: a verification attempt costs one of the IP's authentication attempts before the
// token is looked up or a password is hashed, so one token cannot be used to occupy the hash semaphore indefinitely.
func TestVerifyEmailSpendsTheIPBudget(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 100, 1000)
	token := strings.Repeat("a", 43)
	ip := netip.MustParseAddr("198.51.100.99")
	for range 20 {
		if _, err := x.s.VerifyEmail(t.Context(), console, token, password, nil, ip); !errors.Is(err, accounts.ErrInvalidVerification) {
			t.Fatalf("an unknown token: %v", err)
		}
	}
	if _, err := x.s.VerifyEmail(t.Context(), console, token, password, nil, ip); !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Fatalf("the 21st verification: %v", err)
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

// TestExpiredRegistrationsArePurged: the cleanup worker deletes a request once its 24 hours are over and leaves a
// pending one alone.
func TestExpiredRegistrationsArePurged(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 100, 1000)
	const expired, pending = "expired@example.com", "pending@example.com"
	x.registerPending(t, console, expired)
	x.registerPending(t, console, pending)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`UPDATE accounts.registration_requests SET expires_at = now() - interval '1 minute'
		 WHERE site_id = :s1 AND email = 'expired@example.com'`)); err != nil {
		t.Fatal(err)
	}
	if err := x.registration.PurgeRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := count(t, x.db, "SELECT count(*) FROM accounts.registration_requests WHERE email = 'expired@example.com'"); got != 0 {
		t.Fatalf("expired requests = %d, want 0", got)
	}
	if got := count(t, x.db, "SELECT count(*) FROM accounts.registration_requests"); got != 1 {
		t.Fatalf("requests after the purge = %d, want the pending one", got)
	}
}

// TestOpenRegistrationRechecksTheDomainAtVerification: a domain denied while its link is pending cannot create a
// user, and removing the deny leaves the unused token valid.
func TestOpenRegistrationRechecksTheDomainAtVerification(t *testing.T) {
	t.Parallel()
	x := registrationSite(t)
	x.open(t, false, 100, 1000)
	const email = "blocked@example.com"
	x.registerPending(t, console, email)
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
	if _, err := x.s.VerifyEmail(t.Context(), console, token, password, nil, client); !errors.Is(err, accounts.ErrEmailDomainNotAllowed) {
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
	if _, err := x.s.VerifyEmail(t.Context(), console, token, password, nil, client); err != nil {
		t.Fatalf("verify after the deny was removed: %v", err)
	}
}
