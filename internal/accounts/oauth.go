package accounts

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ErrOAuthNotConfigured is a provider or a login domain the site has not set up: third-party sign-in is off until its
// application credentials are stored.
var ErrOAuthNotConfigured = errors.New("accounts: third-party sign-in is not configured")

// ErrWrongLoginDomain is a sign-in ceremony asked for on a console domain that is not the site's login domain. The
// callback and the session cookie belong to that one domain, so the page must send the visitor there.
var ErrWrongLoginDomain = errors.New("accounts: use the site's login domain")

// ErrAccountNotLinked is a provider account no local user has linked. Axiom never creates a user or attaches the
// identity to one by its email address (docs/domains/accounts.md「不自动合并身份」).
var ErrAccountNotLinked = errors.New("accounts: this provider account is not linked")

// ErrIdentityLinked is a provider account already linked to another user of the site.
var ErrIdentityLinked = errors.New("accounts: this provider account is linked to another user")

// ErrIdentityAlreadyLinked is a different account of a provider this user has already linked: a user has one identity
// per provider, so the old one must be unlinked first.
var ErrIdentityAlreadyLinked = errors.New("accounts: this account already has an identity for this provider")

// ErrMFARequired is a credential change from a session that has not passed the second factor the user has.
var ErrMFARequired = errors.New("accounts: this needs a session that passed two-step verification")

// ErrOAuthFailed is a round trip the provider did not complete: a bad code, an unreachable endpoint or a response that
// is not the shape the provider documents. The caller may start again.
var ErrOAuthFailed = errors.New("accounts: the provider did not complete the sign-in")

// ErrPasskeyFailed is a WebAuthn ceremony a client did not complete correctly, including a credential whose signature
// counter went backwards.
var ErrPasskeyFailed = errors.New("accounts: the passkey ceremony failed")

// oauthProvider is one provider's fixed protocol endpoints. They are constants, never request input, and tests replace
// the whole entry to point at a fake server.
type oauthProvider struct {
	authorize, token string
	userinfo         string
	scope            string
	pkce             bool // whether the provider's authorization endpoint accepts PKCE
}

// oauthProviders are the three built-in providers. All three authorization endpoints take PKCE S256; GitHub's OAuth
// apps gained it in July 2025.
var oauthProviders = map[string]oauthProvider{
	"github": {
		authorize: "https://github.com/login/oauth/authorize",
		token:     "https://github.com/login/oauth/access_token", userinfo: "https://api.github.com/user",
		scope: "read:user", pkce: true,
	},
	"google": {
		authorize: "https://accounts.google.com/o/oauth2/v2/auth",
		token:     "https://oauth2.googleapis.com/token", userinfo: "https://openidconnect.googleapis.com/v1/userinfo",
		scope: "openid email profile", pkce: true,
	},
	"linuxdo": {
		authorize: "https://connect.linux.do/oauth2/authorize",
		token:     "https://connect.linux.do/oauth2/token", userinfo: "https://connect.linux.do/api/user",
		scope: "user", pkce: true,
	},
}

// ProviderNames are the providers a site may configure, in the order its page lists them.
var ProviderNames = []string{"github", "google", "linuxdo"}

// oauthTimeout bounds one provider call; the two calls of a round trip share it.
const oauthTimeout = 10 * time.Second

// oauthIdentity is what a provider says about the signed-in account: its permanent subject, which is the only value
// ever matched, and a display name for the account page.
type oauthIdentity struct {
	subject string
	display string
}

// OAuthStart is the authorization URL a page sends the browser to and, for a sign-in, the nonce the page must keep in
// a cookie until the callback: state alone would let whoever holds a callback URL sign a browser in to another account.
type OAuthStart struct {
	URL   string
	Nonce string
}

// OAuthCallback is the outcome of a provider round trip: the session to set (nil when a link changed nothing) and what
// happened, which the page uses to choose where to go.
type OAuthCallback struct {
	Issued   *Issued
	Provider string
	Linked   bool
}

var oauthLocation = secret.Location{Table: "accounts.site_oauth_providers", RowID: "_", Purpose: "client_secret"}

func oauthSecretLocation(id uuid.UUID) secret.Location {
	loc := oauthLocation
	loc.RowID = id.String()
	return loc
}

// StartOAuth begins a provider round trip on the site's login domain. A sign-in flow carries no session; a link flow
// is bound to p's user and session and is refused from a session that has not passed the second factor p's user has,
// because attaching a new way in is a credential change.
func (s *Service) StartOAuth(ctx context.Context, host, provider string, p *Principal, ip netip.Addr) (OAuthStart, error) {
	site, err := s.loginDomainSite(host)
	if err != nil {
		return OAuthStart{}, err
	}
	if err := s.fromIP(ctx, site.ID, ip); err != nil {
		return OAuthStart{}, err
	}
	protocol, known := s.providers[provider]
	if !known {
		return OAuthStart{}, ErrOAuthNotConfigured
	}
	clientID, ok := site.OAuth[provider]
	if !ok {
		return OAuthStart{}, ErrOAuthNotConfigured
	}
	flow := oauthFlow{
		site: site.ID, provider: provider, intent: "sign_in",
		state: randomToken(), verifier: randomToken(), nonce: randomToken(),
		redirect: site.LoginOrigin + "/api/console/v1/auth/oauth/" + provider + "/callback",
	}
	if p != nil {
		if p.Pending() {
			return OAuthStart{}, ErrMFARequired
		}
		if err := s.requireVerifiedForChange(ctx, *p); err != nil {
			return OAuthStart{}, err
		}
		flow.intent, flow.account, flow.user, flow.session = "link", p.account, p.user, p.session
		flow.nonce = ""
	}
	if err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		stateHash := sha256.Sum256([]byte(flow.state))
		return s.queries.InsertOAuthFlow(ctx, tx, InsertOAuthFlowParams{
			ID: uuid.NewV7(), SiteID: uuid.UUID(site.ID), Provider: provider, Intent: flow.intent,
			AccountID: optionalUUID(flow.account), UserID: optionalUUID(flow.user), SessionID: optionalUUID(flow.session),
			StateHash: stateHash[:], NonceHash: tokenHash(flow.nonce), Verifier: flow.verifier, RedirectUri: flow.redirect,
		})
	}); err != nil {
		return OAuthStart{}, fmt.Errorf("begin third-party sign-in: %w", err)
	}
	params := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {flow.redirect},
		"state": {flow.state}, "scope": {protocol.scope},
	}
	if protocol.pkce {
		params.Set("code_challenge", pkceChallenge(flow.verifier))
		params.Set("code_challenge_method", "S256")
	}
	return OAuthStart{URL: protocol.authorize + "?" + params.Encode(), Nonce: flow.nonce}, nil
}

type oauthFlow struct {
	site     SiteID
	provider string
	intent   string
	state    string
	nonce    string
	verifier string
	redirect string
	account  AccountID
	user     UserID
	session  SessionID
}

// CompleteOAuth finishes a provider round trip. The state is single use and bound to the site and the signed-in
// session that began a link; the code is exchanged with the verifier the flow kept. A sign-in matches the provider's
// subject against linked identities only, and a link attaches it to the session's user and raises the factor version,
// which signs every other session out.
func (s *Service) CompleteOAuth(ctx context.Context, r *http.Request, provider, code, state string, ip netip.Addr, userAgent string) (OAuthCallback, error) {
	site, err := s.loginDomainSite(r.Host)
	if err != nil {
		return OAuthCallback{}, err
	}
	if err := s.fromIP(ctx, site.ID, ip); err != nil {
		return OAuthCallback{}, err
	}
	if code == "" || state == "" {
		return OAuthCallback{}, ErrOAuthFailed
	}
	stateHash := sha256.Sum256([]byte(state))
	var flows []FindOAuthFlowRow
	if err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		var err error
		flows, err = s.queries.FindOAuthFlow(ctx, tx, FindOAuthFlowParams{SiteID: uuid.UUID(site.ID), StateHash: stateHash[:]})
		return err
	}); err != nil {
		return OAuthCallback{}, fmt.Errorf("find the sign-in flow: %w", err)
	}
	if len(flows) == 0 || !flows[0].Unexpired || flows[0].Provider != provider {
		return OAuthCallback{}, ErrOAuthFailed
	}
	flow := flows[0]
	if flow.Intent == "sign_in" {
		// The callback must arrive in the browser that began the sign-in: the nonce cookie, not the URL, proves it.
		// Without this a completed callback could be handed to another browser and sign it in to the attacker.
		cookie, err := r.Cookie(oauthNonceName(site.LoginOrigin))
		if err != nil || len(flow.NonceHash) == 0 || subtle.ConstantTimeCompare(tokenHash(cookie.Value), flow.NonceHash) != 1 {
			return OAuthCallback{}, ErrOAuthFailed
		}
	}
	var consumed int64
	if err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		var err error
		consumed, err = s.queries.ConsumeOAuthFlow(ctx, tx, flow.ID)
		return err
	}); err != nil || consumed != 1 {
		return OAuthCallback{}, fmt.Errorf("consume the sign-in flow: %w", errors.Join(err, ErrOAuthFailed))
	}
	var principal *Principal
	if flow.Intent == "link" {
		// The callback must arrive in the browser that began the link and from the same session. An identity cannot be
		// attached to a user by whoever holds a leaked state.
		p, err := s.AuthenticateAny(ctx, r, ip)
		if err != nil {
			return OAuthCallback{}, err
		}
		if p.Pending() {
			return OAuthCallback{}, ErrMFARequired
		}
		if flow.UserID == nil || flow.SessionID == nil || uuid.UUID(p.user) != *flow.UserID ||
			uuid.UUID(p.session) != *flow.SessionID {
			return OAuthCallback{}, ErrOAuthFailed
		}
		principal = &p
	}
	clientID, secret, err := s.oauthSecret(ctx, site.ID, provider)
	if err != nil {
		return OAuthCallback{}, err
	}
	identity, err := s.providerIdentity(ctx, provider, clientID, secret, code, flow.Verifier, flow.RedirectUri)
	if err != nil {
		return OAuthCallback{}, err
	}
	// The columns bound the subject and the display name; a provider that answers outside them is not one this site
	// can link. A provider without a display name falls back to its own name rather than failing the link.
	if len(identity.subject) == 0 || len(identity.subject) > 255 {
		return OAuthCallback{}, ErrOAuthFailed
	}
	if identity.display = strings.TrimSpace(identity.display); identity.display == "" {
		identity.display = provider
	}
	if flow.Intent == "link" {
		// A subject linked to another user is refused whatever its email says; a subject already linked to this user is
		// a successful no-op, so a double callback does not raise the factor version twice.
		var existing []FindIdentityRow
		if err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
			var err error
			existing, err = s.queries.FindIdentity(ctx, tx, FindIdentityParams{SiteID: uuid.UUID(site.ID), Provider: provider, Subject: identity.subject})
			return err
		}); err != nil {
			return OAuthCallback{}, fmt.Errorf("find the linked identity: %w", err)
		}
		if len(existing) > 0 {
			if existing[0].UserID != uuid.UUID(principal.user) {
				return OAuthCallback{}, ErrIdentityLinked
			}
			return OAuthCallback{Provider: provider, Linked: true}, nil
		}
		issued, err := s.linkIdentity(ctx, *principal, site, provider, identity, ip, userAgent)
		if err != nil {
			return OAuthCallback{}, err
		}
		return OAuthCallback{Issued: &issued, Provider: provider, Linked: true}, nil
	}
	issued, err := s.signInWithIdentity(ctx, site, provider, identity, ip, userAgent)
	if err != nil {
		return OAuthCallback{}, err
	}
	return OAuthCallback{Issued: &issued, Provider: provider}, nil
}

// oauthSecret opens a site's provider application in the site's transaction.
func (s *Service) oauthSecret(ctx context.Context, site SiteID, provider string) (string, secret.Secret, error) {
	if _, ok := s.providers[provider]; !ok {
		return "", secret.Secret{}, ErrOAuthNotConfigured
	}
	var row ReadSiteOAuthProviderRow
	err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site)), func(tx pg.Tx) error {
		var err error
		row, err = s.queries.ReadSiteOAuthProvider(ctx, tx, ReadSiteOAuthProviderParams{SiteID: uuid.UUID(site), Provider: provider})
		return err
	})
	if errors.Is(err, pg.ErrNoRows) {
		return "", secret.Secret{}, ErrOAuthNotConfigured
	}
	if err != nil {
		return "", secret.Secret{}, fmt.Errorf("read the provider application: %w", err)
	}
	opened, err := s.keyring.Open(secret.Sealed{
		Ciphertext: row.ClientSecretCiphertext, Nonce: row.ClientSecretNonce,
		WrappedDEK: row.ClientSecretWrappedDek, KeyVersion: uint32(row.ClientSecretKeyVersion), //nolint:gosec // the column holds 1 to 2³²-1
	}, oauthSecretLocation(row.ID))
	if err != nil {
		return "", secret.Secret{}, fmt.Errorf("open the provider secret: %w", err)
	}
	return row.ClientID, opened, nil
}

// providerIdentity exchanges the code and asks who signed in. Every request goes through the module's outbound client
// with a deadline; the response body is bounded because it comes from a third party.
func (s *Service) providerIdentity(ctx context.Context, provider, clientID string, clientSecret secret.Secret, code, verifier, redirect string) (oauthIdentity, error) {
	p := s.providers[provider]
	ctx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {clientID}, "client_secret": {clientSecret.Reveal()},
	}
	if p.pkce {
		form.Set("code_verifier", verifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.token, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthIdentity{}, fmt.Errorf("exchange the authorization code: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := s.oauthJSON(req, &token); err != nil {
		return oauthIdentity{}, err
	}
	if token.AccessToken == "" {
		return oauthIdentity{}, ErrOAuthFailed
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, p.userinfo, nil)
	if err != nil {
		return oauthIdentity{}, fmt.Errorf("read the provider account: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	switch provider {
	case "github":
		var user struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Name  string `json:"name"`
		}
		if err := s.oauthJSON(req, &user); err != nil {
			return oauthIdentity{}, err
		}
		if user.ID == 0 || user.Login == "" {
			return oauthIdentity{}, ErrOAuthFailed
		}
		return oauthIdentity{subject: fmt.Sprint(user.ID), display: firstText(user.Name, user.Login)}, nil
	case "google":
		var user struct {
			Subject string `json:"sub"`
			Name    string `json:"name"`
			Email   string `json:"email"`
		}
		if err := s.oauthJSON(req, &user); err != nil {
			return oauthIdentity{}, err
		}
		if user.Subject == "" {
			return oauthIdentity{}, ErrOAuthFailed
		}
		return oauthIdentity{subject: user.Subject, display: firstText(user.Name, user.Email)}, nil
	case "linuxdo":
		var user struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Name     string `json:"name"`
		}
		if err := s.oauthJSON(req, &user); err != nil {
			return oauthIdentity{}, err
		}
		if user.ID == 0 {
			return oauthIdentity{}, ErrOAuthFailed
		}
		return oauthIdentity{subject: fmt.Sprint(user.ID), display: firstText(user.Name, user.Username)}, nil
	default:
		return oauthIdentity{}, ErrOAuthNotConfigured
	}
}

// oauthJSON runs req and decodes a bounded JSON body. The provider's error text is not repeated: it can hold the code.
func (s *Service) oauthJSON(req *http.Request, into any) error {
	resp, err := s.exit.Do(req)
	if err != nil {
		return fmt.Errorf("call the provider: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: provider answered %d", ErrOAuthFailed, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("read the provider response: %w", err)
	}
	if err := jsonx.Unmarshal(body, into); err != nil {
		return fmt.Errorf("%w: %w", ErrOAuthFailed, err)
	}
	return nil
}

// signInWithIdentity issues a session for the user a linked identity names. A user with a second factor gets a pending
// session: a third-party sign-in is one factor, however strongly the provider authenticated it.
func (s *Service) signInWithIdentity(ctx context.Context, site Site, provider string, identity oauthIdentity, ip netip.Addr, userAgent string) (Issued, error) {
	var found []FindIdentityRow
	if err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		var err error
		found, err = s.queries.FindIdentity(ctx, tx, FindIdentityParams{SiteID: uuid.UUID(site.ID), Provider: provider, Subject: identity.subject})
		return err
	}); err != nil {
		return Issued{}, fmt.Errorf("find the linked identity: %w", err)
	}
	if len(found) == 0 {
		return Issued{}, ErrAccountNotLinked
	}
	user, account := found[0].UserID, found[0].AccountID
	var issued Issued
	err := s.db.Tx(ctx, pg.AccountScope(account), func(tx pg.Tx) error {
		rows, err := s.queries.UserForWebAuthn(ctx, tx, user)
		if err != nil {
			return err
		}
		if !rows.Active {
			return ErrBadCredentials
		}
		locked, err := s.lockUser(ctx, tx, user, rows.FactorVersion)
		if err != nil {
			return err
		}
		level := levelPassword
		if locked.secondFactor() {
			level = levelPending
		}
		issued, err = s.issue(ctx, tx, newSession{
			user: user, account: account, site: site, surface: Console, level: level,
			factorVersion: locked.FactorVersion, ip: ip, userAgent: userAgent,
		})
		if err != nil {
			return err
		}
		return (securityAttempt{site: site.ID, user: user, account: account, session: issued.session, surface: Console}).record(ctx, tx, "security.sign_in")
	})
	if err != nil {
		return Issued{}, err
	}
	return issued, nil
}

// linkIdentity attaches the provider account to the signed-in user and raises the factor version, which signs every
// other session out; this browser gets a new session with the same level so the person stays signed in.
func (s *Service) linkIdentity(ctx context.Context, p Principal, site Site, provider string, identity oauthIdentity, ip netip.Addr, userAgent string) (Issued, error) {
	var issued Issued
	err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		locked, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion)
		if err != nil {
			return err
		}
		if (locked.Mfa || (locked.Passkeys && p.passkeyFactor())) && !p.Verified() {
			return ErrMFARequired
		}
		if err := s.queries.InsertIdentity(ctx, tx, InsertIdentityParams{
			ID: uuid.NewV7(), SiteID: uuid.UUID(site.ID), AccountID: uuid.UUID(p.account), UserID: uuid.UUID(p.user),
			Provider: provider, Subject: identity.subject, DisplayName: truncate(identity.display, 100),
		}); err != nil {
			if constraint, duplicate := pg.Duplicate(err); duplicate {
				if constraint == "user_identities_user_id_provider_key" {
					return ErrIdentityAlreadyLinked
				}
				return ErrIdentityLinked
			}
			return err
		}
		if err := s.queries.BumpFactorVersion(ctx, tx, uuid.UUID(p.user)); err != nil {
			return err
		}
		level := levelPassword
		if p.Verified() {
			level = levelMFA
		}
		issued, err = s.issue(ctx, tx, newSession{
			user: uuid.UUID(p.user), account: uuid.UUID(p.account), site: site, surface: Console, level: level,
			factorVersion: locked.FactorVersion + 1, ip: ip, userAgent: userAgent,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{
			Actor: audit.User(uuid.UUID(p.user)), Action: "user.identity_link",
			Target: audit.Target{Module: "accounts", Kind: "user", ID: p.user.TypeID(), Site: uuid.UUID(site.ID), Account: uuid.UUID(p.account)},
			Detail: map[string]any{"provider": provider, "subject": identity.subject},
		})
	})
	if err != nil {
		return Issued{}, err
	}
	return issued, nil
}

// UnlinkIdentity detaches a provider account from the signed-in user. It is a credential change: the user's factor
// version rises, every other session ends, and this browser gets a new session with the same level.
func (s *Service) UnlinkIdentity(ctx context.Context, p Principal, provider string, ip netip.Addr, userAgent string) (Issued, error) {
	if _, ok := s.providers[provider]; !ok {
		return Issued{}, ErrNotFound
	}
	if p.Pending() {
		return Issued{}, ErrMFARequired
	}
	if err := s.requireVerifiedForChange(ctx, p); err != nil {
		return Issued{}, err
	}
	var issued Issued
	err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		locked, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion)
		if err != nil {
			return err
		}
		changed, err := s.queries.DeleteIdentity(ctx, tx, DeleteIdentityParams{UserID: uuid.UUID(p.user), Provider: provider})
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrNotFound
		}
		if err := s.queries.BumpFactorVersion(ctx, tx, uuid.UUID(p.user)); err != nil {
			return err
		}
		level := levelPassword
		if p.Verified() {
			level = levelMFA
		}
		issued, err = s.issue(ctx, tx, newSession{
			user: uuid.UUID(p.user), account: uuid.UUID(p.account), site: s.snapshot.Load().sites[p.site], surface: p.surface,
			level: level, factorVersion: locked.FactorVersion + 1, ip: ip, userAgent: userAgent,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{
			Actor: audit.User(uuid.UUID(p.user)), Action: "user.identity_unlink",
			Target: audit.Target{Module: "accounts", Kind: "user", ID: p.user.TypeID(), Site: uuid.UUID(p.site), Account: uuid.UUID(p.account)},
			Detail: map[string]any{"provider": provider},
		})
	})
	if err != nil {
		return Issued{}, err
	}
	return issued, nil
}

// Identity is one provider account linked to a user, as the account page lists it.
type Identity struct {
	Provider    string
	DisplayName string
	CreatedAt   time.Time
}

// Identities lists a user's linked provider accounts.
func (s *Service) Identities(ctx context.Context, p Principal) ([]Identity, error) {
	var rows []ListIdentitiesRow
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		rows, err = s.queries.ListIdentities(ctx, tx, uuid.UUID(p.user))
		return err
	}); err != nil {
		return nil, fmt.Errorf("list linked identities: %w", err)
	}
	out := make([]Identity, 0, len(rows))
	for _, r := range rows {
		out = append(out, Identity{Provider: r.Provider, DisplayName: r.DisplayName, CreatedAt: r.CreatedAt.Time})
	}
	return out, nil
}

// OAuthNonceCookie is the browser binding of a sign-in a page started: HttpOnly, Lax (the provider's redirect is a
// top-level GET), and a __Host- cookie on every https origin. The callback checks it before it spends the state.
func (s *Service) OAuthNonceCookie(host, nonce string) (*http.Cookie, error) {
	d, _, err := s.domain(host)
	if err != nil {
		return nil, err
	}
	return &http.Cookie{ // #nosec G124 -- Secure for every https origin; only a registered plain-http localhost origin goes without
		Name: oauthNonceName(d.origin), Value: nonce, Path: "/", MaxAge: 600,
		HttpOnly: true, Secure: strings.HasPrefix(d.origin, "https://"), SameSite: http.SameSiteLaxMode,
	}, nil
}

func oauthNonceName(origin string) string {
	if strings.HasPrefix(origin, "https://") {
		return "__Host-axiom_oauth"
	}
	return "axiom_oauth"
}

// tokenHash is the SHA-256 a one-time token is stored as; empty means absent.
func tokenHash(token string) []byte {
	if token == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// loginDomainSite is the console site of host, which must be its login domain: a WebAuthn relying party and an OAuth
// callback both belong to exactly one origin.
func (s *Service) loginDomainSite(host string) (Site, error) {
	d, snap, err := s.domain(host)
	if err != nil {
		return Site{}, err
	}
	if d.surface != Console {
		return Site{}, ErrUnknownHost
	}
	site := snap.sites[d.site]
	if site.LoginDomain == "" || site.LoginOrigin == "" {
		return Site{}, ErrOAuthNotConfigured
	}
	if !strings.EqualFold(host, site.LoginDomain) {
		return Site{}, ErrWrongLoginDomain
	}
	return site, nil
}

// requireVerifiedForChange refuses a credential change from a password-only session when the user has a second factor.
func (s *Service) requireVerifiedForChange(ctx context.Context, p Principal) error {
	var locked LockUserRow
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		rows, err := s.queries.UserForWebAuthn(ctx, tx, uuid.UUID(p.user))
		if err != nil {
			return err
		}
		locked, err = s.lockUser(ctx, tx, uuid.UUID(p.user), rows.FactorVersion)
		return err
	}); err != nil {
		return err
	}
	if (locked.Mfa || (locked.Passkeys && p.passkeyFactor())) && !p.Verified() {
		return ErrMFARequired
	}
	return nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func optionalUUID[T ~[16]byte](id T) *uuid.UUID {
	if id == (T{}) {
		return nil
	}
	u := uuid.UUID(id)
	return &u
}

func firstText(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
