package cpa

import (
	"errors"
	"fmt"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// Login is the management key of one CPA. CPA has no separate login endpoint for the management API: the key is sent
// with every request, and rotating it is replacing this login (internal/suppliers/README.md「CPA」).
type Login struct{ Key secret.Secret }

// loginV1 is the stored form: plaintext, sealed by the store that holds it. Secret's own marshalling redacts, so a Login
// is never encoded directly.
type loginV1 struct {
	V   int    `json:"v"`
	Key string `json:"management_key"`
}

// Encode is the stored form of l. It is the one place the secret is revealed.
func (l Login) Encode() ([]byte, error) {
	return jsonx.MarshalCanonical(loginV1{V: 1, Key: l.Key.Reveal()})
}

// DecodeLogin reads the stored form, refusing members it does not know: they would be conditions it cannot honour.
func DecodeLogin(raw []byte) (Login, error) {
	var d loginV1
	if err := jsonx.UnmarshalKnown(raw, &d); err != nil || d.V != 1 || d.Key == "" {
		return Login{}, errors.New("cpa: the stored login is not in a form this version reads")
	}
	return Login{Key: secret.New(d.Key)}, nil
}

// NativeUnit implements adapter.Onboarding. A CPA pool has no cash balance: the balance field is the number of accounts
// that can serve now, so one native unit is one account.
func (*Adapter) NativeUnit() string { return "quota" }

// LoginFields implements adapter.Onboarding.
func (*Adapter) LoginFields(bool) []adapter.LoginField {
	return []adapter.LoginField{adapter.FieldAccessToken}
}

// NewLogin implements adapter.Onboarding. The management key is checked by the first management read, not here: an
// unauthenticated probe would count against CPA's five-failure address ban.
func (*Adapter) NewLogin(c adapter.Credentials) (adapter.Login, error) {
	if c.Email != "" || c.Password != "" || c.RefreshToken != "" || !c.AccessTokenExpiresAt.IsZero() {
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldAccessToken, Reason: "is the only member CPA takes"}
	}
	if c.AccessToken == "" {
		return adapter.Login{}, &adapter.LoginError{Field: adapter.FieldAccessToken, Reason: "is required"}
	}
	raw, err := Login{Key: secret.New(c.AccessToken)}.Encode()
	if err != nil {
		return adapter.Login{}, fmt.Errorf("cpa: encode the login: %w", err)
	}
	return adapter.Login{Kind: "cpa", Plaintext: raw}, nil
}
