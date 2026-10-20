package sub2api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// Login is the account credentials of one sub2api supplier: the tokens it holds and the email and password to log in
// again with when the refresh token is gone. sub2api has no personal access token, and a refresh token works once, so
// these change as they are used.
type Login struct {
	Email                     string
	Password, Access, Refresh secret.Secret
	AccessExpires             time.Time
	Lifetime                  time.Duration // how long an access token lives, to refresh before the last quarter of it
}

// loginV1 is the stored form: plaintext, sealed by the store that holds it. Secret's own marshalling redacts, so a
// Login is never encoded directly.
type loginV1 struct {
	V             int    `json:"v"`
	Email         string `json:"email"`
	Password      string `json:"password"`
	Access        string `json:"access"`
	Refresh       string `json:"refresh"`
	AccessExpires string `json:"access_expires"`
	LifetimeSecs  int64  `json:"lifetime_secs"`
}

// Encode is the stored form of l. It is the one place the secrets are revealed.
func (l Login) Encode() ([]byte, error) {
	return jsonx.MarshalCanonical(loginV1{
		V: 1, Email: l.Email, Password: l.Password.Reveal(), Access: l.Access.Reveal(), Refresh: l.Refresh.Reveal(),
		AccessExpires: l.AccessExpires.UTC().Format(time.RFC3339), LifetimeSecs: int64(l.Lifetime / time.Second),
	})
}

// DecodeLogin reads the stored form, refusing members it does not know: they would be conditions it cannot honour.
func DecodeLogin(raw []byte) (Login, error) {
	var d loginV1
	if err := jsonx.UnmarshalKnown(raw, &d); err != nil || d.V != 1 {
		return Login{}, errors.New("sub2api: the stored login is not in a form this version reads")
	}
	var expires time.Time
	if d.AccessExpires != "" {
		var err error
		if expires, err = time.Parse(time.RFC3339, d.AccessExpires); err != nil {
			return Login{}, errors.New("sub2api: the stored login has a bad expiry")
		}
	}
	return Login{
		Email: d.Email, Password: secret.New(d.Password), Access: secret.New(d.Access), Refresh: secret.New(d.Refresh),
		AccessExpires: expires, Lifetime: time.Duration(d.LifetimeSecs) * time.Second,
	}, nil
}

// session keeps the supplier logged in: it refreshes the access token before it runs out, logs in again with the
// password when the refresh token is refused, and never tries to log in again after the supplier rejects the account.
type session struct {
	c       adapter.Client
	store   adapter.LoginStore
	now     func() time.Time
	login   Login
	version int64
}

var errRefreshInvalid = errors.New("sub2api: the refresh token was refused")

func (s *session) load(ctx context.Context) error {
	raw, version, err := s.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("sub2api: load the login: %w", err)
	}
	l, err := DecodeLogin(raw)
	if err != nil {
		return err
	}
	s.login, s.version = l, version
	return nil
}

func (s *session) fresh() bool {
	return s.login.Access.Reveal() != "" && s.now().Before(s.login.AccessExpires.Add(-s.login.Lifetime/4))
}

// ensure makes the access token one that will work for a while.
func (s *session) ensure(ctx context.Context) error {
	if s.fresh() {
		return nil
	}
	return s.renew(ctx)
}

// renew gets new tokens: by refreshing, and by logging in again only when the supplier refused the refresh token.
// When a refresh fails in a way that does not say whether the token was used up (the network), it is left for the
// next round, which will find the token refused if it was and log in again then.
func (s *session) renew(ctx context.Context) error {
	if s.login.Refresh.Reveal() != "" {
		err := s.refresh(ctx)
		if !errors.Is(err, errRefreshInvalid) {
			return err
		}
	}
	return s.passwordLogin(ctx)
}

// tokens is the data of a login or a refresh.
type tokens struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ExpiresIn int64  `json:"expires_in"`
	TwoFactor bool   `json:"requires_2fa"`
}

func (s *session) refresh(ctx context.Context) error {
	body, err := jsonx.MarshalCanonical(map[string]string{"refresh_token": s.login.Refresh.Reveal()})
	if err != nil {
		return err
	}
	resp, err := s.c.Do(ctx, adapter.Request{
		Method: http.MethodPost, Path: "/api/v1/auth/refresh", Body: body,
		Header: jsonHeader(), Buckets: []string{bucketRefresh},
	})
	if err != nil {
		return fmt.Errorf("sub2api: refresh: %w", err)
	}
	switch {
	case resp.Status == http.StatusUnauthorized && errorCode(resp.Body) == "REFRESH_TOKEN_INVALID":
		return errRefreshInvalid
	case resp.Status == http.StatusTooManyRequests:
		return s.blocked(resp)
	case resp.Status != http.StatusOK:
		return fmt.Errorf("sub2api: refresh: status %d", resp.Status)
	}
	return s.adopt(ctx, resp)
}

func (s *session) passwordLogin(ctx context.Context) error {
	body, err := jsonx.MarshalCanonical(map[string]string{"email": s.login.Email, "password": s.login.Password.Reveal()})
	if err != nil {
		return err
	}
	resp, err := s.c.Do(ctx, adapter.Request{
		Method: http.MethodPost, Path: "/api/v1/auth/login", Body: body,
		Header: jsonHeader(), Buckets: []string{bucketLogin},
	})
	if err != nil {
		return fmt.Errorf("sub2api: login: %w", err)
	}
	switch {
	case resp.Status == http.StatusUnauthorized, resp.Status == http.StatusForbidden:
		return fmt.Errorf("%w: login refused (%s)", adapter.ErrAuthRejected, codeName(errorCode(resp.Body)))
	case resp.Status == http.StatusTooManyRequests:
		return s.blocked(resp)
	case resp.Status != http.StatusOK:
		return fmt.Errorf("sub2api: login: status %d", resp.Status)
	}
	return s.adopt(ctx, resp)
}

// adopt keeps the tokens of a login or refresh answer and stores them. Another holder may have stored newer ones in
// the meantime; theirs are then the ones to use, and nothing more is sent.
func (s *session) adopt(ctx context.Context, resp adapter.Response) error {
	t, err := open[tokens](resp)
	if err != nil {
		return err
	}
	if t.TwoFactor {
		return fmt.Errorf("%w: the account asks for a second factor, which no program can give", adapter.ErrAuthRejected)
	}
	if t.Access == "" || t.Refresh == "" || t.ExpiresIn <= 0 {
		return fmt.Errorf("%w: tokens missing from a login answer", adapter.ErrUnrecognised)
	}
	next := s.login
	next.Access, next.Refresh = secret.New(t.Access), secret.New(t.Refresh)
	next.Lifetime = time.Duration(t.ExpiresIn) * time.Second
	next.AccessExpires = s.now().Add(next.Lifetime)
	raw, err := next.Encode()
	if err != nil {
		return err
	}
	switch err := s.store.Swap(ctx, s.version, raw); {
	case err == nil, errors.Is(err, adapter.ErrStaleLogin):
		return s.load(ctx) // what is stored is what is used: after a stale swap, the other holder's tokens
	default:
		return fmt.Errorf("sub2api: store the new login: %w", err)
	}
}

// blocked is the error of an answer that said to wait.
func (s *session) blocked(resp adapter.Response) error {
	wait := adapter.RetryAfter(resp.Header)
	if wait == 0 {
		wait = time.Minute
	}
	return adapter.Blocked{Until: s.now().Add(wait)}
}

func jsonHeader() http.Header { return http.Header{"Content-Type": {"application/json"}} }

// authed sends a request with the access token. If the supplier says the token ran out it renews once and sends
// again; if it says the account is revoked or inactive that is the end of it, because logging in again would only
// risk the account.
func (s *session) authed(ctx context.Context, r adapter.Request) (adapter.Response, error) {
	if err := s.ensure(ctx); err != nil {
		return adapter.Response{}, err
	}
	for attempt := 0; ; attempt++ {
		r.Header = http.Header{"Authorization": {"Bearer " + s.login.Access.Reveal()}}
		resp, err := s.c.Do(ctx, r)
		if err != nil {
			return adapter.Response{}, fmt.Errorf("sub2api: %s %s: %w", r.Method, r.Path, err)
		}
		switch resp.Status {
		case http.StatusTooManyRequests:
			return adapter.Response{}, s.blocked(resp)
		case http.StatusUnauthorized:
			if err := s.afterRefusal(ctx, resp, attempt); err != nil {
				return adapter.Response{}, err
			}
		default:
			return resp, nil
		}
	}
}

// afterRefusal handles a 401 on an authenticated call: an expired token is renewed (once per call), anything else
// ends the account's use.
func (s *session) afterRefusal(ctx context.Context, resp adapter.Response, attempt int) error {
	code := errorCode(resp.Body)
	if code != "TOKEN_EXPIRED" && code != "INVALID_TOKEN" && code != "UNAUTHORIZED" {
		return fmt.Errorf("%w: %s", adapter.ErrAuthRejected, codeName(code))
	}
	if attempt > 0 {
		return fmt.Errorf("%w: the new token was refused too (%s)", adapter.ErrAuthRejected, codeName(code))
	}
	return s.renew(ctx)
}

// LoginKind names this credential structure in suppliers.supplier_logins.kind.
const LoginKind = "session"

// NativeUnit implements adapter.Onboarding: sub2api counts dollars.
func (*Adapter) NativeUnit() string { return "usd" }

// LoginFields implements adapter.Onboarding: the tokens a person got by signing in by hand, which keep the login alive
// by refreshing, and the email and password to sign in again with. A site with a CAPTCHA cannot be signed into by a
// program, so for it only the tokens.
func (*Adapter) LoginFields(captcha bool) []adapter.LoginField {
	tokens := []adapter.LoginField{adapter.FieldAccessToken, adapter.FieldRefreshToken, adapter.FieldAccessTokenExpiresAt}
	if captcha {
		return tokens
	}
	return append([]adapter.LoginField{adapter.FieldEmail, adapter.FieldPassword}, tokens...)
}

// NewLogin implements adapter.Onboarding: either the tokens, all three, or the email and the password, or both.
func (*Adapter) NewLogin(c adapter.Credentials) (adapter.Login, error) {
	l := Login{Email: c.Email, Password: secret.New(c.Password), Access: secret.New(c.AccessToken), Refresh: secret.New(c.RefreshToken), AccessExpires: c.AccessTokenExpiresAt}
	switch {
	case (c.Email == "") != (c.Password == ""):
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldPassword, Reason: "and email go together"}
	case (c.AccessToken == "") != (c.RefreshToken == ""):
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldRefreshToken, Reason: "and access_token go together"}
	case c.AccessToken != "" && c.AccessTokenExpiresAt.IsZero():
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldAccessTokenExpiresAt, Reason: "is required with the tokens"}
	case c.Email == "" && c.AccessToken == "":
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldAccessToken, Reason: "or email and password are required"}
	}
	raw, err := l.Encode()
	if err != nil {
		return adapter.Login{}, err
	}
	return adapter.Login{Kind: LoginKind, Plaintext: raw, ExpiresAt: c.AccessTokenExpiresAt}, nil
}
