package newapi

import (
	"errors"
	"fmt"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// LoginKind names this credential structure in suppliers.supplier_logins.kind.
const LoginKind = "pat"

// Login is a new-api account's credentials: a personal access token a person made in the supplier's console, and when
// it expires (the zero time: never). A personal access token cannot be refreshed or made by a program, so there is no
// state to keep current. The legacy access token and user id of 0.13 are not supported.
type Login struct {
	PAT       secret.Secret
	ExpiresAt time.Time
}

// loginV1 is the plaintext form that is sealed. The version lets a later form be told from this one; unknown members
// are refused on reading, so a form this code does not know is an error and not a login missing its fields.
type loginV1 struct {
	V         int    `json:"v"`
	PAT       string `json:"pat"`
	ExpiresAt int64  `json:"expires_at"` // unix seconds, 0 for never
}

// EncodeLogin is the bytes to seal. The plaintext exists only inside it and the function that opens it again.
func EncodeLogin(l Login) ([]byte, error) {
	if l.PAT.Reveal() == "" {
		return nil, errors.New("new-api login: no access token")
	}
	var expires int64
	if !l.ExpiresAt.IsZero() {
		expires = l.ExpiresAt.Unix()
	}
	return jsonx.MarshalCanonical(loginV1{V: 1, PAT: l.PAT.Reveal(), ExpiresAt: expires})
}

// DecodeLogin reads what EncodeLogin wrote. Its errors say what is wrong with the form, never what the form held.
func DecodeLogin(raw []byte) (Login, error) {
	var d loginV1
	if err := jsonx.UnmarshalKnown(raw, &d); err != nil {
		return Login{}, errors.New("new-api login: not a login this version knows")
	}
	if d.V != 1 || d.PAT == "" || d.ExpiresAt < 0 {
		return Login{}, fmt.Errorf("new-api login: version %d with no token or a negative expiry is not a login this version knows", d.V)
	}
	l := Login{PAT: secret.New(d.PAT)}
	if d.ExpiresAt > 0 {
		l.ExpiresAt = time.Unix(d.ExpiresAt, 0).UTC()
	}
	return l, nil
}

// NativeUnit implements adapter.Onboarding: new-api counts quota, an integer of its own.
func (*Adapter) NativeUnit() string { return "quota" }

// LoginFields implements adapter.Onboarding: a personal access token and, if it ends, when. The same with a CAPTCHA,
// since a token needs no login.
func (*Adapter) LoginFields(bool) []adapter.LoginField {
	return []adapter.LoginField{adapter.FieldAccessToken, adapter.FieldAccessTokenExpiresAt}
}

// NewLogin implements adapter.Onboarding.
func (*Adapter) NewLogin(c adapter.Credentials) (adapter.Login, error) {
	if c.AccessToken == "" {
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldAccessToken, Reason: "is required: a personal access token"}
	}
	raw, err := EncodeLogin(Login{PAT: secret.New(c.AccessToken), ExpiresAt: c.AccessTokenExpiresAt})
	if err != nil {
		return adapter.Login{}, err
	}
	return adapter.Login{Kind: LoginKind, Plaintext: raw, ExpiresAt: c.AccessTokenExpiresAt}, nil
}
