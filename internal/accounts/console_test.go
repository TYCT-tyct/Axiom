package accounts_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

const (
	console      = "main.example" // s1's console domain, from the seed
	admin        = "admin.main.example"
	otherConsole = "console.second.example"
	password     = "correct horse battery"
)

// clock is a test's controllable time.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *clock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

// site is a seeded database with an admin domain for s1 and a console domain for s2, a node on it with a clock of
// its own, and a user registered on s1 with u1's invite code.
type site struct {
	db       *pg.DB
	database pgtest.Database
	s        *accounts.Service
	clock    *clock
	user     accounts.UserID
	email    string
}

func newTestSite(t *testing.T) site {
	t.Helper()
	db, database := open(t)
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(`INSERT INTO accounts.site_domains VALUES
		('`+admin+`', :s1, 'admin', 'https://`+admin+`', NULL),
		('`+otherConsole+`', :s2, 'console', 'https://`+otherConsole+`', NULL)`)); err != nil {
		t.Fatal(err)
	}
	c := &clock{now: time.Now()}
	s := node(t, db, hashKeys(t, 1), c.Now)
	email := "Someone@Example.com"
	user, err := s.Register(t.Context(), accounts.RegisterRequest{Host: console, InviteCode: inviteCode(t, db, ":u1"), Email: email, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	return site{db, database, s, c, user, email}
}

func inviteCode(t *testing.T, db *pg.DB, user string) string {
	t.Helper()
	return texts(t, db, platform, ids.Replace("SELECT invite_code FROM accounts.users WHERE id = "+user))[0]
}

// signIn signs the user in at host and fails the test if it cannot.
func (x site) signIn(t *testing.T, host string) accounts.Issued {
	t.Helper()
	issued, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: host, Email: x.email, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

// request is a state-changing request to host from origin carrying JSON, with issued's cookie and CSRF token.
func (x site) request(t *testing.T, host, origin string, issued accounts.Issued) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+host+"/", strings.NewReader("{}"))
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	if issued.Token != "" {
		cookie, err := x.s.Cookie(host, issued)
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(cookie)
		r.Header.Set("X-Csrf-Token", issued.CSRFToken)
	}
	return r
}

func (x site) principal(t *testing.T, host string, issued accounts.Issued) (accounts.Principal, error) {
	t.Helper()
	return x.s.Authenticate(t.Context(), x.request(t, host, "https://"+host, issued))
}

// TestRegisterSignInAndUse: a user registered with an invite code signs in, is recognised from the cookie, may make
// a key in its own account, whose audit entry the registration's tenant record admits, may not touch another
// account, and is no longer recognised once signed out.
func TestRegisterSignInAndUse(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	issued := x.signIn(t, console)
	p, err := x.principal(t, console, issued)
	if err != nil || p.UserID() != x.user || issued.Pending {
		t.Fatalf("authenticate: %v, %+v", err, issued)
	}
	if got := texts(t, x.db, platform, "SELECT a.tier_id::text || ' ' || a.kind || ' ' || w.is_default::text FROM accounts.accounts a "+
		"JOIN accounts.workspaces w ON w.account_id = a.id WHERE a.id = '"+uuid.UUID(p.AccountID()).String()+"'"); len(got) != 1 || got[0] != t1.String()+" personal true" {
		t.Errorf("the new account: %v", got)
	}
	scope, err := x.s.Authorize(t.Context(), p, accounts.ManageKeys, accounts.AccountTarget(p.AccountID()))
	if err != nil {
		t.Fatal(err)
	}
	workspace := texts(t, x.db, platform, "SELECT id::text FROM accounts.workspaces WHERE account_id = '"+uuid.UUID(p.AccountID()).String()+"'")[0]
	if err := x.db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		_, err := x.s.CreateKey(t.Context(), tx, system, accounts.KeyRequest{
			Site: accounts.SiteID(s1), Account: p.AccountID(), Workspace: accounts.WorkspaceID(uuid.MustParse(workspace)),
			CreatedBy: p.UserID(), Name: "Mine",
		})
		return err
	}); err != nil {
		t.Errorf("a key in the authorized scope: %v", err)
	}
	if _, err := x.s.Authorize(t.Context(), p, accounts.ManageKeys, accounts.AccountTarget(accounts.AccountID(a1))); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("another account: %v, want not found", err)
	}
	if err := x.s.SignOut(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := x.principal(t, console, issued); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("after signing out: %v", err)
	}
}

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
		"an unknown code":       {accounts.RegisterRequest{console, "nosuchcode0000", "new@example.com", password, client}, accounts.ErrInvalidInvite},
		"another site's code":   {accounts.RegisterRequest{console, inviteCode(t, x.db, ":u2"), "new@example.com", password, client}, accounts.ErrInvalidInvite},
		"a site that is closed": {accounts.RegisterRequest{otherConsole, inviteCode(t, x.db, ":u2"), "new@example.com", password, client}, accounts.ErrInvalidInvite},
		"a short password":      {accounts.RegisterRequest{console, code, "new@example.com", "short", client}, accounts.ErrWeakPassword},
		"an email registered":   {accounts.RegisterRequest{console, code, " SOMEONE@example.com", password, client}, accounts.ErrEmailTaken},
		"the admin domain":      {accounts.RegisterRequest{admin, code, "new@example.com", password, client}, accounts.ErrUnknownHost},
	} {
		if _, err := x.s.Register(t.Context(), c.r); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace("UPDATE accounts.users SET status = 'disabled' WHERE id = :u1")); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.Register(t.Context(), accounts.RegisterRequest{console, code, "new@example.com", password, client}); !errors.Is(err, accounts.ErrInvalidInvite) {
		t.Errorf("a disabled inviter: %v", err)
	}
}

// TestSignInRefusals: an unknown email, a wrong password, a disabled user and a user of another site are refused
// alike.
func TestSignInRefusals(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	for name, r := range map[string]accounts.SignInRequest{
		"a wrong password":     {Host: console, Email: x.email, Password: "wrong password!"},
		"an unknown email":     {Host: console, Email: "nobody@example.com", Password: password},
		"another site's login": {Host: otherConsole, Email: x.email, Password: password},
	} {
		if _, err := x.s.SignIn(t.Context(), r); !errors.Is(err, accounts.ErrBadCredentials) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, "UPDATE accounts.users SET status = 'disabled' WHERE email = 'someone@example.com'"); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: password}); !errors.Is(err, accounts.ErrBadCredentials) {
		t.Errorf("a disabled user: %v", err)
	}
}

// TestRequestsMustComeFromTheSite: a state-changing request needs the domain's exact origin, a JSON body and its
// session's CSRF token; a session works only on the site and surface it was issued for, and not once idle too long.
func TestRequestsMustComeFromTheSite(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	issued := x.signIn(t, console)
	cases := map[string]func(*http.Request){
		"no origin":            func(r *http.Request) { r.Header.Del("Origin") },
		"a null origin":        func(r *http.Request) { r.Header.Set("Origin", "null") },
		"another origin":       func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"a form body":          func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
		"no CSRF token":        func(r *http.Request) { r.Header.Del("X-Csrf-Token") },
		"another CSRF token":   func(r *http.Request) { r.Header.Set("X-Csrf-Token", issued.Token) },
		"a JSON body (passes)": func(*http.Request) {},
	}
	for name, change := range cases {
		r := x.request(t, console, "https://"+console, issued)
		change(r)
		_, err := x.s.Authenticate(t.Context(), r)
		if pass := strings.HasSuffix(name, "(passes)"); pass != (err == nil) || !pass && !errors.Is(err, accounts.ErrBadOrigin) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := x.s.CheckOrigin(x.request(t, console, "https://evil.example", accounts.Issued{})); !errors.Is(err, accounts.ErrBadOrigin) {
		t.Errorf("a sign-in from another origin: %v", err)
	}
	if _, err := x.principal(t, admin, issued); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("a console session on the admin surface: %v", err)
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, "UPDATE accounts.sessions SET last_seen_at = now() - interval '8 days'"); err != nil {
		t.Fatal(err)
	}
	if _, err := x.principal(t, console, issued); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("an idle session: %v", err)
	}
}

// enableTOTP turns on two-step verification for the user signed in with issued, returning the secret, the new
// session and the recovery codes.
func (x site) enableTOTP(t *testing.T, host string, issued accounts.Issued) (string, accounts.Issued, []string) {
	t.Helper()
	p, err := x.principal(t, host, issued)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := x.s.BeginTOTP(p)
	code := accounts.TOTPCode(key, x.clock.Now())
	if _, _, err := x.s.ConfirmTOTP(t.Context(), p, accounts.FactorChange{Secret: key, Password: "wrong password!", Code: code}); !errors.Is(err, accounts.ErrBadCredentials) {
		t.Errorf("a wrong password: %v", err)
	}
	if _, _, err := x.s.ConfirmTOTP(t.Context(), p, accounts.FactorChange{Secret: key, Password: password, Code: "000000"}); !errors.Is(err, accounts.ErrBadCode) && code != "000000" {
		t.Errorf("a wrong code: %v", err)
	}
	mfa, codes, err := x.s.ConfirmTOTP(t.Context(), p, accounts.FactorChange{Secret: key, Password: password, Code: code})
	if err != nil || len(codes) != 10 {
		t.Fatalf("confirm: %v, %d codes", err, len(codes))
	}
	return key, mfa, codes
}

// verify passes two-step verification for a pending session at host.
func (x site) verify(t *testing.T, host string, pending accounts.Issued, code string) (accounts.Issued, error) {
	t.Helper()
	return x.s.VerifyMFA(t.Context(), x.request(t, host, "https://"+host, pending), code, client, "test")
}

// TestTwoStepVerification: turning on TOTP ends every other session; sign-in then waits for a code, which a pending
// session cannot skip; a TOTP step and a recovery code each work once.
func TestTwoStepVerification(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	first, second := x.signIn(t, console), x.signIn(t, console)
	key, mfa, codes := x.enableTOTP(t, console, first)
	if _, err := x.principal(t, console, second); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("another session after the factors changed: %v", err)
	}
	if _, err := x.principal(t, console, mfa); err != nil {
		t.Errorf("the session ConfirmTOTP issued: %v", err)
	}
	pending := x.signIn(t, console)
	if _, err := x.principal(t, console, pending); !pending.Pending || !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("a pending session: %+v, %v", pending, err)
	}
	if _, err := x.verify(t, console, pending, accounts.TOTPCode(key, x.clock.Now())); !errors.Is(err, accounts.ErrBadCode) {
		t.Errorf("the step ConfirmTOTP used: %v", err)
	}
	x.clock.Advance(30 * time.Second)
	if _, err := x.verify(t, console, pending, accounts.TOTPCode(key, x.clock.Now())); err != nil {
		t.Errorf("a current code: %v", err)
	}
	again := x.signIn(t, console)
	if _, err := x.verify(t, console, again, accounts.TOTPCode(key, x.clock.Now())); !errors.Is(err, accounts.ErrBadCode) {
		t.Errorf("the same TOTP step again: %v", err)
	}
	if _, err := x.verify(t, console, again, codes[0]); err != nil {
		t.Errorf("a recovery code: %v", err)
	}
	if _, err := x.verify(t, console, x.signIn(t, console), strings.ToUpper(codes[0])); !errors.Is(err, accounts.ErrBadCode) {
		t.Errorf("a recovery code again: %v", err)
	}
}

// TestCodesRunOutAndRenew: wrong codes count until a pending session has none left, after which even a right code is
// refused; new recovery codes end the old ones.
func TestCodesRunOutAndRenew(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	key, mfa, codes := x.enableTOTP(t, console, x.signIn(t, console))
	tries := x.signIn(t, console)
	for range 5 {
		if _, err := x.verify(t, console, tries, "000000"); !errors.Is(err, accounts.ErrBadCode) {
			t.Errorf("a wrong code: %v", err)
		}
	}
	if _, err := x.verify(t, console, tries, codes[1]); !errors.Is(err, accounts.ErrBadCode) {
		t.Errorf("a right code after five wrong ones: %v", err)
	}
	x.clock.Advance(30 * time.Second)
	p, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	_, renewed, err := x.s.RegenerateRecoveryCodes(t.Context(), p, accounts.FactorChange{Code: accounts.TOTPCode(key, x.clock.Now())})
	if err != nil || len(renewed) != 10 {
		t.Fatalf("new recovery codes: %v", err)
	}
	if _, err := x.verify(t, console, x.signIn(t, console), codes[2]); !errors.Is(err, accounts.ErrBadCode) {
		t.Errorf("an old recovery code: %v", err)
	}
}

// factorVersion is the registered user's factor version.
func (x site) factorVersion(t *testing.T) string {
	t.Helper()
	return texts(t, x.db, platform, "SELECT factor_version::text FROM accounts.users WHERE id = '"+uuid.UUID(x.user).String()+"'")[0]
}

// TestTOTPTurnsOnOnce: a user with two-step verification cannot turn it on again, with a new secret or with the one
// in use and a code of the step it last used, and the refusal changes nothing: the factor version, the session, the
// recovery codes.
func TestTOTPTurnsOnOnce(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	key, mfa, codes := x.enableTOTP(t, console, x.signIn(t, console))
	version := x.factorVersion(t)
	p, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := x.s.BeginTOTP(p)
	for name, c := range map[string]accounts.FactorChange{
		"a new secret":            {Secret: fresh, Password: password, Code: accounts.TOTPCode(fresh, x.clock.Now())},
		"the secret in use again": {Secret: key, Password: password, Code: accounts.TOTPCode(key, x.clock.Now())},
	} {
		if _, _, err := x.s.ConfirmTOTP(t.Context(), p, c); !errors.Is(err, accounts.ErrTOTPOn) {
			t.Errorf("%s: %v, want %v", name, err, accounts.ErrTOTPOn)
		}
	}
	if got := x.factorVersion(t); got != version {
		t.Errorf("factor version %s after the refusals, was %s", got, version)
	}
	if _, err := x.principal(t, console, mfa); err != nil {
		t.Errorf("the session after the refusals: %v", err)
	}
	if _, err := x.verify(t, console, x.signIn(t, console), codes[0]); err != nil {
		t.Errorf("a recovery code after the refusals: %v", err)
	}
}

// TestTOTPConfirmationsAtOnce: two confirmations at once for a user without two-step verification give one.
func TestTOTPConfirmationsAtOnce(t *testing.T) {
	t.Parallel()
	y := newTestSite(t)
	q, err := y.principal(t, console, y.signIn(t, console))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			secretText, _ := y.s.BeginTOTP(q)
			_, _, err := y.s.ConfirmTOTP(context.WithoutCancel(t.Context()), q, accounts.FactorChange{
				Secret: secretText, Password: password, Code: accounts.TOTPCode(secretText, y.clock.Now()),
			})
			results <- err
		}()
	}
	won := 0
	for range 2 {
		if err := <-results; err == nil {
			won++
		} else if !errors.Is(err, accounts.ErrBadCredentials) && !errors.Is(err, accounts.ErrTOTPOn) {
			t.Errorf("the losing confirmation: %v", err)
		}
	}
	if won != 1 {
		t.Errorf("%d of two confirmations at once succeeded", won)
	}
}

// TestFactorChangesAreAudited: turning on TOTP and new recovery codes each leave an entry naming the user and its
// tenant, in the change's transaction: an entry rejected by the database undoes the factors, recovery codes
// and new session with it.
func TestFactorChangesAreAudited(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	key, mfa, _ := x.enableTOTP(t, console, x.signIn(t, console))
	x.clock.Advance(30 * time.Second)
	p, err := x.principal(t, console, mfa)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := x.s.RegenerateRecoveryCodes(t.Context(), p, accounts.FactorChange{Code: accounts.TOTPCode(key, x.clock.Now())}); err != nil {
		t.Fatal(err)
	}
	user, account := uuid.UUID(x.user).String(), uuid.UUID(p.AccountID()).String()
	want := "user.totp_enable " + x.user.TypeID() + " " + s1.String() + " " + account + " user:" + user
	got := texts(t, x.db, platform, "SELECT action || ' ' || target_id || ' ' || site_id || ' ' || account_id || ' ' || actor_kind || ':' || actor_id "+
		"FROM platform.audit_log WHERE action IN ('user.totp_enable','user.recovery_codes_regenerate') ORDER BY id")
	if len(got) != 2 || got[0] != want || got[1] != strings.Replace(want, "user.totp_enable", "user.recovery_codes_regenerate", 1) {
		t.Errorf("audit entries %q, want %q and its recovery_codes_regenerate", got, want)
	}

	y := newTestSite(t)
	q, err := y.principal(t, console, y.signIn(t, console))
	if err != nil {
		t.Fatal(err)
	}
	user = uuid.UUID(y.user).String()
	if err := pgtest.Run(t.Context(), y.database.Superuser, `ALTER TABLE platform.audit_log ADD CONSTRAINT reject_factor_audit CHECK (action NOT LIKE 'user.%') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	secretText, _ := y.s.BeginTOTP(q)
	if _, _, err := y.s.ConfirmTOTP(t.Context(), q, accounts.FactorChange{
		Secret: secretText, Password: password, Code: accounts.TOTPCode(secretText, y.clock.Now()),
	}); err == nil {
		t.Fatal("a factor change whose audit entry fails succeeded")
	}
	state := texts(t, y.db, platform, "SELECT u.factor_version || ' ' || (u.mfa_enabled_at IS NULL) || ' ' || "+
		"(SELECT count(*) FROM accounts.recovery_codes WHERE user_id = u.id) || ' ' || "+
		"(SELECT count(*) FROM accounts.sessions WHERE user_id = u.id) FROM accounts.users u WHERE u.id = '"+user+"'")
	if len(state) != 1 || state[0] != "1 true 0 1" {
		t.Errorf("after the failed change: version, no TOTP, recovery codes, sessions = %q, want \"1 true 0 1\"", state)
	}
	if _, err := y.principal(t, console, y.signIn(t, console)); err != nil {
		t.Errorf("a password sign-in after the failed change: %v", err)
	}
}

// TestOneChallengeOneSession: two codes sent at once for one pending session give one session. The pending session's
// row is held locked from outside until both verifications are waiting, so they overlap: the user's lock orders them,
// the first revokes the pending session, and the second finds it gone; the revocation must also change one row.
func TestOneChallengeOneSession(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	_, _, codes := x.enableTOTP(t, console, x.signIn(t, console))
	pending := x.signIn(t, console)
	watcher := x.admin(t)
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- watcher.Tx(context.WithoutCancel(t.Context()), pg.Scope{}, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "SELECT 1 FROM accounts.sessions WHERE level = 'pending_mfa' AND revoked_at IS NULL FOR UPDATE"); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	free := sync.OnceFunc(func() { close(release) })
	defer free() // a failing watcher query must not strand the held transaction
	results := make(chan error, 2)
	for _, code := range codes[:2] {
		go func() {
			_, err := x.verify(t, console, pending, code)
			results <- err
		}()
	}
	waiting := 0
	for deadline := time.Now().Add(10 * time.Second); waiting < 2 && time.Now().Before(deadline); waiting = locksWaitedFor(t, watcher) {
		time.Sleep(10 * time.Millisecond)
	}
	if waiting < 2 {
		t.Errorf("%d verifications waiting after 10 seconds, want both: they did not overlap", waiting)
	}
	free()
	won := 0
	for range 2 {
		if err := <-results; err == nil {
			won++
		} else if !errors.Is(err, accounts.ErrNoSession) {
			t.Errorf("the losing code: %v", err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if won != 1 {
		t.Errorf("%d sessions from one challenge", won)
	}
}

// admin is a pool logged in as the superuser, for holding locks and watching them.
func (x site) admin(t *testing.T) *pg.DB {
	t.Helper()
	db, err := pg.Open(t.Context(), x.database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

// locksWaitedFor is how many of the database's queries wait for a lock, as watcher, a superuser pool, sees.
func locksWaitedFor(t *testing.T, watcher *pg.DB) int {
	t.Helper()
	n, err := strconv.Atoi(texts(t, watcher, pg.Scope{}, "SELECT count(*)::text FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()")[0])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestPlatformAuthorization: the platform scope needs an unrevoked platform grant whose role holds the permission,
// a session on the admin surface and two-step verification in that session; a grant made after a password sign-in
// does not make that session strong enough.
func TestPlatformAuthorization(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	weak := x.signIn(t, admin)
	if err := pgtest.Run(t.Context(), x.database.Superuser, `INSERT INTO accounts.platform_grants (id, site_id, account_id, user_id, role_id)
		SELECT gen_random_uuid(), site_id, account_id, id, (SELECT id FROM accounts.roles WHERE name = 'admin')
		FROM accounts.users WHERE email = 'someone@example.com'`); err != nil {
		t.Fatal(err)
	}
	p, err := x.principal(t, admin, weak)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.Authorize(t.Context(), p, accounts.ManageSites, accounts.PlatformTarget()); !errors.Is(err, accounts.ErrForbidden) {
		t.Errorf("a password session: %v, want forbidden", err)
	}
	key, _, _ := x.enableTOTP(t, admin, weak)
	strong := func(host string) accounts.Principal {
		t.Helper()
		x.clock.Advance(30 * time.Second)
		mfa, err := x.verify(t, host, x.signIn(t, host), accounts.TOTPCode(key, x.clock.Now()))
		if err != nil {
			t.Fatal(err)
		}
		p, err := x.principal(t, host, mfa)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p = strong(admin)
	scope, err := x.s.Authorize(t.Context(), p, accounts.ManageSites, accounts.PlatformTarget())
	if err != nil {
		t.Fatalf("an admin session after two-step verification: %v", err)
	}
	if n := texts(t, x.db, scope, "SELECT count(*)::text FROM accounts.sites")[0]; n != "2" {
		t.Errorf("the platform scope sees %s sites", n)
	}
	if _, err := x.s.Authorize(t.Context(), strong(console), accounts.ManageSites, accounts.PlatformTarget()); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("a console session: %v, want not found", err)
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, "UPDATE accounts.platform_grants SET revoked_at = now()"); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.Authorize(t.Context(), p, accounts.ManageSites, accounts.PlatformTarget()); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("a revoked grant: %v, want not found", err)
	}
}
