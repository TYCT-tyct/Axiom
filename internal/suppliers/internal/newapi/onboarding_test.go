package newapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"
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

// TestCreateKey: the token is made with no quota, expiry or model limit, and its id is found by the name because the
// create answer has none. A narrow token, a refusal and a supplier error are told apart.
//
//nolint:cyclop // each refusal is one case in the table
func TestCreateKey(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	t.Run("made and found", func(t *testing.T) {
		t.Parallel()
		f := newFake()
		f.routes["POST /api/token/"] = answer(200, `{"success":true,"message":""}`)
		f.routes["GET /api/token/search?keyword=axiom-1-2&page_size=100"] = answer(200,
			`{"success":true,"data":{"items":[{"id":12,"name":"axiom-1-2","group":"g"}]}}`)
		ref, err := open(t, f, now).(adapter.KeyMaker).CreateKey(t.Context(), "g", "axiom-1-2")
		if err != nil || ref != "12" {
			t.Fatalf("CreateKey = %q, %v", ref, err)
		}
		made := f.sent[0]
		if made.Method != http.MethodPost || made.Path != "/api/token/" || !strings.Contains(string(made.Body), `"unlimited_quota":true`) ||
			!strings.Contains(string(made.Body), `"model_limits_enabled":false`) || !strings.Contains(string(made.Body), `"expired_time":-1`) {
			t.Errorf("the create request: %+v", made)
		}
	})
	t.Run("the login may not manage keys", func(t *testing.T) {
		t.Parallel()
		f := newFake()
		f.routes["POST /api/token/"] = answer(403,
			`{"code":"ACCESS_TOKEN_SCOPE_DENIED","message_params":{"scope":"api_key:write"},"success":false}`)
		_, err := open(t, f, now).(adapter.KeyMaker).CreateKey(t.Context(), "g", "axiom-1-2")
		if !errors.Is(err, adapter.ErrKeyNotPermitted) {
			t.Fatalf("CreateKey = %v, want not permitted", err)
		}
	})
	t.Run("the login may not search keys", func(t *testing.T) {
		t.Parallel()
		f := newFake()
		f.routes["POST /api/token/"] = answer(200, `{"success":true}`)
		f.routes["GET /api/token/search?keyword=axiom-1-2&page_size=100"] = answer(403,
			`{"code":"ACCESS_TOKEN_SCOPE_DENIED","message_params":{"scope":"api_key:read"},"success":false}`)
		_, err := open(t, f, now).(adapter.KeyMaker).CreateKey(t.Context(), "g", "axiom-1-2")
		if !errors.Is(err, adapter.ErrKeyNotPermitted) {
			t.Fatalf("CreateKey = %v, want not permitted", err)
		}
	})
	t.Run("the supplier refuses", func(t *testing.T) {
		t.Parallel()
		f := newFake()
		f.routes["POST /api/token/"] = answer(200, `{"success":false,"message":"no"}`)
		_, err := open(t, f, now).(adapter.KeyMaker).CreateKey(t.Context(), "g", "axiom-1-2")
		if !errors.Is(err, adapter.ErrKeyRefused) {
			t.Fatalf("CreateKey = %v, want refused", err)
		}
	})
	t.Run("not listed yet", func(t *testing.T) {
		t.Parallel()
		f := newFake()
		f.routes["POST /api/token/"] = answer(200, `{"success":true}`)
		f.routes["GET /api/token/search?keyword=axiom-1-2&page_size=100"] = answer(200, `{"success":true,"data":{"items":[]}}`)
		_, err := open(t, f, now).(adapter.KeyMaker).CreateKey(t.Context(), "g", "axiom-1-2")
		if err == nil || errors.Is(err, adapter.ErrKeyNotPermitted) || errors.Is(err, adapter.ErrKeyRefused) {
			t.Fatalf("CreateKey = %v, want a task failure", err)
		}
	})
}

// TestA5xxOnAKeyReadIsRetryable: a gateway error while searching for a just-created key or reading its text is a
// temporary failure, not an unrecognised answer that would halt the key task for good.
func TestA5xxOnAKeyReadIsRetryable(t *testing.T) {
	t.Parallel()
	t.Run("search after create", func(t *testing.T) {
		t.Parallel()
		f := newFake()
		f.routes["POST /api/token/"] = answer(200, `{"success":true}`)
		search := "GET /api/token/search?keyword=axiom-1-2&page_size=100"
		f.routes[search] = answer(503, `{"success":false}`)
		m := open(t, f, now)
		if _, err := m.(adapter.KeyMaker).CreateKey(t.Context(), "g", "axiom-1-2"); !errors.Is(err, errServer) || errors.Is(err, adapter.ErrUnrecognised) {
			t.Fatalf("search = %v, want a retryable server error", err)
		}
		f.routes[search] = answer(200, `{"success":true,"data":{"items":[{"id":12,"name":"axiom-1-2","group":"g"}]}}`)
		ref, err := m.(adapter.KeyMaker).CreateKey(t.Context(), "g", "axiom-1-2")
		if err != nil || ref != "12" {
			t.Fatalf("after recovery CreateKey = %q, %v; want 12", ref, err)
		}
	})
	t.Run("read the text", func(t *testing.T) {
		t.Parallel()
		f := newFake()
		f.routes["POST /api/token/1/key"] = answer(503, `{"success":false}`)
		m := open(t, f, now)
		if _, err := m.KeySecret(t.Context(), "1"); !errors.Is(err, errServer) || errors.Is(err, adapter.ErrUnrecognised) {
			t.Fatalf("KeySecret = %v, want a retryable server error", err)
		}
		f.routes["POST /api/token/1/key"] = sample(t, "28-reveal-key")
		if text, err := m.KeySecret(t.Context(), "1"); err != nil || text.Reveal() == "" {
			t.Fatalf("after recovery KeySecret = %v, %v; want text", text, err)
		}
	})
}
