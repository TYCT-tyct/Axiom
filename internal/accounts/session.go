package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

var (
	// ErrBadCredentials is a sign-in refused, alike for an unknown email, a wrong password and a disabled user.
	ErrBadCredentials = errors.New("accounts: wrong email or password")
	// ErrNoSession is a request without a valid session for its site and surface.
	ErrNoSession = errors.New("accounts: not signed in")
	// ErrBadOrigin is a state-changing request whose Origin is not the domain's registered origin, or whose body is not
	// JSON, or which lacks its session's CSRF token.
	ErrBadOrigin = errors.New("accounts: request refused: origin, content type or CSRF token")
)

// Session levels: a password session; a pending one, which only two-step verification accepts; and one that passed
// two-step verification, which sensitive permissions need.
const (
	levelPending  = "pending_mfa"
	levelPassword = "password"
	levelMFA      = "mfa"
)

// pendingFor is how long a pending session waits for its second factor.
const pendingFor = 5 * time.Minute

// Principal is a signed-in user as a request's session proves it. Only Authenticate makes one.
type Principal struct {
	user          UserID
	account       AccountID // the user's personal account
	site          SiteID
	surface       Surface
	session       SessionID
	level         string
	factorVersion int64
	// consentPending is whether a current legal document is not accepted; the console guard refuses everything but
	// reading the session, accepting, and signing out until it is false.
	consentPending bool
}

// UserID is the signed-in user.
func (p Principal) UserID() UserID { return p.user }

// AccountID is the user's personal account.
func (p Principal) AccountID() AccountID { return p.account }

// SessionID is the session the principal was recognised by.
func (p Principal) SessionID() SessionID { return p.session }

// Pending reports whether the session still waits for its second factor, as only AuthenticateAny returns one.
func (p Principal) Pending() bool { return p.level == levelPending }

// Verified reports whether the session passed two-step verification.
func (p Principal) Verified() bool { return p.level == levelMFA }

// ConsentPending reports whether the user has not accepted a current legal document. It is computed with the rest of
// the session so a new version takes effect on the next request, not when a snapshot reloads.
func (p Principal) ConsentPending() bool { return p.consentPending }

// SignInRequest is a sign-in with a password at the request's Host.
type SignInRequest struct {
	Host, Email, Password string
	ClientIP              netip.Addr
	UserAgent             string
}

// Issued is a new session: its token, which goes only into the cookie, and its CSRF token, which the client sends
// back in X-CSRF-Token. Pending is a session that waits for the second factor.
type Issued struct {
	session          uuid.UUID
	Token, CSRFToken string
	Pending          bool
	MaxAge           time.Duration
}

// SignIn checks the password and issues a session, pending when the user has two-step verification. Each sign-in
// spends one of its IP's attempts and, until it succeeds, one of its email's failures (ErrTooManyAttempts when none is
// left); the hash takes one of the node's turns (ErrBusy). The hash is checked before the issuing transaction, which
// then locks the user and issues only if the factors have not changed and the user is enabled. An unknown email costs
// a hash too.
func (s *Service) SignIn(ctx context.Context, r SignInRequest) (out Issued, err error) {
	d, snap, err := s.domain(r.Host)
	if err != nil {
		return Issued{}, err
	}
	if d.surface != Console && d.surface != Admin {
		return Issued{}, ErrUnknownHost
	}
	event := securityAttempt{site: d.site, surface: d.surface}
	defer func() { s.failed(ctx, event, "security.sign_in_failed", &err) }()
	if err := s.fromIP(ctx, d.site, r.ClientIP); err != nil {
		return Issued{}, err
	}
	var issued Issued
	err = failing(s, ctx, s.emailLimit(d.site, normalEmail(r.Email), snap.sites[d.site].limits), func() error {
		issued, err = s.signIn(ctx, r, d, snap, &event)
		return err
	})
	return issued, err
}

func (s *Service) signIn(ctx context.Context, r SignInRequest, d domain, snap *snapshot, event *securityAttempt) (Issued, error) {
	var err error
	var found []LoginTenantRow
	if err := s.db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		found, err = s.queries.LoginTenant(ctx, tx, LoginTenantParams{Domain: strings.ToLower(r.Host), Email: normalEmail(r.Email)})
		return err
	}); err != nil {
		return Issued{}, fmt.Errorf("sign in: %w", err)
	}
	if len(found) == 0 {
		if err := s.hash(ctx, func() { passwordMatches(dummyHash, r.Password) }); err != nil {
			return Issued{}, err
		}
		return Issued{}, ErrBadCredentials
	}
	user, account := found[0].UserID, found[0].AccountID
	event.user, event.account = user, account
	var stored []UserPasswordRow
	if err := s.db.Tx(ctx, pg.AccountScope(account), func(tx pg.Tx) error {
		stored, err = s.queries.UserPassword(ctx, tx, user)
		return err
	}); err != nil || len(stored) == 0 {
		return Issued{}, fmt.Errorf("sign in: %w", errors.Join(err, ErrBadCredentials))
	}
	matched := false
	if err := s.hash(ctx, func() { matched = passwordMatches(stored[0].PasswordHash, r.Password) }); err != nil {
		return Issued{}, err
	}
	if !matched {
		return Issued{}, ErrBadCredentials
	}
	var issued Issued
	err = s.db.Tx(ctx, pg.AccountScope(account), func(tx pg.Tx) error {
		issued, err = s.passwordSession(ctx, tx, r, d, snap, user, account, stored[0].FactorVersion, event)
		return err
	})
	return issued, err
}

// passwordSession decides the level a password sign-in gets and issues the session, under the user's lock.
func (s *Service) passwordSession(ctx context.Context, tx pg.Tx, r SignInRequest, d domain, snap *snapshot, user, account uuid.UUID, factorVersion int64, event *securityAttempt) (Issued, error) {
	locked, err := s.lockUser(ctx, tx, user, factorVersion)
	if err != nil {
		return Issued{}, err
	}
	// A passkey only works on the site's login domain. A user with no TOTP is sent there instead of getting a session
	// that skips the passkey; a user who also has TOTP can finish that on any console domain, and the admin surface,
	// which cannot run WebAuthn at all, keeps a usable password session.
	if locked.Passkeys && !locked.Mfa && d.surface == Console && d.origin != snap.sites[d.site].LoginOrigin {
		return Issued{}, ErrWrongLoginDomain
	}
	level := levelPassword
	if locked.Mfa || (locked.Passkeys && d.surface == Console) {
		level = levelPending
	}
	issued, err := s.issue(ctx, tx, newSession{
		user: user, account: account, site: snap.sites[d.site], surface: d.surface, level: level,
		factorVersion: locked.FactorVersion, ip: r.ClientIP, userAgent: r.UserAgent,
	})
	if err != nil {
		return Issued{}, err
	}
	event.session = issued.session
	if err := event.record(ctx, tx, "security.sign_in"); err != nil {
		return Issued{}, err
	}
	return issued, nil
}

// secondFactor reports whether the user has a second factor to prove: two-step verification or a passkey. Either one
// makes a password sign-in wait for it, and a sensitive change need an already verified session.
func (r LockUserRow) secondFactor() bool { return r.Mfa || r.Passkeys }

// passkeyFactor reports whether a passkey is a second factor on this principal's surface. The admin listener cannot run
// a WebAuthn ceremony, so a passkey there would only leave a pending session no page can complete.
func (p Principal) passkeyFactor() bool { return p.surface == Console }

// secondFactorUnproved reports whether a change must refuse this session: the user has a second factor the session's
// surface can prove and the session has not proved it.
func secondFactorUnproved(locked LockUserRow, p Principal) bool {
	return (locked.Mfa || (locked.Passkeys && p.passkeyFactor())) && !p.Verified()
}

// resumedLevel is the level a replacement session keeps after a credential change this session performed.
func resumedLevel(p Principal) string {
	if p.Verified() {
		return levelMFA
	}
	return levelPassword
}

// lockUser is the first statement of a transaction that issues a session or spends or changes a factor: it locks the
// user's row and refuses a disabled user or factors changed since version.
func (s *Service) lockUser(ctx context.Context, tx pg.Tx, user uuid.UUID, version int64) (LockUserRow, error) {
	rows, err := s.queries.LockUser(ctx, tx, user)
	if err != nil {
		return LockUserRow{}, err
	}
	if len(rows) == 0 || !rows[0].Active || rows[0].FactorVersion != version {
		return LockUserRow{}, ErrBadCredentials
	}
	return rows[0], nil
}

type newSession struct {
	user, account uuid.UUID
	site          Site
	surface       Surface
	level         string
	factorVersion int64
	ip            netip.Addr
	userAgent     string
}

// issue inserts a session with new tokens; a pending one lasts pendingFor, others the site's lifetime for the surface.
func (s *Service) issue(ctx context.Context, tx pg.Tx, n newSession) (Issued, error) {
	life := n.site.sessions[n.surface]
	if n.level == levelPending {
		life = lifetime{pendingFor, pendingFor}
	}
	token, csrf := randomToken(), randomToken()
	hash := sha256.Sum256([]byte(token))
	ip := ""
	if n.ip.IsValid() {
		ip = n.ip.String()
	}
	session := uuid.NewV7()
	if err := s.queries.InsertSession(ctx, tx, InsertSessionParams{
		ID: session, SiteID: uuid.UUID(n.site.ID), AccountID: n.account, UserID: n.user, Surface: string(n.surface),
		TokenHash: hash[:], CsrfToken: csrf, Level: n.level, FactorVersion: n.factorVersion,
		MaxAgeSeconds: int64(life.max / time.Second), IdleSeconds: int64(life.idle / time.Second),
		Ip: ip, UserAgent: truncate(n.userAgent, 512),
	}); err != nil {
		return Issued{}, fmt.Errorf("issue a session: %w", err)
	}
	return Issued{session: session, Token: token, CSRFToken: csrf, Pending: n.level == levelPending, MaxAge: life.max}, nil
}

// Authenticate is the user whose session the request's cookie carries, for the site and surface of its Host. A
// state-changing request must also come from the domain's registered origin, carry JSON and the session's CSRF token.
// A pending session is not signed in. ip is the caller-resolved trusted client address for unknown-cookie limits.
func (s *Service) Authenticate(ctx context.Context, r *http.Request, ip netip.Addr) (Principal, error) {
	return s.authenticate(ctx, r, ip, false)
}

// AuthenticateAny is Authenticate for the operations a session still waiting for its second factor may do: read
// itself and end.
func (s *Service) AuthenticateAny(ctx context.Context, r *http.Request, ip netip.Addr) (Principal, error) {
	return s.authenticate(ctx, r, ip, true)
}

func (s *Service) authenticate(ctx context.Context, r *http.Request, ip netip.Addr, pendingToo bool) (out Principal, err error) {
	found, _, d, err := s.sessionOf(ctx, r)
	if err != nil {
		return Principal{}, s.failedSession(ctx, r, d, ip, err)
	}
	var valid []ValidSessionRow
	life := s.snapshot.Load().sites[d.site].sessions[d.surface]
	maxAge, idle := life.seconds()
	if err := s.db.Tx(ctx, pg.AccountScope(found.AccountID), func(tx pg.Tx) error {
		if valid, err = s.queries.ValidSession(ctx, tx, ValidSessionParams{ID: found.SessionID, MaxAgeSeconds: maxAge, IdleSeconds: idle}); err != nil || len(valid) == 0 {
			return err
		}
		return s.queries.TouchSession(ctx, tx, found.SessionID)
	}); err != nil {
		return Principal{}, fmt.Errorf("authenticate: %w", err)
	}
	if len(valid) == 0 || valid[0].Level == levelPending && !pendingToo {
		return Principal{}, ErrNoSession
	}
	if err := checkCSRF(r, d, valid[0].CsrfToken); err != nil {
		return Principal{}, err
	}
	return Principal{
		user: UserID(found.UserID), account: AccountID(found.AccountID), site: d.site, surface: d.surface,
		session: SessionID(found.SessionID), level: valid[0].Level, factorVersion: valid[0].FactorVersion,
		consentPending: valid[0].ConsentPending,
	}, nil
}

// sessionOf finds the session the request's cookie names, on its Host's site and surface, with no scope, and returns
// it with its token's hash.
func (s *Service) sessionOf(ctx context.Context, r *http.Request) (SessionTenantRow, []byte, domain, error) {
	d, _, err := s.domain(r.Host)
	if err != nil {
		return SessionTenantRow{}, nil, domain{}, err
	}
	cookie, err := r.Cookie(cookieName(d.origin))
	if err != nil {
		return SessionTenantRow{}, nil, domain{}, ErrNoSession
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	var found []SessionTenantRow
	if err := s.db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		found, err = s.queries.SessionTenant(ctx, tx, SessionTenantParams{
			Domain: strings.ToLower(r.Host), Surface: string(d.surface), TokenHash: hash[:],
		})
		return err
	}); err != nil {
		return SessionTenantRow{}, nil, domain{}, fmt.Errorf("find a session: %w", err)
	}
	if len(found) == 0 {
		return SessionTenantRow{}, nil, d, ErrNoSession
	}
	return found[0], hash[:], d, nil
}

// SignOut revokes p's session.
func (s *Service) SignOut(ctx context.Context, p Principal) error {
	return s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		_, err := s.queries.RevokeSession(ctx, tx, uuid.UUID(p.session))
		return err
	})
}

// ExpiredCookie is the cookie that ends the session cookie at host, for signing out.
func (s *Service) ExpiredCookie(host string) (*http.Cookie, error) {
	return s.Cookie(host, Issued{MaxAge: -time.Second})
}

// CheckOrigin refuses a state-changing request, signed in or not, that does not come from its domain's registered
// origin or does not carry JSON. Sign-in, registration and two-step verification call it before anything else.
func (s *Service) CheckOrigin(r *http.Request) error {
	d, _, err := s.domain(r.Host)
	if err != nil {
		return err
	}
	return checkCSRF(r, d, "")
}

// checkCSRF checks a state-changing request: its Origin is exactly the domain's registered origin, a missing or
// "null" one refused; its body is JSON, which a form cannot send across sites; and, with a session, it carries the
// session's CSRF token. Reads pass.
func checkCSRF(r *http.Request, d domain, csrf string) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Header.Get("Origin") != d.origin || err != nil || media != "application/json" {
		return ErrBadOrigin
	}
	if csrf != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Csrf-Token")), []byte(csrf)) != 1 {
		return ErrBadOrigin
	}
	return nil
}

// Cookie is the session cookie for issued at host: __Host- prefixed and Secure, except on a domain registered with a
// plain-http localhost origin, which the registration decides, not the request.
func (s *Service) Cookie(host string, issued Issued) (*http.Cookie, error) {
	d, _, err := s.domain(host)
	if err != nil {
		return nil, err
	}
	return &http.Cookie{ // #nosec G124 -- Secure for every https origin; only a registered plain-http localhost origin goes without
		Name: cookieName(d.origin), Value: issued.Token, Path: "/", MaxAge: int(issued.MaxAge / time.Second),
		HttpOnly: true, Secure: strings.HasPrefix(d.origin, "https://"), SameSite: http.SameSiteLaxMode,
	}, nil
}

func cookieName(origin string) string {
	if strings.HasPrefix(origin, "https://") {
		return "__Host-axiom_session"
	}
	return "axiom_session"
}

// randomToken is 256 random bits, URL-safe.
func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(b)
}

func normalEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
