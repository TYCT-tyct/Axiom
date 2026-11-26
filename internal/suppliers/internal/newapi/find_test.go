package newapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// TestFindKey: the key is found in the account's listing, which masks each key to its first and last four characters;
// a key whose mask is the text's is read whole, and only one that is the same whole is the key, with its status and
// model limits. Too many keys with the text's mask is no answer. No request carries the text or any part of it: new-api
// logs every request's path and query.
func TestFindKey(t *testing.T) {
	t.Parallel()
	const text = "sk-abcdXXXXXXXXXXXXwxyz"
	listing := func(items string) adapter.Response {
		return answer(http.StatusOK, `{"success":true,"data":{"total":2,"items":[`+items+`]}}`)
	}
	token := func(id, group, key string, status string) string {
		return `{"id":` + id + `,"name":"n` + id + `","group":"` + group + `","status":` + status + `,"key":"` + key +
			`","expired_time":-1,"model_limits_enabled":true,"model_limits":"gpt-a"}`
	}
	page := "GET /api/token/?p=1&page_size=100"
	cases := []struct {
		name  string
		route map[string]adapter.Response
		err   error
		found bool
		key   adapter.KeyState
	}{
		{
			name: "the second with the mask is the key",
			route: map[string]adapter.Response{
				page:                    listing(token("7", "vip", "abcd**********wxyz", "1") + "," + token("8", "default", "abcd**********wxyz", "2") + "," + token("9", "vip", "zzzz**********wxyz", "1")),
				"POST /api/token/7/key": answer(http.StatusOK, `{"success":true,"data":{"key":"abcdYYYYYYYYYYYYwxyz"}}`),
				"POST /api/token/8/key": answer(http.StatusOK, `{"success":true,"data":{"key":"abcdXXXXXXXXXXXXwxyz"}}`),
			},
			found: true, key: adapter.KeyState{Ref: "8", GroupRef: "default", Name: "n8", Status: adapter.KeyDisabled, Policy: &adapter.KeyPolicy{Models: []string{"gpt-a"}}},
		},
		{
			name: "none with the mask is the key",
			route: map[string]adapter.Response{
				page:                    listing(token("7", "vip", "abcd**********wxyz", "1")),
				"POST /api/token/7/key": answer(http.StatusOK, `{"success":true,"data":{"key":"abcdYYYYYYYYYYYYwxyz"}}`),
			},
		},
		{name: "none has the mask", route: map[string]adapter.Response{page: listing(token("9", "vip", "zzzz**********wxyz", "1"))}},
		{
			name:  "too many with the mask",
			route: map[string]adapter.Response{page: listing(strings.TrimSuffix(strings.Repeat(token("7", "g", "abcd**********wxyz", "1")+",", maxFound+1), ","))},
			err:   adapter.ErrUnrecognised,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFake()
			c.routes = tc.route
			key, found, err := open(t, c, time.Now()).(adapter.KeyFinder).FindKey(t.Context(), secret.New(text))
			if !errors.Is(err, tc.err) || found != tc.found || !sameKey(key, tc.key) {
				t.Errorf("FindKey = %+v, %t, %v; want %+v, %t, %v", key, found, err, tc.key, tc.found, tc.err)
			}
			for _, r := range c.sent {
				if strings.Contains(r.Path+string(r.Body), "abcd") || strings.Contains(r.Path+string(r.Body), "wxyz") {
					t.Errorf("%s %s carries the key's text", r.Method, r.Path)
				}
			}
		})
	}
}

// sameKey compares two key states with their policies' content.
func sameKey(a, b adapter.KeyState) bool {
	if (a.Policy == nil) != (b.Policy == nil) {
		return false
	}
	if a.Policy != nil && strings.Join(a.Policy.Models, ",") != strings.Join(b.Policy.Models, ",") {
		return false
	}
	a.Policy, b.Policy = nil, nil
	return a == b
}

// TestMaskKey is new-api's MaskTokenKey (model/token.go): the listing's masks are compared with it.
func TestMaskKey(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"": "", "abcd": "****", "abcdefgh": "ab****gh", "abcdefghi": "abcd**********fghi",
	} {
		if got := maskKey(in); got != want {
			t.Errorf("maskKey(%q) = %q, want %q", in, got, want)
		}
	}
}
