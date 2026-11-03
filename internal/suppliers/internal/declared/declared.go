package declared

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

var (
	_ adapter.Adapter    = (*Adapter)(nil)
	_ adapter.Onboarding = (*Adapter)(nil)
	_ adapter.Manager    = (*manager)(nil)
)

// LoginKind is the kind of the sealed login a declaration writes. It names the structure, not the declaration: a
// declaration change keeps the same structure, and a stored login this structure cannot read is refused.
const LoginKind = "declared"

// Kind implements adapter.Adapter.
func (a *Adapter) Kind() adapter.Kind { return a.kind }

// Buckets implements adapter.Adapter.
func (a *Adapter) Buckets() []adapter.BucketSpec { return a.buckets }

// Calls implements adapter.Adapter.
func (a *Adapter) Calls() adapter.CallQuirks { return quirks{p: a.calls} }

// NativeUnit implements adapter.Onboarding: quota or usd, as the declaration states.
func (a *Adapter) NativeUnit() string { return a.native }

// LoginFields implements adapter.Onboarding. A CAPTCHA cannot be passed by a program, so a site showing one takes only
// an access token a person made: the token fields when the declaration has them, and none otherwise, which says the
// site cannot be connected while its check is on.
func (a *Adapter) LoginFields(captcha bool) []adapter.LoginField {
	if !captcha {
		return a.auth.fields
	}
	var fields []adapter.LoginField
	for _, f := range a.auth.fields {
		if f == adapter.FieldAccessToken || f == adapter.FieldAccessTokenExpiresAt {
			fields = append(fields, f)
		}
	}
	return fields
}

// NewLogin implements adapter.Onboarding: it checks the members the declaration takes and seals only those. A token
// wins over an account's email and password, so a person can connect a CAPTCHA site with a token.
func (a *Adapter) NewLogin(c adapter.Credentials) (adapter.Login, error) {
	switch {
	case c.AccessToken != "" && hasField(a.auth.fields, adapter.FieldAccessToken):
		raw, err := encodeLogin(loginV1{V: 1, AccessToken: c.AccessToken, ExpiresAt: unix(c.AccessTokenExpiresAt)})
		if err != nil {
			return adapter.Login{}, err
		}
		return adapter.Login{Kind: LoginKind, Plaintext: raw, ExpiresAt: c.AccessTokenExpiresAt}, nil
	case c.Email == "" && c.Password == "":
		if hasField(a.auth.fields, adapter.FieldAccessToken) {
			return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldAccessToken, Reason: "is required"}
		}
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldEmail, Reason: "is required"}
	case c.Email == "" || c.Password == "":
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldPassword, Reason: "and email go together"}
	case !hasField(a.auth.fields, adapter.FieldEmail):
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldEmail, Reason: "is not taken by this system"}
	}
	raw, err := encodeLogin(loginV1{V: 1, Email: c.Email, Password: c.Password})
	if err != nil {
		return adapter.Login{}, err
	}
	return adapter.Login{Kind: LoginKind, Plaintext: raw}, nil
}

func hasField(fields []adapter.LoginField, want adapter.LoginField) bool {
	return slices.Contains(fields, want)
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// loginV1 is the sealed form. AccessToken or Email and Password is set, never both paths for one connection.
type loginV1 struct {
	V           int    `json:"v"`
	AccessToken string `json:"access_token,omitempty"`
	Email       string `json:"email,omitempty"`
	Password    string `json:"password,omitempty"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
}

func encodeLogin(l loginV1) ([]byte, error) {
	return jsonx.MarshalCanonical(l)
}

func decodeLogin(raw []byte) (loginV1, error) {
	var l loginV1
	if err := jsonx.UnmarshalKnown(raw, &l); err != nil || l.V != 1 || l.ExpiresAt < 0 {
		return loginV1{}, errors.New("declared: the stored login is not a form this version reads")
	}
	switch {
	case l.AccessToken != "" && l.Email == "" && l.Password == "":
	case l.AccessToken == "" && l.Email != "" && l.Password != "":
	default:
		return loginV1{}, errors.New("declared: the stored login holds neither a token nor an account")
	}
	return l, nil
}

// Open implements adapter.Adapter. A static token is used as stored; otherwise the login runs here, on every Open,
// because the declaration has no refresh operation and a token it does not keep cannot silently expire.
func (a *Adapter) Open(ctx context.Context, c adapter.Client, login adapter.LoginStore) (adapter.Manager, error) {
	raw, _, err := login.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("declared: load the login: %w", err)
	}
	l, err := decodeLogin(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", adapter.ErrAuthRejected, err)
	}
	m := &manager{a: a, c: c}
	switch {
	case l.AccessToken != "" && !a.auth.hasToken:
		return nil, fmt.Errorf("%w: the stored token is not one this declaration takes", adapter.ErrAuthRejected)
	case l.AccessToken != "":
		if l.ExpiresAt != 0 && !a.now().Before(time.Unix(l.ExpiresAt, 0)) {
			return nil, fmt.Errorf("%w: the access token expired", adapter.ErrAuthRejected)
		}
		m.token = l.AccessToken
	case a.auth.login == nil:
		return nil, fmt.Errorf("%w: the stored account has no token this declaration can use", adapter.ErrAuthRejected)
	default:
		m.token, err = a.login(ctx, c, l)
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

// login runs the declaration's login operation and returns the token it answered with.
func (a *Adapter) login(ctx context.Context, c adapter.Client, l loginV1) (string, error) {
	p := a.auth.login
	body := make(map[string]string, len(p.body))
	for member, t := range p.body {
		body[member] = t.raw(map[string]string{"email": l.Email, "password": l.Password})
	}
	encoded, err := jsonx.MarshalCanonical(body)
	if err != nil {
		return "", fmt.Errorf("declared: the login body: %w", err)
	}
	resp, err := send(ctx, c, http.MethodPost, p.path, nil, p.bucket, encoded)
	if err != nil {
		return "", err
	}
	if err := loginStatus(resp, a.now); err != nil {
		return "", err
	}
	token, err := loginToken(p.token, resp.Body)
	if err != nil {
		return "", err
	}
	if p.expiresAt != nil {
		if err := loginExpiry(p.expiresAt, resp.Body, a.now); err != nil {
			return "", err
		}
	}
	return token, nil
}

func loginStatus(resp adapter.Response, now func() time.Time) error {
	switch {
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		return fmt.Errorf("%w: the login was refused", adapter.ErrAuthRejected)
	case resp.Status == http.StatusTooManyRequests:
		wait := adapter.RetryAfter(resp.Header)
		if wait == 0 {
			wait = time.Minute
		}
		return adapter.Blocked{Until: now().Add(wait)}
	case resp.Status >= http.StatusInternalServerError:
		return fmt.Errorf("declared: the login answered %d", resp.Status)
	case resp.Status < 200 || resp.Status >= 300:
		return fmt.Errorf("%w: the login answered %d", adapter.ErrUnrecognised, resp.Status)
	}
	return nil
}

func loginToken(p path, body []byte) (string, error) {
	raw, ok := p.lookup(body)
	if !ok {
		return "", missing("token in the login answer")
	}
	token, ok := readText(raw)
	if !ok || token == "" {
		return "", missing("token in the login answer")
	}
	return token, nil
}

func loginExpiry(p *path, body []byte, now func() time.Time) error {
	raw, ok := p.lookup(body)
	if !ok {
		return missing("expiry in the login answer")
	}
	expires, ok := readTime(raw)
	if !ok {
		return fmt.Errorf("%w: the login answer's expiry", adapter.ErrUnrecognised)
	}
	if !now().Before(expires) {
		return fmt.Errorf("%w: the login answered an expired token", adapter.ErrAuthRejected)
	}
	return nil
}

// manager is one open management session: the adapter's plan, the client and the token.
type manager struct {
	a     *Adapter
	c     adapter.Client
	token string
}

// send sends one request. The token goes where the declaration says; a GET has no body.
func send(ctx context.Context, c adapter.Client, method string, t tmpl, vals map[string]string, bucket string, body []byte) (adapter.Response, error) {
	path := t.expand(vals)
	header := http.Header{"Accept": {"application/json"}}
	if body != nil {
		header.Set("Content-Type", "application/json")
	}
	return c.Do(ctx, adapter.Request{Method: method, Path: path, Header: header, Body: body, Buckets: []string{bucket}})
}

// do sends one management request with the session's token and classifies the status the way every management read
// does: a refused credential stops polling, a limit is not a failure, and anything else is the task's to retry.
func (m *manager) do(ctx context.Context, what, method string, t tmpl, vals map[string]string, bucket string, body []byte) (adapter.Response, error) {
	header := http.Header{"Accept": {"application/json"}}
	if body != nil {
		header.Set("Content-Type", "application/json")
	}
	path := t.expand(vals)
	switch {
	case m.a.auth.header != "":
		header.Set(m.a.auth.header, m.a.auth.prefix+m.token)
	case m.a.auth.query != "":
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		path += sep + m.a.auth.query + "=" + url.QueryEscape(m.token)
	}
	resp, err := m.c.Do(ctx, adapter.Request{Method: method, Path: path, Header: header, Body: body, Buckets: []string{bucket}})
	if err != nil {
		return adapter.Response{}, err
	}
	if err := managementStatus(resp, what, m.a.now); err != nil {
		return adapter.Response{}, err
	}
	return resp, nil
}

// managementStatus classifies a management answer: a refused credential stops polling, a limit is not a failure,
// and anything else is the task's to retry.
func managementStatus(resp adapter.Response, what string, now func() time.Time) error {
	switch {
	case resp.Status >= 200 && resp.Status < 300:
		return nil
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		return fmt.Errorf("%w: %s answered %d", adapter.ErrAuthRejected, what, resp.Status)
	case resp.Status == http.StatusTooManyRequests:
		wait := adapter.RetryAfter(resp.Header)
		if wait == 0 {
			wait = time.Minute
		}
		return adapter.Blocked{Until: now().Add(wait)}
	case resp.Status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %s answered %d", errServer, what, resp.Status)
	default:
		return fmt.Errorf("%w: %w", adapter.ErrUnrecognised, &statusError{what: what, status: resp.Status})
	}
}

// errServer marks a 5xx: the supplier may have run something, so a key step must leave it to the task's retry and not
// take it for a refused key.
var errServer = errors.New("declared: supplier server error")

// statusError keeps the status of a refusal that is not auth or a limit, so a key step can name it the way its contract
// asks without guessing from the message.
type statusError struct {
	what   string
	status int
}

func (e *statusError) Error() string { return fmt.Sprintf("%s answered %d", e.what, e.status) }

// Account implements adapter.Manager.
func (m *manager) Account(ctx context.Context) (adapter.Account, error) {
	resp, err := m.do(ctx, "account", http.MethodGet, m.a.account.path, nil, m.a.account.bucket, nil)
	if err != nil {
		return adapter.Account{}, err
	}
	raw, ok := m.a.account.balance.lookup(resp.Body)
	if !ok {
		return adapter.Account{}, missing("balance")
	}
	balance, ok := readBalance(raw)
	if !ok {
		return adapter.Account{}, fmt.Errorf("%w: the balance is not a decimal amount", adapter.ErrUnrecognised)
	}
	acc := adapter.Account{Balance: balance, Funding: adapter.FundingWallet}
	if m.a.account.unitHint != nil {
		raw, ok := m.a.account.unitHint.lookup(resp.Body)
		if !ok {
			return adapter.Account{}, missing("unit hint")
		}
		if acc.UnitHint, ok = readText(raw); !ok {
			return adapter.Account{}, fmt.Errorf("%w: the unit hint is not text", adapter.ErrUnrecognised)
		}
	} else {
		acc.UnitHint = m.a.account.unitValue
	}
	if m.a.account.funding != nil {
		acc.Funding = fundingOf(m.a.account.funding, resp.Body)
	}
	return acc, nil
}

// fundingOf reads where a record says it was paid from. A missing value is not the wallet: only a value the
// declaration names is, so a changed answer cannot quietly turn a subscription into cash.
func fundingOf(f *fundingPlan, body []byte) adapter.Funding {
	raw, ok := f.path.lookup(body)
	if !ok {
		return adapter.FundingOther
	}
	text, ok := readText(raw)
	if ok && f.wallet[text] {
		return adapter.FundingWallet
	}
	return adapter.FundingOther
}

// KeySecret implements adapter.Manager.
func (m *manager) KeySecret(ctx context.Context, ref adapter.KeyRef) (secret.Secret, error) {
	resp, err := m.do(ctx, "key secret", http.MethodGet, m.a.keys.secret.path, map[string]string{"ref": string(ref)}, m.a.keys.secret.bucket, nil)
	if err != nil {
		return secret.Secret{}, err
	}
	raw, ok := m.a.keys.secret.token.lookup(resp.Body)
	if !ok {
		return secret.Secret{}, missing("key text")
	}
	text, ok := readText(raw)
	if !ok || text == "" {
		return secret.Secret{}, missing("key text")
	}
	return secret.New(text), nil
}

// CreateKey implements adapter.KeyMaker. A declaration without a create operation answers ErrNoKeyMaker, which makes
// the sync keep a person's bound key instead.
func (m *manager) CreateKey(ctx context.Context, groupRef, name string) (adapter.KeyRef, error) {
	p := m.a.keys.create
	if p == nil {
		return "", adapter.ErrNoKeyMaker
	}
	body := make(map[string]string, len(p.body))
	values := map[string]string{"name": name, "group": groupRef}
	for member, t := range p.body {
		body[member] = t.raw(values)
	}
	encoded, err := jsonx.MarshalCanonical(body)
	if err != nil {
		return "", fmt.Errorf("declared: the key body: %w", err)
	}
	resp, err := m.do(ctx, "key creation", http.MethodPost, p.path, nil, p.bucket, encoded)
	if err != nil {
		return "", createError(err)
	}
	raw, ok := p.ref.lookup(resp.Body)
	if !ok {
		return "", missing("id of the made key")
	}
	ref, ok := readText(raw)
	if !ok || ref == "" {
		return "", missing("id of the made key")
	}
	return adapter.KeyRef(ref), nil
}

// createError names a refused key creation the way the sync's key steps expect, so the group waits instead of the
// whole task failing. A 5xx, a transport failure and a broken declaration stay task failures.
func createError(err error) error {
	if errors.Is(err, adapter.ErrAuthRejected) {
		return fmt.Errorf("%w: %w", adapter.ErrKeyNotPermitted, err)
	}
	refused, ok := errors.AsType[*statusError](err)
	if !ok {
		return err
	}
	switch refused.status {
	case http.StatusNotFound, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %w", adapter.ErrKeyGroupUnavailable, err)
	default:
		return fmt.Errorf("%w: %w", adapter.ErrKeyRefused, err)
	}
}
