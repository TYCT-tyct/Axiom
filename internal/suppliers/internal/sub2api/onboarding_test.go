package sub2api

import (
	"errors"
	"slices"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

var _ adapter.Onboarding = (*Adapter)(nil)

// TestNewLogin: tokens, or an email and a password, or both make a login that reads back as it was given; a half of
// either is a refusal that names the member and holds no value.
func TestNewLogin(t *testing.T) {
	t.Parallel()
	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	for name, c := range map[string]adapter.Credentials{
		"tokens":   {AccessToken: "at", RefreshToken: "rt", AccessTokenExpiresAt: expires},
		"password": {Email: "ops@example.com", Password: "pw"},
		"both":     {Email: "ops@example.com", Password: "pw", AccessToken: "at", RefreshToken: "rt", AccessTokenExpiresAt: expires},
	} {
		login, err := New().NewLogin(c)
		if err != nil || login.Kind != LoginKind || !login.ExpiresAt.Equal(c.AccessTokenExpiresAt) {
			t.Errorf("%s: %+v %v", name, login, err)
			continue
		}
		read, err := DecodeLogin(login.Plaintext)
		if err != nil || read.Email != c.Email || read.Password.Reveal() != c.Password || read.Access.Reveal() != c.AccessToken ||
			read.Refresh.Reveal() != c.RefreshToken {
			t.Errorf("%s: read back %+v %v", name, read, err)
		}
	}
}

// TestNewLoginRefusals: a half of either way in names the member it lacks.
func TestNewLoginRefusals(t *testing.T) {
	t.Parallel()
	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	for name, c := range map[string]struct {
		in    adapter.Credentials
		field adapter.LoginField
	}{
		"nothing":               {adapter.Credentials{}, adapter.FieldAccessToken},
		"an email alone":        {adapter.Credentials{Email: "a@b.c"}, adapter.FieldPassword},
		"a password alone":      {adapter.Credentials{Password: "pw"}, adapter.FieldPassword},
		"an access token alone": {adapter.Credentials{AccessToken: "at", AccessTokenExpiresAt: expires}, adapter.FieldRefreshToken},
		"a refresh token alone": {adapter.Credentials{RefreshToken: "rt"}, adapter.FieldRefreshToken},
		"tokens without expiry": {adapter.Credentials{AccessToken: "at", RefreshToken: "rt"}, adapter.FieldAccessTokenExpiresAt},
	} {
		_, err := New().NewLogin(c.in)
		if loginErr, ok := errors.AsType[*adapter.LoginError](err); !ok || loginErr.Field != c.field {
			t.Errorf("%s: %v, want a refusal of %s", name, err, c.field)
		}
	}
}

// TestOnboardingFacts: the unit of the balance and the members a login takes, narrowed for a CAPTCHA.
func TestOnboardingFacts(t *testing.T) {
	t.Parallel()
	if got := New().NativeUnit(); got != "usd" {
		t.Errorf("the unit: %q", got)
	}
	if got := New().LoginFields(true); slices.Contains(got, adapter.FieldPassword) || slices.Contains(got, adapter.FieldEmail) || len(got) != 3 {
		t.Errorf("the login fields of a site with a CAPTCHA: %v", got)
	}
	if got := New().LoginFields(false); len(got) != 5 {
		t.Errorf("the login fields: %v", got)
	}
}
