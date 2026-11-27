package adapter

import (
	"time"
)

// LoginField names a member of Credentials, as the admin API names it.
type LoginField string

// The members of Credentials.
const (
	FieldEmail                LoginField = "email"
	FieldPassword             LoginField = "password"
	FieldAccessToken          LoginField = "access_token"
	FieldRefreshToken         LoginField = "refresh_token"
	FieldAccessTokenExpiresAt LoginField = "access_token_expires_at"
)

// Credentials is what a person gives to connect a supplier: whichever of the members the system's login takes. They
// are secrets; nothing here is logged or returned.
type Credentials struct {
	Email, Password, AccessToken, RefreshToken string
	AccessTokenExpiresAt                       time.Time
}

// Present lists the members that are set.
func (c Credentials) Present() []LoginField {
	var fields []LoginField
	for _, f := range []struct {
		field LoginField
		set   bool
	}{
		{FieldEmail, c.Email != ""},
		{FieldPassword, c.Password != ""},
		{FieldAccessToken, c.AccessToken != ""},
		{FieldRefreshToken, c.RefreshToken != ""},
		{FieldAccessTokenExpiresAt, !c.AccessTokenExpiresAt.IsZero()},
	} {
		if f.set {
			fields = append(fields, f.field)
		}
	}
	return fields
}

// Login is credentials in the form that is sealed: the kind of the structure (suppliers.supplier_logins.kind), its
// plaintext, which the caller seals and nobody else keeps, and when the access it gives ends, zero for never.
type Login struct {
	Kind      string
	Plaintext []byte
	ExpiresAt time.Time
}

// LoginError is credentials that cannot make a login: the member that is wrong or missing, and which rule it breaks.
// Its text never holds a value.
type LoginError struct {
	Field  LoginField
	Reason string
}

func (e *LoginError) Error() string { return "login: " + string(e.Field) + " " + e.Reason }

// Onboarding is what an adapter adds to be connected by a person from the admin: it is optional, as writing is, so
// that what an adapter can do is what it implements.
type Onboarding interface {
	// NativeUnit is the unit the system counts its balance in: "quota" or "usd".
	NativeUnit() string
	// LoginFields are the members of Credentials the system's login takes. A site that asks for a CAPTCHA cannot be
	// logged into by a program, so for it only the tokens a person made by hand are taken.
	LoginFields(captcha bool) []LoginField
	// NewLogin checks the credentials and makes the login to seal, or a *LoginError.
	NewLogin(c Credentials) (Login, error)
}
