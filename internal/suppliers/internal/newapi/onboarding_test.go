package newapi

import (
	"errors"
	"fmt"
	"maps"
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
		f.routes[searchFirst] = answer(200,
			`{"success":true,"data":{"total":1,"items":[{"id":12,"name":"axiom-1-2","group":"g","status":1}]}}`)
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
		f.routes[searchFirst] = answer(403,
			`{"code":"ACCESS_TOKEN_SCOPE_DENIED","message_params":{"scope":"api_key:read"},"success":false}`)
		_, err := open(t, f, now).(adapter.KeyMaker).CreateKey(t.Context(), "g", "axiom-1-2")
		if !madeBut(err, adapter.ErrKeyNotPermitted) {
			t.Fatalf("CreateKey = %v, want not permitted to search a key that was made", err)
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
		f.routes[searchFirst] = answer(200, `{"success":true,"data":{"total":0,"items":[]}}`)
		_, err := open(t, f, now).(adapter.KeyMaker).CreateKey(t.Context(), "g", "axiom-1-2")
		if !errors.Is(err, adapter.ErrKeyMade) || errors.Is(err, adapter.ErrKeyNotPermitted) || errors.Is(err, adapter.ErrKeyRefused) {
			t.Fatalf("CreateKey = %v, want a key made and not listed yet", err)
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
		search := searchFirst
		f.routes[search] = answer(503, `{"success":false}`)
		m := open(t, f, now)
		if _, err := m.(adapter.KeyMaker).CreateKey(t.Context(), "g", "axiom-1-2"); !errors.Is(err, errServer) || !errors.Is(err, adapter.ErrKeyMade) || errors.Is(err, adapter.ErrUnrecognised) {
			t.Fatalf("search = %v, want a retryable server error", err)
		}
		f.routes[search] = answer(200, `{"success":true,"data":{"total":1,"items":[{"id":12,"name":"axiom-1-2","group":"g","status":1}]}}`)
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

// searchFirst is the first page of new-api's search for the key name the tests make.
const searchFirst = "GET /api/token/search?keyword=axiom-1-2&p=1&page_size=100"

// TestKeyNamed: only a key whose name and group are the same whole counts, the oldest of two; every page of the search
// is read to its total, and a search that ends short of it, lists a key twice, or cannot be read, is no absence.
func TestKeyNamed(t *testing.T) {
	t.Parallel()
	item := func(id int, name, group string) string {
		return fmt.Sprintf(`{"id":%d,"name":%q,"group":%q,"status":1}`, id, name, group)
	}
	page := func(total int, items ...string) adapter.Response {
		return answer(200, fmt.Sprintf(`{"success":true,"data":{"total":%d,"items":[%s]}}`, total, strings.Join(items, ",")))
	}
	second := "GET /api/token/search?keyword=axiom-1-2&p=2&page_size=100"
	many := make([]string, 100)
	for i := range many {
		many[i] = item(500+i, "axiom-1-2", "other")
	}
	for _, c := range []struct {
		name   string
		routes map[string]adapter.Response
		ref    adapter.KeyRef
		found  bool
		broken bool
	}{
		{name: "none", routes: map[string]adapter.Response{searchFirst: page(0)}},
		{name: "another group's", routes: map[string]adapter.Response{searchFirst: page(1, item(3, "axiom-1-2", "vip"))}},
		{name: "the oldest of two", routes: map[string]adapter.Response{searchFirst: page(2, item(9, "axiom-1-2", "g"), item(4, "axiom-1-2", "g"))}, ref: "4", found: true},
		{name: "on the second page", routes: map[string]adapter.Response{searchFirst: page(101, many...), second: page(101, item(7, "axiom-1-2", "g"))}, ref: "7", found: true},
		{name: "a search that ends short", routes: map[string]adapter.Response{searchFirst: page(101, many...), second: page(101)}, broken: true},
		{name: "a search that lists a key twice", routes: map[string]adapter.Response{searchFirst: page(101, many...), second: page(101, many[0])}, broken: true},
		{name: "a search that fails", routes: map[string]adapter.Response{searchFirst: answer(503, `{"success":false}`)}, broken: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFake()
			maps.Copy(f.routes, c.routes)
			key, found, _, err := open(t, f, now).(adapter.KeyMaker).KeyNamed(t.Context(), "g", "axiom-1-2", nil)
			if (err != nil) != c.broken || found != c.found || key.Ref != c.ref {
				t.Fatalf("KeyNamed = %+v, %v, %v; want %q, %v, broken %v", key, found, err, c.ref, c.found, c.broken)
			}
		})
	}
}

// TestKeyByRef: a key is read as the listing reads it; new-api's answer that the account has no such key is the key
// gone, and no other failed answer is.
func TestKeyByRef(t *testing.T) {
	t.Parallel()
	for body, want := range map[string]string{
		`{"success":true,"data":{"id":9,"name":"n","group":"g","status":2}}`: "found 9 disabled",
		`{"success":false,"message":"record not found"}`:                     "gone",
		`{"success":false,"message":"something else"}`:                       "broken",
	} {
		f := newFake()
		f.routes["GET /api/token/9"] = answer(200, body)
		key, found, err := open(t, f, now).(adapter.KeyMaker).KeyByRef(t.Context(), "9")
		got := "gone"
		switch {
		case err != nil:
			got = "broken"
		case found:
			got = fmt.Sprintf("found %s %s", key.Ref, statusWord(key.Status))
		}
		if got != want {
			t.Errorf("KeyByRef after %s = %s (%v), want %s", body, got, err, want)
		}
	}
}

func statusWord(s adapter.KeyStatus) string {
	if s == adapter.KeyDisabled {
		return "disabled"
	}
	return "other"
}

// madeBut is a failure to find a key the supplier made, for the reason why.
func madeBut(err, why error) bool {
	return errors.Is(err, adapter.ErrKeyMade) && errors.Is(err, why)
}

// TestCreateKeyWithAnAnswerThatDoesNotSay: an answer without success, a proxy's say, may follow a key new-api made, so
// it is no refusal.
func TestCreateKeyWithAnAnswerThatDoesNotSay(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.routes["POST /api/token/"] = answer(200, `{}`)
	_, err := open(t, f, time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)).(adapter.KeyMaker).CreateKey(t.Context(), "g", "axiom-1-2")
	if err == nil || errors.Is(err, adapter.ErrKeyRefused) {
		t.Fatalf("CreateKey = %v, want a failure that is no refusal", err)
	}
}
