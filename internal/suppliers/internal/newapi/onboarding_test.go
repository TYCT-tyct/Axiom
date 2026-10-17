package newapi

import (
	"errors"
	"slices"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

var _ adapter.Onboarding = (*Adapter)(nil)

// TestNewLogin: a personal access token makes a login that reads back as it was given; anything else is a refusal that
// names the member and holds no value.
func TestNewLogin(t *testing.T) {
	t.Parallel()
	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	login, err := New().NewLogin(adapter.Credentials{AccessToken: "pat-secret", AccessTokenExpiresAt: expires})
	if err != nil || login.Kind != LoginKind || !login.ExpiresAt.Equal(expires) {
		t.Fatalf("a login: %+v %v", login, err)
	}
	read, err := DecodeLogin(login.Plaintext)
	if err != nil || read.PAT.Reveal() != "pat-secret" || !read.ExpiresAt.Equal(expires) {
		t.Errorf("the login read back: %+v %v", read, err)
	}
	if never, err := New().NewLogin(adapter.Credentials{AccessToken: "pat-secret"}); err != nil || !never.ExpiresAt.IsZero() {
		t.Errorf("a token that does not end: %+v %v", never, err)
	}
	_, err = New().NewLogin(adapter.Credentials{Email: "a@b.c"})
	if loginErr, ok := errors.AsType[*adapter.LoginError](err); !ok || loginErr.Field != adapter.FieldAccessToken {
		t.Errorf("no token: %v", err)
	}
}

// TestOnboardingFacts: the unit of the balance and the members a login takes, the same with or without a CAPTCHA.
func TestOnboardingFacts(t *testing.T) {
	t.Parallel()
	if got := New().NativeUnit(); got != "quota" {
		t.Errorf("the unit: %q", got)
	}
	for _, captcha := range []bool{false, true} {
		if got := New().LoginFields(captcha); !slices.Equal(got, []adapter.LoginField{adapter.FieldAccessToken, adapter.FieldAccessTokenExpiresAt}) {
			t.Errorf("the login fields (captcha %v): %v", captcha, got)
		}
	}
}
