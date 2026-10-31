package accounts_test

import (
	"crypto/sha256"
	"encoding/base64"
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
	"axiom/internal/platform/jsonx"
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
	server := httptest.NewTLSServer(f.handler(t))
	t.Cleanup(server.Close)
	return f, server
}

// handler routes the provider's three endpoints.
func (f *fakeOAuth) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			http.Redirect(w, r, "/token", http.StatusFound)
		case "/token":
			f.token(t, w, r)
		case "/user":
			f.user(t, w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// token answers the token request, or refuses when deny is set.
func (f *fakeOAuth) token(t *testing.T, w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.gotForm.Store(r.PostForm)
	if f.deny.Load() {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if _, err := w.Write([]byte(`{"access_token":"the-token","token_type":"bearer"}`)); err != nil {
		t.Errorf("write the token response: %v", err)
	}
}

// user answers the userinfo request with the configured subject and email.
func (f *fakeOAuth) user(t *testing.T, w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer the-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, err := jsonx.MarshalCanonical(map[string]any{
		"id": f.subject.Load(), "sub": fmt.Sprint(f.subject.Load()), "login": "octocat",
		"name": "The Octocat", "email": f.email.Load(),
	})
	if err != nil {
		t.Errorf("encode the provider account: %v", err)
		return
	}
	if _, err := w.Write(body); err != nil {
		t.Errorf("write the provider account: %v", err)
	}
}

// configureOAuth points GitHub's endpoints at the fake server.
func configureOAuth(t *testing.T, s *accounts.Service, server *httptest.Server) {
	t.Helper()
	s.SetOAuthEndpointsForTesting("github", server.URL+"/authorize", server.URL+"/token", server.URL+"/user")
}

// oauthFixture is a site with a fake provider, and a node whose provider calls reach it.
type oauthFixture struct {
	x      site
	s      *accounts.Service
	fake   *fakeOAuth
	server *httptest.Server
}

func newOAuthFixture(t *testing.T, subject int64) oauthFixture {
	t.Helper()
	x := newTestSite(t)
	fake, server := newFakeOAuth(t, subject, x.email)
	exit := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	t.Cleanup(exit.CloseIdleConnections)
	s := oauthService(t, x.db, exit)
	configureOAuth(t, s, server)
	return oauthFixture{x: x, s: s, fake: fake, server: server}
}

// oauthSite turns on the site's login domain and stores one provider application, then reloads the snapshot.
func oauthSite(t *testing.T, x site, s *accounts.Service, provider string) {
	t.Helper()
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		if err := s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{LoginDomain: &domain}, "test"); err != nil {
			return err
		}
		return s.SetSiteOAuthProvider(t.Context(), tx, system, accounts.SiteID(s1), provider, "client-id", "client-secret", "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// startURL runs a start and returns its state and nonce.
func startURL(t *testing.T, s *accounts.Service, p *accounts.Principal) (string, string) {
	t.Helper()
	start, err := s.StartOAuth(t.Context(), console, "github", p, client)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state"), start.Nonce
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

// cookieRequest is a console GET carrying issued's session cookie, to authenticate what a callback issued.
func cookieRequest(t *testing.T, s *accounts.Service, issued accounts.Issued) *http.Request {
	t.Helper()
	cookie, err := s.Cookie(console, issued)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+console+"/", nil)
	r.AddCookie(cookie)
	return r
}

// pkceAuthorization starts a provider sign-in, checks the authorization URL carries an S256 challenge, and checks the
// token request carried the verifier that hashes to it.
func pkceAuthorization(t *testing.T, s *accounts.Service, fake *fakeOAuth, provider string) {
	t.Helper()
	start, err := s.StartOAuth(t.Context(), console, provider, nil, client)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	challenge := query.Get("code_challenge")
	if query.Get("code_challenge_method") != "S256" || challenge == "" {
		t.Fatalf("%s PKCE in the authorization URL: %s", provider, start.URL)
	}
	state := query.Get("state")
	if _, err := s.CompleteOAuth(t.Context(), callback(t, s, state, start.Nonce, nil), provider, "the-code", state, client, "test"); !errors.Is(err, accounts.ErrAccountNotLinked) {
		t.Fatalf("a %s subject no one linked: %v", provider, err)
	}
	form, ok := fake.gotForm.Load().(url.Values)
	if !ok {
		t.Fatal("the token request was not recorded")
	}
	verifier := form.Get("code_verifier")
	if verifier == "" {
		t.Fatalf("the %s token request has no code_verifier: %v", provider, form)
	}
	sum := sha256.Sum256([]byte(verifier))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != challenge {
		t.Fatalf("the %s verifier hashes to %s, the challenge is %s", provider, got, challenge)
	}
}

// linkFirstIdentity signs the user in with a password and attaches the provider account in the same browser.
func linkFirstIdentity(t *testing.T, f oauthFixture) {
	t.Helper()
	passwordSession := f.x.signIn(t, console)
	p, err := f.s.Authenticate(t.Context(), f.x.request(t, console, "https://"+console, passwordSession), client)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := startURL(t, f.s, &p)
	out, err := f.s.CompleteOAuth(t.Context(), callback(t, f.s, state, "", &passwordSession), "github", "the-code", state, client, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Linked || out.Issued == nil {
		t.Fatalf("link: %+v", out)
	}
	if got := texts(t, f.x.db, platform, "SELECT provider || ' ' || subject || ' ' || display_name FROM accounts.user_identities"); len(got) != 1 || got[0] != "github 42 The Octocat" {
		t.Fatalf("identity: %v", got)
	}
	if got := texts(t, f.x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'user.identity_link'"); got[0] != "1" {
		t.Errorf("link audits: %v", got)
	}
	if p, err := f.s.Authenticate(t.Context(), cookieRequest(t, f.s, *out.Issued), client); err != nil || p.UserID() != f.x.user {
		t.Errorf("the session is the linked user's: %v %v", p, err)
	}
}

// assertOneIdentityPerProvider checks that a second account of the same provider is refused and says so.
func assertOneIdentityPerProvider(t *testing.T, f oauthFixture) {
	t.Helper()
	fresh := f.x.signIn(t, console)
	p, err := f.s.Authenticate(t.Context(), f.x.request(t, console, "https://"+console, fresh), client)
	if err != nil {
		t.Fatal(err)
	}
	f.fake.subject.Store(7)
	state, _ := startURL(t, f.s, &p)
	if _, err := f.s.CompleteOAuth(t.Context(), callback(t, f.s, state, "", &fresh), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrIdentityAlreadyLinked) {
		t.Errorf("a second account of the same provider: %v", err)
	}
}

// assertSignsInBySubject checks that the linked user signs in from the provider subject alone, and that the callback
// must return in the browser that began the flow.
func assertSignsInBySubject(t *testing.T, f oauthFixture) {
	t.Helper()
	// The email the provider reports is a different one and is deliberately not the user's.
	f.fake.subject.Store(42)
	f.fake.email.Store("someone-else@example.com")
	state, nonce := startURL(t, f.s, nil)
	// Without the nonce cookie, or with a different one, the state is not spent and no session is issued.
	if _, err := f.s.CompleteOAuth(t.Context(), callback(t, f.s, state, "", nil), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrOAuthFailed) {
		t.Errorf("a callback without the nonce cookie: %v", err)
	}
	if _, err := f.s.CompleteOAuth(t.Context(), callback(t, f.s, state, "another-browser", nil), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrOAuthFailed) {
		t.Errorf("a callback with another browser's nonce: %v", err)
	}
	out, err := f.s.CompleteOAuth(t.Context(), callback(t, f.s, state, nonce, nil), "github", "the-code", state, client, "test")
	if err != nil || out.Issued == nil || out.Issued.Pending {
		t.Fatalf("sign-in: %+v %v", out, err)
	}
	if p, err := f.s.Authenticate(t.Context(), cookieRequest(t, f.s, *out.Issued), client); err != nil || p.UserID() != f.x.user {
		t.Errorf("the session is the linked user's: %v %v", p, err)
	}
}

// assertNoMergeByEmail checks that an unknown subject is not attached by email and that a spent state changes nothing.
func assertNoMergeByEmail(t *testing.T, f oauthFixture, usersBefore string) {
	t.Helper()
	f.fake.subject.Store(99)
	f.fake.email.Store(f.x.email)
	state, nonce := startURL(t, f.s, nil)
	if _, err := f.s.CompleteOAuth(t.Context(), callback(t, f.s, state, nonce, nil), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrAccountNotLinked) {
		t.Errorf("unknown subject: %v", err)
	}
	if _, err := f.s.CompleteOAuth(t.Context(), callback(t, f.s, state, nonce, nil), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrOAuthFailed) {
		t.Errorf("replayed state: %v", err)
	}
	if got := texts(t, f.x.db, platform, "SELECT count(*)::text FROM accounts.users")[0]; got != usersBefore {
		t.Errorf("users: %v, want the %v before the flows", got, usersBefore)
	}
	if got := texts(t, f.x.db, platform, "SELECT count(*)::text FROM accounts.user_identities"); got[0] != "1" {
		t.Errorf("an unlinked provider account was attached by email: %v", got)
	}
}

// TestOAuthLinksOnlyWhileSignedInAndSignsInBySubject: a provider account attaches to the signed-in user, a later
// sign-in finds that user by the provider's subject, and an unknown subject is not registered even when its email is a
// user's.
func TestOAuthLinksOnlyWhileSignedInAndSignsInBySubject(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t, 42)
	usersBefore := texts(t, f.x.db, platform, "SELECT count(*)::text FROM accounts.users")[0]
	oauthSite(t, f.x, f.s, "github")
	linkFirstIdentity(t, f)
	t.Run("one identity per provider", func(t *testing.T) { assertOneIdentityPerProvider(t, f) })
	t.Run("signs in by subject", func(t *testing.T) { assertSignsInBySubject(t, f) })
	t.Run("does not merge by email", func(t *testing.T) { assertNoMergeByEmail(t, f, usersBefore) })
}

// TestOAuthLinkNeedsTheSameSession: a callback without the session that began a link attaches nothing.
func TestOAuthLinkNeedsTheSameSession(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t, 7)
	oauthSite(t, f.x, f.s, "github")
	session := f.x.signIn(t, console)
	p, err := f.s.Authenticate(t.Context(), f.x.request(t, console, "https://"+console, session), client)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := startURL(t, f.s, &p)
	if _, err := f.s.CompleteOAuth(t.Context(), callback(t, f.s, state, "", nil), "github", "the-code", state, client, "test"); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("a link without its session: %v", err)
	}
	if got := texts(t, f.x.db, platform, "SELECT count(*)::text FROM accounts.user_identities"); got[0] != "0" {
		t.Errorf("identities: %v", got)
	}
}

// TestOAuthStartUsesPKCE: Google's and GitHub's authorization URLs carry an S256 challenge and the token request the
// matching verifier. GitHub OAuth Apps accept PKCE since July 2025.
func TestOAuthStartUsesPKCE(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t, 1)
	f.s.SetOAuthEndpointsForTesting("google", f.server.URL+"/authorize", f.server.URL+"/token", f.server.URL+"/user")
	oauthSite(t, f.x, f.s, "google")
	pkceAuthorization(t, f.s, f.fake, "google")
	// GitHub has its own endpoints, already pointed at the fake by configureOAuth; give it an application and repeat.
	oauthSite(t, f.x, f.s, "github")
	pkceAuthorization(t, f.s, f.fake, "github")
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
	oauthSite(t, x, s, "github")
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
