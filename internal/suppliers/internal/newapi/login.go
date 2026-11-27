package newapi

import (
	"errors"
	"fmt"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// LoginKind and LegacyLoginKind name the two credential structures in suppliers.supplier_logins.kind.
const (
	LoginKind       = "pat"
	LegacyLoginKind = "access_token"
)

// Login is a new-api account's credentials: a personal access token a person made in the supplier's console, and when
// it expires (the zero time: never). A personal access token cannot be refreshed or made by a program, so there is no
// state to keep current.
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

// LegacyLogin is the login of the systems before personal access tokens, new-api before 1.0 and one-api: the system
// access token a person generated in the account's settings, and the account's numeric id, which new-api asks for on
// every request beside the token (New-Api-User) and one-api ignores. The token does not expire; generating another one
// replaces it.
type LegacyLogin struct {
	AccessToken secret.Secret
	UserID      string
}

// legacyLoginV1 is the plaintext form of a LegacyLogin. Its members are not loginV1's, so neither form reads as the
// other.
type legacyLoginV1 struct {
	V           int    `json:"v"`
	AccessToken string `json:"access_token"`
	UserID      string `json:"user_id"`
}

// EncodeLegacyLogin is the bytes to seal.
func EncodeLegacyLogin(l LegacyLogin) ([]byte, error) {
	if l.AccessToken.Reveal() == "" || !decimalID.MatchString(l.UserID) {
		return nil, errors.New("new-api login: no access token or no user id")
	}
	return jsonx.MarshalCanonical(legacyLoginV1{V: 1, AccessToken: l.AccessToken.Reveal(), UserID: l.UserID})
}

// DecodeLegacyLogin reads what EncodeLegacyLogin wrote.
func DecodeLegacyLogin(raw []byte) (LegacyLogin, error) {
	var d legacyLoginV1
	if err := jsonx.UnmarshalKnown(raw, &d); err != nil || d.V != 1 || d.AccessToken == "" || !decimalID.MatchString(d.UserID) {
		return LegacyLogin{}, errors.New("new-api login: not a legacy login this version knows")
	}
	return LegacyLogin{AccessToken: secret.New(d.AccessToken), UserID: d.UserID}, nil
}

// NativeUnit implements adapter.Onboarding: new-api counts quota, an integer of its own.
func (*Adapter) NativeUnit() string { return "quota" }

// LoginFields implements adapter.Onboarding: a personal access token and, if it ends, when; or, for a system before
// personal access tokens, its system access token and the user id. The same with a CAPTCHA, since a token needs no
// login.
func (*Adapter) LoginFields(bool) []adapter.LoginField {
	return []adapter.LoginField{adapter.FieldAccessToken, adapter.FieldAccessTokenExpiresAt, adapter.FieldUserID}
}

// NewLogin implements adapter.Onboarding: a legacy login when a user id is given, else a personal access token.
func (*Adapter) NewLogin(c adapter.Credentials) (adapter.Login, error) {
	if c.AccessToken == "" {
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldAccessToken, Reason: "is required: a personal access token, or a system access token with user_id"}
	}
	if c.UserID != "" {
		return newLegacyLogin(c)
	}
	raw, err := EncodeLogin(Login{PAT: secret.New(c.AccessToken), ExpiresAt: c.AccessTokenExpiresAt})
	if err != nil {
		return adapter.Login{}, err
	}
	return adapter.Login{Kind: LoginKind, Plaintext: raw, ExpiresAt: c.AccessTokenExpiresAt}, nil
}

func newLegacyLogin(c adapter.Credentials) (adapter.Login, error) {
	switch {
	case !c.AccessTokenExpiresAt.IsZero():
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldAccessTokenExpiresAt, Reason: "does not apply: a system access token does not expire"}
	case !decimalID.MatchString(c.UserID):
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldUserID, Reason: "is not a user id: the number the supplier's settings page shows"}
	}
	raw, err := EncodeLegacyLogin(LegacyLogin{AccessToken: secret.New(c.AccessToken), UserID: c.UserID})
	if err != nil {
		return adapter.Login{}, err
	}
	return adapter.Login{Kind: LegacyLoginKind, Plaintext: raw}, nil
}
