package accounts_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	core "axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

const (
	opConsole   = "console.first.example"
	opAPI       = "api.first.example"
	opAdmin     = "admin.first.example"
	opEmail     = "First@Example.com"
	newPassword = "a second horse battery"
)

func currency(t testing.TB, code string) core.Currency {
	t.Helper()
	c, err := core.ParseCurrency(code)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func siteSpec(t testing.TB) accounts.SiteSpec {
	return accounts.SiteSpec{
		Name: "First", Currency: currency(t, "USD"), KeyPrefix: "sk-ax-v1-",
		Hosts: map[accounts.Surface]string{accounts.Console: opConsole, accounts.API: opAPI, accounts.Admin: opAdmin},
	}
}

// emptyDatabase is a migrated database with no site at all, which only the command line can start.
func emptyDatabase(t *testing.T) *pg.DB {
	t.Helper()
	return openPool(t, pgtest.New(t, accounts.Module()).App)
}

// openPool is a pool on url, closed when the test ends.
func openPool(t *testing.T, url string) *pg.DB {
	t.Helper()
	db, err := pg.Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

// madeSite is a migrated database with the main site made through the command line's operator, and the operator.
func madeSite(t *testing.T, opened accounts.OpenedFunc) (*pg.DB, *accounts.Operator, accounts.SiteID) {
	t.Helper()
	db := emptyDatabase(t)
	o := accounts.NewOperator(db, platform, opened)
	site, err := o.InitSite(t.Context(), siteSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	return db, o, site
}

// TestInitSiteChecksItsSpec: a spec the table would refuse, or that cannot serve, is refused before anything is
// written (no database is needed to see it).
func TestInitSiteChecksItsSpec(t *testing.T) {
	t.Parallel()
	hosts := func(console, api, admin string) map[accounts.Surface]string {
		return map[accounts.Surface]string{accounts.Console: console, accounts.API: api, accounts.Admin: admin}
	}
	cases := []struct {
		name   string
		change func(*accounts.SiteSpec)
		want   string
	}{
		{"no name", func(s *accounts.SiteSpec) { s.Name = "" }, "name"},
		{"a name of 101 characters", func(s *accounts.SiteSpec) { s.Name = strings.Repeat("n", 101) }, "name"},
		{"no currency", func(s *accounts.SiteSpec) { s.Currency = core.Currency{} }, "currency"},
		{"a main site in euros", func(s *accounts.SiteSpec) { s.Currency = currency(t, "EUR") }, "want USD"},
		{"a prefix without its hyphen", func(s *accounts.SiteSpec) { s.KeyPrefix = "sk-ax" }, "key prefix"},
		{"an upper-case prefix", func(s *accounts.SiteSpec) { s.KeyPrefix = "SK-" }, "key prefix"},
		{"no console", func(s *accounts.SiteSpec) { s.Hosts = hosts("", opAPI, "") }, "console host is required"},
		{"no api", func(s *accounts.SiteSpec) { s.Hosts = hosts(opConsole, "", "") }, "api host is required"},
		{"an upper-case host", func(s *accounts.SiteSpec) { s.Hosts = hosts("Console.Example", opAPI, "") }, "lower-case host"},
		{"a host with a scheme", func(s *accounts.SiteSpec) { s.Hosts = hosts("https://console.example", opAPI, "") }, "lower-case host"},
		{"one host for two surfaces", func(s *accounts.SiteSpec) { s.Hosts = hosts(opConsole, opConsole, "") }, "same"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			spec := siteSpec(t)
			c.change(&spec)
			_, err := accounts.NewOperator(nil, platform, nil).InitSite(t.Context(), spec)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error about %q", err, c.want)
			}
		})
	}
}

// TestInitSite: the site, its first tier and its domains are made together, once, in the main site's currency.
func TestInitSite(t *testing.T) {
	t.Parallel()
	db, o, _ := madeSite(t, nil)
	other := siteSpec(t)
	other.Hosts = map[accounts.Surface]string{accounts.Console: "c.second.example", accounts.API: "a.second.example"}
	if _, err := o.InitSite(t.Context(), other); !errors.Is(err, accounts.ErrSiteExists) {
		t.Errorf("a second main site: %v", err)
	}
	if got := texts(t, db, platform, "SELECT s.currency || ' ' || s.is_main::text || ' ' || (t.id = s.default_tier_id)::text || ' ' || s.registration "+
		"FROM accounts.sites s JOIN accounts.tiers t ON t.site_id = s.id"); len(got) != 1 || got[0] != "USD true true invite" {
		t.Errorf("the site and its tier: %v", got)
	}
	want := []string{
		"admin admin.first.example https://admin.first.example", "api api.first.example https://api.first.example",
		"console console.first.example https://console.first.example",
	}
	if got := texts(t, db, platform, "SELECT surface || ' ' || domain || ' ' || origin FROM accounts.site_domains ORDER BY surface"); !slices.Equal(got, want) {
		t.Errorf("the domains: %v, want %v", got, want)
	}
	if got := texts(t, db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'site.create'"); got[0] != "1" {
		t.Errorf("%s audit entries for the site, want 1", got[0])
	}
}

// TestInitSiteForDevelopment: a localhost host is registered with a plain-http origin, which the table allows only
// for localhost, and a site needs no admin surface.
func TestInitSiteForDevelopment(t *testing.T) {
	t.Parallel()
	db := emptyDatabase(t)
	spec := siteSpec(t)
	spec.Hosts = map[accounts.Surface]string{accounts.Console: "localhost:5173", accounts.API: "localhost:8080"}
	if _, err := accounts.NewOperator(db, platform, nil).InitSite(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	want := []string{"api http://localhost:8080", "console http://localhost:5173"}
	if got := texts(t, db, platform, "SELECT surface || ' ' || origin FROM accounts.site_domains ORDER BY surface"); !slices.Equal(got, want) {
		t.Errorf("the domains: %v, want %v", got, want)
	}
}

// TestCreateUser: the user the command makes signs in, is the owner of an account the opening hook saw, and has no
// inviter.
func TestCreateUser(t *testing.T) {
	t.Parallel()
	var opened []uuid.UUID
	db, o, site := madeSite(t, func(_ context.Context, _ pg.Tx, site, account uuid.UUID) error {
		opened = append(opened, site, account)
		return nil
	})
	user, account, err := o.CreateUser(t.Context(), opEmail, password)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 2 || opened[0] != uuid.UUID(site) || opened[1] != uuid.UUID(account) {
		t.Errorf("the opening hook saw %v, want the site and the new account", opened)
	}
	if got := texts(t, db, platform, "SELECT (u.inviter_user_id IS NULL)::text || ' ' || (u.email = 'first@example.com')::text || ' ' || r.name "+
		"FROM accounts.users u JOIN accounts.members m ON m.user_id = u.id JOIN accounts.roles r ON r.id = m.role_id WHERE u.id = '"+uuid.UUID(user).String()+"'"); len(got) != 1 || got[0] != "true true owner" {
		t.Errorf("the user: %v", got)
	}
	s := node(t, db, hashKeys(t, 1), nil)
	if _, err := s.SignIn(t.Context(), accounts.SignInRequest{Host: opConsole, Email: opEmail, Password: password, ClientIP: client}); err != nil {
		t.Errorf("the new user signing in: %v", err)
	}
}

// TestCreateUserRefusals: a user before the site, a second user with the same email, and the addresses and passwords
// registration refuses, are all refused, and a failed opening hook undoes the user.
func TestCreateUserRefusals(t *testing.T) {
	t.Parallel()
	db := emptyDatabase(t)
	o := accounts.NewOperator(db, platform, nil)
	if _, _, err := o.CreateUser(t.Context(), opEmail, password); !errors.Is(err, accounts.ErrNoSite) {
		t.Fatalf("a user before the site: %v", err)
	}
	if _, err := o.InitSite(t.Context(), siteSpec(t)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.CreateUser(t.Context(), opEmail, password); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		email, password string
		want            error
	}{
		"the same email in another case": {"FIRST@example.com", password, accounts.ErrEmailTaken},
		"a weak password":                {"second@example.com", "short", accounts.ErrWeakPassword},
		"no at sign":                     {"second.example.com", password, accounts.ErrBadEmail},
		"an at sign at the edge":         {"@example", password, accounts.ErrBadEmail},
		"a space":                        {"se cond@example.com", password, accounts.ErrBadEmail},
		"non-ASCII":                      {"sécond@example.com", password, accounts.ErrBadEmail},
	} {
		if _, _, err := o.CreateUser(t.Context(), c.email, c.password); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	failing := accounts.NewOperator(db, platform, func(context.Context, pg.Tx, uuid.UUID, uuid.UUID) error { return errors.New("no balance row") })
	if _, _, err := failing.CreateUser(t.Context(), "second@example.com", password); err == nil {
		t.Fatal("a user whose opening hook failed")
	}
	if got := texts(t, db, platform, "SELECT count(*)::text FROM accounts.users"); got[0] != "1" {
		t.Errorf("%s users after the hook failed, want only the first", got[0])
	}
}

// TestRegisterRunsTheOpeningHook: a registration opens its account through the same hook, in the same transaction, so a
// hook that fails leaves no user behind.
func TestRegisterRunsTheOpeningHook(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	var calls atomic.Int32
	var fail atomic.Bool
	s, err := accounts.NewService(db, accounts.Config{
		Platform: platform, Hashes: hashKeys(t, 1), Keyring: keyring(t),
		OnAccountOpened: func(context.Context, pg.Tx, uuid.UUID, uuid.UUID) error {
			calls.Add(1)
			if fail.Load() {
				return errors.New("no balance row")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	register := func(email string) error {
		_, err := s.Register(t.Context(), accounts.RegisterRequest{Host: console, InviteCode: inviteCode(t, db, ":u1"), Email: email, Password: password})
		return err
	}
	fail.Store(true)
	if err := register("one@example.com"); err == nil {
		t.Fatal("a registration whose hook failed")
	}
	if got := texts(t, db, platform, "SELECT count(*)::text FROM accounts.users WHERE email = 'one@example.com'"); got[0] != "0" {
		t.Error("the user stayed after the hook failed")
	}
	fail.Store(false)
	if err := register("one@example.com"); err != nil || calls.Load() != 2 {
		t.Errorf("registering after: %v, hook called %d times, want 2", err, calls.Load())
	}
	if err := register("bad"); !errors.Is(err, accounts.ErrBadEmail) {
		t.Errorf("an address without an @: %v", err)
	}
}

// principalOf is the user whose session issued is, on the console.
func principalOf(t *testing.T, s *accounts.Service, issued accounts.Issued) (accounts.Principal, error) {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+opConsole+"/", nil)
	cookie, err := s.Cookie(opConsole, issued)
	if err != nil {
		t.Fatal(err)
	}
	r.AddCookie(cookie)
	return s.Authenticate(t.Context(), r)
}

// factorSite is a site with one user, a node on it with a clock of its own, and the operator.
type factorSite struct {
	t     *testing.T
	o     *accounts.Operator
	s     *accounts.Service
	clock *clock
}

func newFactorSite(t *testing.T) factorSite {
	t.Helper()
	db, o, _ := madeSite(t, nil)
	if _, _, err := o.CreateUser(t.Context(), opEmail, password); err != nil {
		t.Fatal(err)
	}
	clk := &clock{now: time.Now()}
	return factorSite{t, o, node(t, db, hashKeys(t, 1), clk.Now), clk}
}

// signIn signs the site's user in with pw.
func (x factorSite) signIn(pw string) (accounts.Issued, error) {
	return x.s.SignIn(x.t.Context(), accounts.SignInRequest{Host: opConsole, Email: opEmail, Password: pw, ClientIP: client})
}

// TestSetPassword: a new password ends every session and the old password, and a weak one is refused.
func TestSetPassword(t *testing.T) {
	t.Parallel()
	x := newFactorSite(t)
	before, err := x.signIn(password)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.o.SetPassword(t.Context(), opEmail, newPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := principalOf(t, x.s, before); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("a session from before the new password: %v", err)
	}
	if _, err := x.signIn(password); !errors.Is(err, accounts.ErrBadCredentials) {
		t.Errorf("the old password: %v", err)
	}
	if _, err := x.signIn(newPassword); err != nil {
		t.Errorf("the new password: %v", err)
	}
	if err := x.o.SetPassword(t.Context(), opEmail, "short"); !errors.Is(err, accounts.ErrWeakPassword) {
		t.Errorf("a weak new password: %v", err)
	}
}

// TestResetMFA: resetting two-step verification ends the sessions the secret made, lets the user sign in with the
// password alone, and lets them turn it on again with the same secret (its last used step is cleared).
func TestResetMFA(t *testing.T) {
	t.Parallel()
	x := newFactorSite(t)
	first, err := x.signIn(password)
	if err != nil {
		t.Fatal(err)
	}
	p, err := principalOf(t, x.s, first)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := x.s.BeginTOTP(p)
	change := accounts.FactorChange{Secret: key, Password: password, Code: accounts.TOTPCode(key, x.clock.Now())}
	enabled, _, err := x.s.ConfirmTOTP(t.Context(), p, change)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := x.signIn(password); err != nil || !pending.Pending {
		t.Fatalf("a user with two-step verification: %+v, %v", pending, err)
	}
	if err := x.o.ResetMFA(t.Context(), opEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := principalOf(t, x.s, enabled); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("a session from before the reset: %v", err)
	}
	plain, err := x.signIn(password)
	if err != nil || plain.Pending {
		t.Fatalf("signing in after the reset: %+v, %v", plain, err)
	}
	if p, err = principalOf(t, x.s, plain); err != nil {
		t.Fatal(err)
	}
	if _, _, err := x.s.ConfirmTOTP(t.Context(), p, change); err != nil {
		t.Errorf("turning it on again after the reset: %v", err)
	}
}

// TestGrantPlatform: a role is granted once however often the command runs, to a user who exists, from a role that
// exists.
func TestGrantPlatform(t *testing.T) {
	t.Parallel()
	db, o, _ := madeSite(t, nil)
	if _, _, err := o.CreateUser(t.Context(), opEmail, password); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := o.GrantPlatform(t.Context(), opEmail, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if got := texts(t, db, platform, "SELECT r.name || ' ' || (g.granted_by IS NULL)::text || ' ' || (g.revoked_at IS NULL)::text FROM accounts.platform_grants g JOIN accounts.roles r ON r.id = g.role_id"); !slices.Equal(got, []string{"admin true true"}) {
		t.Errorf("the grants after granting twice: %v", got)
	}
	if got := texts(t, db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'platform_grant.create'"); got[0] != "1" {
		t.Errorf("%s audit entries for two grants, want 1", got[0])
	}
	if err := o.GrantPlatform(t.Context(), opEmail, "emperor"); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("a role that does not exist: %v", err)
	}
}

// TestGrantPlatformConcurrently: two grants of one role at once leave one grant and one audit entry. A superuser
// holds the grants table against inserts until both commands wait for it, so both start from a table with no grant.
func TestGrantPlatformConcurrently(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t, accounts.Module())
	db, watcher := openPool(t, database.App), openPool(t, database.Superuser)
	o := accounts.NewOperator(db, platform, nil)
	if _, err := o.InitSite(t.Context(), siteSpec(t)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.CreateUser(t.Context(), opEmail, password); err != nil {
		t.Fatal(err)
	}
	free, freed := holdLock(t, watcher, "LOCK TABLE accounts.platform_grants IN SHARE ROW EXCLUSIVE MODE")
	defer free() // a failing test must not strand the held transaction
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- o.GrantPlatform(t.Context(), opEmail, "admin") }()
	}
	waiting := 0
	for deadline := time.Now().Add(10 * time.Second); waiting < 2 && time.Now().Before(deadline); waiting = locksWaitedFor(t, watcher) {
		time.Sleep(10 * time.Millisecond)
	}
	if waiting < 2 {
		t.Errorf("%d grants waiting after 10 seconds, want both: they did not overlap", waiting)
	}
	free()
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("a grant: %v", err)
		}
	}
	if err := <-freed; err != nil {
		t.Fatal(err)
	}
	if got := texts(t, watcher, pg.Scope{}, "SELECT (SELECT count(*) FROM accounts.platform_grants WHERE revoked_at IS NULL)::text || ' ' || "+
		"(SELECT count(*) FROM platform.audit_log WHERE action = 'platform_grant.create')::text"); got[0] != "1 1" {
		t.Errorf("active grants and audit entries after two grants at once: %q, want \"1 1\"", got[0])
	}
}

// holdLock runs statement in a transaction of watcher, a superuser pool, that stays open until free is called; what the
// transaction ends with arrives on freed.
func holdLock(t *testing.T, watcher *pg.DB, statement string) (free func(), freed <-chan error) {
	t.Helper()
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- watcher.Tx(context.WithoutCancel(t.Context()), pg.Scope{}, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), statement); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-done:
		t.Fatal(err)
	}
	return sync.OnceFunc(func() { close(release) }), done
}

// TestUnknownUser: every command that names a user says so when there is none, and changes nothing.
func TestUnknownUser(t *testing.T) {
	t.Parallel()
	_, o, _ := madeSite(t, nil)
	for name, err := range map[string]error{
		"grant-platform": o.GrantPlatform(t.Context(), "nobody@example.com", "admin"),
		"set-password":   o.SetPassword(t.Context(), "nobody@example.com", password),
		"reset-mfa":      o.ResetMFA(t.Context(), "nobody@example.com"),
	} {
		if !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("%s: %v, want not found", name, err)
		}
	}
}
