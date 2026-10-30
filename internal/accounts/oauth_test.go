package accounts_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// oauthService is a node whose provider calls reach a test server instead of the internet.
func oauthService(t *testing.T, db *pg.DB, exit *egress.Client) *accounts.Service {
	t.Helper()
	s, err := accounts.NewService(db, accounts.Config{
		Platform: platform, Hashes: hashKeys(t, 1), Keyring: keyring(t), Egress: exit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	return s
}

// fakeOAuth is a provider's token and user endpoints. The subject it reports and the email it reports are separate:
// a test can make the email match a user while the subject does not, which must not merge them.
type fakeOAuth struct {
	subject atomic.Int64
	email   atomic.Value
	gotForm atomic.Value // url.Values of the token request
	deny    atomic.Bool
}

func newFakeOAuth(t *testing.T, subject int64, email string) (*fakeOAuth, *httptest.Server) {
	t.Helper()
	f := &fakeOAuth{}
	f.subject.Store(subject)
	f.email.Store(email)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			http.Redirect(w, r, "/token", http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.gotForm.Store(r.PostForm)
			if f.deny.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"the-token","token_type":"bearer"}`))
		case "/user":
			if r.Header.Get("Authorization") != "Bearer the-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": f.subject.Load(), "sub": fmt.Sprint(f.subject.Load()), "login": "octocat",
				"name": "The Octocat", "email": f.email.Load(),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return f, server
}

func (f *fakeOAuth) configure(t *testing.T, s *accounts.Service, server *httptest.Server) {
	t.Helper()
	s.SetOAuthEndpointsForTesting("github", server.URL+"/authorize", server.URL+"/token", server.URL+"/user")
}

// startURL runs a start and returns its URL and state.
func startURL(t *testing.T, s *accounts.Service, p *accounts.Principal) (string, string, string) {
	t.Helper()
	start, err := s.StartOAuth(t.Context(), console, "github", p, client)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	return start.URL, u.Query().Get("state"), start.Nonce
}

// callback builds the provider's redirect back to the site, with the session cookie when linking.
func callback(t *testing.T, s *accounts.Service, state, nonce string, issued *accounts.Issued) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"https://"+console+"/api/console/v1/auth/oauth/github/callback?code=the-code&state="+url.QueryEscape(state), nil)
	if nonce != "" {
		cookie, err := s.OAuthNonceCookie(console, nonce)
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(cookie)
	}
	if issued != nil {
		cookie, err := s.Cookie(console, *issued)
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(cookie)
	}
	return r
}

// TestOAuthLinksOnlyWhileSignedInAndSignsInBySubject: a provider account attaches to the signed-in user, a later
// sign-in finds that user by the provider's subject, and an unknown subject is not registered even when its email is a
// user's.
func TestOAuthLinksOnlyWhileSignedInAndSignsInBySubject(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	usersBefore := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.users")[0]
	fake, server := newFakeOAuth(t, 42, x.email)
	exit := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	defer exit.CloseIdleConnections()
	s := oauthService(t, x.db, exit)
	fake.configure(t, s, server)
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		return s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{LoginDomain: &domain}, "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return s.SetSiteOAuthProvider(t.Context(), tx, system, accounts.SiteID(s1), "github", "client-id", "client-secret", "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	passwordSession := x.signIn(t, console)
	p, err := s.Authenticate(t.Context(), x.request(t, console, "https://"+console, passwordSession), client)
	if err != nil {
		t.Fatal(err)
	}
	var nonce string
	_, state, _ := startURL(t, s, &p)
	out, err := s.CompleteOAuth(t.Context(), callback(t, s, state, "", &passwordSession), "github", "the-code", state, client, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Linked || out.Issued == nil {
		t.Fatalf("link: %+v", out)
	}
	if got := texts(t, x.db, platform, "SELECT provider || ' ' || subject || ' ' || display_name FROM accounts.user_identities"); len(got) != 1 || got[0] != "github 42 The Octocat" {
		t.Fatalf("identity: %v", got)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'user.identity_link'"); got[0] != "1" {
		t.Errorf("link audits: %v", got)
	}

	// A user has one identity per provider: linking a second GitHub account is refused until the first is unlinked,
	// and says so rather than claiming someone else holds it.
	fresh := x.signIn(t, console)
	freshPrincipal, err := s.Authenticate(t.Context(), x.request(t, console, "https://"+console, fresh), client)
	if err != nil {
		t.Fatal(err)
	}
	fake.subject.Store(7)
	_, state, _ = startURL(t, s, &freshPrincipal)
	if _, err := s.CompleteOAuth(t.Context(), callback(t, s, state, "", &fresh), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrIdentityAlreadyLinked) {
		t.Errorf("a second account of the same provider: %v", err)
	}

	// The linked user signs in from the provider subject alone; the email the provider reports is a different one and
	// is deliberately not the user's.
	fake.subject.Store(42)
	fake.email.Store("someone-else@example.com")
	_, state, nonce = startURL(t, s, nil)
	// The callback must return in the browser that began the flow: without the nonce cookie, or with a different one,
	// the state is not spent and no session is issued.
	if _, err := s.CompleteOAuth(t.Context(), callback(t, s, state, "", nil), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrOAuthFailed) {
		t.Errorf("a callback without the nonce cookie: %v", err)
	}
	if _, err := s.CompleteOAuth(t.Context(), callback(t, s, state, "another-browser", nil), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrOAuthFailed) {
		t.Errorf("a callback with another browser's nonce: %v", err)
	}
	out, err = s.CompleteOAuth(t.Context(), callback(t, s, state, nonce, nil), "github", "the-code", state, client, "test")
	if err != nil || out.Issued == nil || out.Issued.Pending {
		t.Fatalf("sign-in: %+v %v", out, err)
	}
	if p, err := s.Authenticate(t.Context(), func() *http.Request {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+console+"/", nil)
		cookie, _ := s.Cookie(console, *out.Issued)
		r.AddCookie(cookie)
		return r
	}(), client); err != nil || p.UserID() != x.user {
		t.Errorf("the session is the linked user's: %v %v", p.UserID(), err)
	}

	// A provider account no one linked is refused even when the email it reports is this user's: no identity is attached
	// by email. Replaying the state changes nothing either.
	fake.subject.Store(99)
	fake.email.Store(x.email)
	_, state, nonce = startURL(t, s, nil)
	if _, err := s.CompleteOAuth(t.Context(), callback(t, s, state, nonce, nil), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrAccountNotLinked) {
		t.Errorf("unknown subject: %v", err)
	}
	if _, err := s.CompleteOAuth(t.Context(), callback(t, s, state, nonce, nil), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrOAuthFailed) {
		t.Errorf("replayed state: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.users")[0]; got != usersBefore {
		t.Errorf("users: %v, want the %v before the flows", got, usersBefore)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.user_identities"); got[0] != "1" {
		t.Errorf("an unlinked provider account was attached by email: %v", got)
	}
}

// TestOAuthLinkNeedsTheSameSession: a callback without the session that began a link attaches nothing.
func TestOAuthLinkNeedsTheSameSession(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	fake, server := newFakeOAuth(t, 7, x.email)
	exit := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	defer exit.CloseIdleConnections()
	s := oauthService(t, x.db, exit)
	fake.configure(t, s, server)
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		return s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{LoginDomain: &domain}, "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return s.SetSiteOAuthProvider(t.Context(), tx, system, accounts.SiteID(s1), "github", "client-id", "client-secret", "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	session := x.signIn(t, console)
	p, err := s.Authenticate(t.Context(), x.request(t, console, "https://"+console, session), client)
	if err != nil {
		t.Fatal(err)
	}
	_, state, _ := startURL(t, s, &p)
	if _, err := s.CompleteOAuth(t.Context(), callback(t, s, state, "", nil), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("a link without its session: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.user_identities"); got[0] != "0" {
		t.Errorf("identities: %v", got)
	}
}

// TestOAuthStartUsesPKCE: Google's and GitHub's authorization URLs carry an S256 challenge and the token request the
// matching verifier. GitHub OAuth Apps accept PKCE since July 2025.
func TestOAuthStartUsesPKCE(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	fake, server := newFakeOAuth(t, 1, x.email)
	exit := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	defer exit.CloseIdleConnections()
	s := oauthService(t, x.db, exit)
	fake.configure(t, s, server)
	s.SetOAuthEndpointsForTesting("google", server.URL+"/authorize", server.URL+"/token", server.URL+"/user")
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		if err := s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{LoginDomain: &domain}, "test"); err != nil {
			return err
		}
		return s.SetSiteOAuthProvider(t.Context(), tx, system, accounts.SiteID(s1), "google", "client-id", "client-secret", "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	start, err := s.StartOAuth(t.Context(), console, "google", nil, client)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.URL)
	query := u.Query()
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		t.Fatalf("PKCE in the authorization URL: %s", start.URL)
	}
	state := query.Get("state")
	if _, err := s.CompleteOAuth(t.Context(), callback(t, s, state, start.Nonce, nil), "google", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrAccountNotLinked) {
		t.Fatalf("a provider subject no one linked: %v", err)
	}
	form, _ := fake.gotForm.Load().(url.Values)
	verifier := form.Get("code_verifier")
	if verifier == "" {
		t.Fatalf("the token request has no code_verifier: %v", form)
	}
	sum := sha256.Sum256([]byte(verifier))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != query.Get("code_challenge") {
		t.Fatalf("the verifier hashes to %s, the challenge is %s", got, query.Get("code_challenge"))
	}
	// GitHub has its own endpoints, already pointed at the fake by configure; give it an application and repeat.
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return s.SetSiteOAuthProvider(t.Context(), tx, system, accounts.SiteID(s1), "github", "client-id", "client-secret", "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	gh, err := s.StartOAuth(t.Context(), console, "github", nil, client)
	if err != nil {
		t.Fatal(err)
	}
	gu, _ := url.Parse(gh.URL)
	gq := gu.Query()
	if gq.Get("code_challenge_method") != "S256" || gq.Get("code_challenge") == "" {
		t.Fatalf("GitHub PKCE in the authorization URL: %s", gh.URL)
	}
	if _, err := s.CompleteOAuth(t.Context(), callback(t, s, gq.Get("state"), gh.Nonce, nil), "github", "the-code", gq.Get("state"), client, "test"); !errors.Is(err, accounts.ErrAccountNotLinked) {
		t.Fatalf("a GitHub subject no one linked: %v", err)
	}
	form, _ = fake.gotForm.Load().(url.Values)
	verifier = form.Get("code_verifier")
	if verifier == "" {
		t.Fatalf("the GitHub token request has no code_verifier: %v", form)
	}
	sum = sha256.Sum256([]byte(verifier))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != gq.Get("code_challenge") {
		t.Fatalf("the GitHub verifier hashes to %s, the challenge is %s", got, gq.Get("code_challenge"))
	}
}

// TestOAuthStartOnAnotherConsoleDomainIsRefused: the callback and its cookie belong to the login domain.
func TestOAuthStartOnAnotherConsoleDomainIsRefused(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	const secondConsole = "login2.main.example"
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`INSERT INTO accounts.site_domains VALUES ('`+secondConsole+`', :s1, 'console', 'https://`+secondConsole+`', NULL)`)); err != nil {
		t.Fatal(err)
	}
	s := oauthService(t, x.db, egress.NewForTesting(egresstest.Loopback(t)))
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		return s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{LoginDomain: &domain}, "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return s.SetSiteOAuthProvider(t.Context(), tx, system, accounts.SiteID(s1), "github", "client-id", "client-secret", "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartOAuth(t.Context(), secondConsole, "github", nil, client); !errors.Is(err, accounts.ErrWrongLoginDomain) {
		t.Fatalf("another console domain: %v", err)
	}
}

// TestSetSiteSignInRefusesADomainWithAssets: passkeys bind to the login domain, so it cannot move under them.
func TestSetSiteSignInRefusesADomainWithAssets(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	const secondConsole = "login3.main.example"
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`INSERT INTO accounts.site_domains VALUES ('`+secondConsole+`', :s1, 'console', 'https://`+secondConsole+`', NULL)`)); err != nil {
		t.Fatal(err)
	}
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		return x.s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{LoginDomain: &domain}, "test")
	}); err != nil {
		t.Fatal(err)
	}
	// A linked identity is one of the assets the change would strand.
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`INSERT INTO accounts.user_identities (id, site_id, account_id, user_id, provider, subject, display_name) VALUES
		(gen_random_uuid(), :s1, :a1, :u1, 'github', '42', 'Octo')`)); err != nil {
		t.Fatal(err)
	}
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := secondConsole
		return x.s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{LoginDomain: &domain}, "test")
	}); !errors.Is(err, accounts.ErrLoginDomainLocked) {
		t.Fatalf("another domain with assets: %v", err)
	}
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := "not-a-domain.example"
		return x.s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{LoginDomain: &domain}, "test")
	}); err == nil {
		t.Fatal("a domain the site does not serve was accepted")
	}
}

// TestSetLoginDomainAfterProvider: an application stored before the site has a login domain does not prevent choosing
// the first domain; only moving an existing one is locked.
func TestSetLoginDomainAfterProvider(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return x.s.SetSiteOAuthProvider(t.Context(), tx, system, accounts.SiteID(s1), "github", "client-id", "client-secret", "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		return x.s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{LoginDomain: &domain}, "test")
	}); err != nil {
		t.Fatalf("the first login domain after a provider: %v", err)
	}
}
