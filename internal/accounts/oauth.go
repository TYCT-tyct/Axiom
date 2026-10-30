package accounts

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"uuid"

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

// ProviderNames are the providers a site may configure, in the order its page lists them.
var ProviderNames = []string{"github", "google", "linuxdo"}

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
	flow, err := s.consumeOAuthState(ctx, r, site, provider, state)
	if err != nil {
		return OAuthCallback{}, err
	}
	var principal *Principal
	if flow.Intent == "link" {
		p, err := s.oauthLinkPrincipal(ctx, r, flow, ip)
		if err != nil {
			return OAuthCallback{}, err
		}
		principal = &p
	}
	identity, err := s.oauthIdentityFor(ctx, site, provider, code, flow)
	if err != nil {
		return OAuthCallback{}, err
	}
	if flow.Intent == "link" {
		return s.finishOAuthLink(ctx, *principal, site, provider, identity, ip, userAgent)
	}
	issued, err := s.signInWithIdentity(ctx, site, provider, identity, ip, userAgent)
	if err != nil {
		return OAuthCallback{}, err
	}
	return OAuthCallback{Issued: &issued, Provider: provider}, nil
}

// consumeOAuthState finds the flow the state names, checks the browser binding of a sign-in, and spends the state.
func (s *Service) consumeOAuthState(ctx context.Context, r *http.Request, site Site, provider, state string) (FindOAuthFlowRow, error) {
	stateHash := sha256.Sum256([]byte(state))
	var flows []FindOAuthFlowRow
	if err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		var err error
		flows, err = s.queries.FindOAuthFlow(ctx, tx, FindOAuthFlowParams{SiteID: uuid.UUID(site.ID), StateHash: stateHash[:]})
		return err
	}); err != nil {
		return FindOAuthFlowRow{}, fmt.Errorf("find the sign-in flow: %w", err)
	}
	if len(flows) == 0 || !flows[0].Unexpired || flows[0].Provider != provider {
		return FindOAuthFlowRow{}, ErrOAuthFailed
	}
	flow := flows[0]
	if !oauthNonceMatches(r, site, flow) {
		return FindOAuthFlowRow{}, ErrOAuthFailed
	}
	var consumed int64
	if err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		var err error
		consumed, err = s.queries.ConsumeOAuthFlow(ctx, tx, flow.ID)
		return err
	}); err != nil || consumed != 1 {
		return FindOAuthFlowRow{}, fmt.Errorf("consume the sign-in flow: %w", errors.Join(err, ErrOAuthFailed))
	}
	return flow, nil
}

// oauthNonceMatches is the browser binding of a sign-in: the nonce cookie, not the URL, must be the one the flow was
// begun with. Without it a completed callback handed to another browser could sign that browser in.
func oauthNonceMatches(r *http.Request, site Site, flow FindOAuthFlowRow) bool {
	if flow.Intent != "sign_in" {
		return true
	}
	cookie, err := r.Cookie(oauthNonceName(site.LoginOrigin))
	return err == nil && len(flow.NonceHash) > 0 && subtle.ConstantTimeCompare(tokenHash(cookie.Value), flow.NonceHash) == 1
}

// oauthLinkPrincipal is the session a link callback must arrive in: the same user and session that began it, past the
// second factor the user has. An identity cannot be attached by whoever holds a leaked state.
func (s *Service) oauthLinkPrincipal(ctx context.Context, r *http.Request, flow FindOAuthFlowRow, ip netip.Addr) (Principal, error) {
	p, err := s.AuthenticateAny(ctx, r, ip)
	if err != nil {
		return Principal{}, err
	}
	if p.Pending() {
		return Principal{}, ErrMFARequired
	}
	if flow.UserID == nil || flow.SessionID == nil || uuid.UUID(p.user) != *flow.UserID ||
		uuid.UUID(p.session) != *flow.SessionID {
		return Principal{}, ErrOAuthFailed
	}
	return p, nil
}

// oauthIdentityFor opens the site's application and exchanges the code for who signed in. The columns bound the subject
// and the display name; a provider that answers outside them is not one this site can link. A provider without a
// display name falls back to its own name rather than failing the link.
func (s *Service) oauthIdentityFor(ctx context.Context, site Site, provider, code string, flow FindOAuthFlowRow) (oauthIdentity, error) {
	clientID, secret, err := s.oauthSecret(ctx, site.ID, provider)
	if err != nil {
		return oauthIdentity{}, err
	}
	identity, err := s.providerIdentity(ctx, provider, clientID, secret, code, flow.Verifier, flow.RedirectUri)
	if err != nil {
		return oauthIdentity{}, err
	}
	if len(identity.subject) == 0 || len(identity.subject) > 255 {
		return oauthIdentity{}, ErrOAuthFailed
	}
	if identity.display = strings.TrimSpace(identity.display); identity.display == "" {
		identity.display = provider
	}
	return identity, nil
}

// finishOAuthLink attaches the identity. A subject linked to another user is refused whatever its email says; a subject
// already linked to this user is a successful no-op, so a double callback does not raise the factor version twice.
func (s *Service) finishOAuthLink(ctx context.Context, p Principal, site Site, provider string, identity oauthIdentity, ip netip.Addr, userAgent string) (OAuthCallback, error) {
	existing, err := s.findIdentity(ctx, site, provider, identity.subject)
	if err != nil {
		return OAuthCallback{}, err
	}
	if len(existing) > 0 {
		if existing[0].UserID != uuid.UUID(p.user) {
			return OAuthCallback{}, ErrIdentityLinked
		}
		return OAuthCallback{Provider: provider, Linked: true}, nil
	}
	issued, err := s.linkIdentity(ctx, p, site, provider, identity, ip, userAgent)
	if err != nil {
		return OAuthCallback{}, err
	}
	return OAuthCallback{Issued: &issued, Provider: provider, Linked: true}, nil
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

// signInWithIdentity issues a session for the user a linked identity names. A user with a second factor gets a pending
// session: a third-party sign-in is one factor, however strongly the provider authenticated it.
func (s *Service) signInWithIdentity(ctx context.Context, site Site, provider string, identity oauthIdentity, ip netip.Addr, userAgent string) (Issued, error) {
	found, err := s.findIdentity(ctx, site, provider, identity.subject)
	if err != nil {
		return Issued{}, err
	}
	if len(found) == 0 {
		return Issued{}, ErrAccountNotLinked
	}
	var issued Issued
	err = s.db.Tx(ctx, pg.AccountScope(found[0].AccountID), func(tx pg.Tx) error {
		issued, err = s.identitySession(ctx, tx, site, provider, identity.subject, found[0], ip, userAgent)
		return err
	})
	if err != nil {
		return Issued{}, err
	}
	return issued, nil
}

// identitySession issues the session for a linked identity under the user's lock. The subject was read before this
// transaction; unlinking locks the user and raises the factor version, so this lock waited for it and the identity must
// still be this user's when the session is issued.
func (s *Service) identitySession(ctx context.Context, tx pg.Tx, site Site, provider, subject string, linked FindIdentityRow, ip netip.Addr, userAgent string) (Issued, error) {
	rows, err := s.queries.UserForWebAuthn(ctx, tx, linked.UserID)
	if err != nil {
		return Issued{}, err
	}
	if !rows.Active {
		return Issued{}, ErrBadCredentials
	}
	locked, err := s.lockUser(ctx, tx, linked.UserID, rows.FactorVersion)
	if err != nil {
		return Issued{}, err
	}
	held, err := s.identityHeld(ctx, tx, site, provider, subject, linked.UserID, linked.AccountID)
	if err != nil {
		return Issued{}, err
	}
	if !held {
		return Issued{}, ErrAccountNotLinked
	}
	level := levelPassword
	if locked.secondFactor() {
		level = levelPending
	}
	issued, err := s.issue(ctx, tx, newSession{
		user: linked.UserID, account: linked.AccountID, site: site, surface: Console, level: level,
		factorVersion: locked.FactorVersion, ip: ip, userAgent: userAgent,
	})
	if err != nil {
		return Issued{}, err
	}
	if err := (securityAttempt{site: site.ID, user: linked.UserID, account: linked.AccountID, session: issued.session, surface: Console}).record(ctx, tx, "security.sign_in"); err != nil {
		return Issued{}, err
	}
	return issued, nil
}

// identityHeld reports whether the identity still names user and account: an unlink that committed between the first
// read and the user lock must not leave a session behind.
func (s *Service) identityHeld(ctx context.Context, tx pg.Tx, site Site, provider, subject string, user, account uuid.UUID) (bool, error) {
	rows, err := s.queries.FindIdentity(ctx, tx, FindIdentityParams{SiteID: uuid.UUID(site.ID), Provider: provider, Subject: subject})
	if err != nil {
		return false, err
	}
	return len(rows) > 0 && rows[0].UserID == user && rows[0].AccountID == account, nil
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
	if secondFactorUnproved(locked, p) {
		return ErrMFARequired
	}
	return nil
}

func optionalUUID[T ~[16]byte](id T) *uuid.UUID {
	if id == (T{}) {
		return nil
	}
	u := uuid.UUID(id)
	return &u
}
