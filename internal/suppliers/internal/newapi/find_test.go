package newapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// TestFindKey: the search by text answers masked keys; the one whose text read back is the same whole is the key, so a
// key that only matches part of the text is not; a search that answers too many is no answer, and an error of the search
// passes on only its kind, never the text the request carried.
func TestFindKey(t *testing.T) {
	t.Parallel()
	search := func(text string) string {
		return "GET /api/token/search?" + url.Values{"token": {strings.TrimPrefix(text, "sk-")}, "page_size": {"6"}}.Encode()
	}
	items := `{"success":true,"data":{"items":[{"id":7,"name":"mine","group":"vip","status":1},{"id":8,"name":"other","group":"default","status":2}]}}`
	cases := []struct {
		name  string
		text  string
		route map[string]adapter.Response
		err   error
		found bool
		key   adapter.KeyState
	}{
		{
			name: "the second candidate is the key", text: "sk-abc_def",
			route: map[string]adapter.Response{
				search("sk-abc_def"):    answer(http.StatusOK, items),
				"POST /api/token/7/key": answer(http.StatusOK, `{"success":true,"data":{"key":"abcXdef"}}`),
				"POST /api/token/8/key": answer(http.StatusOK, `{"success":true,"data":{"key":"abc_def"}}`),
			},
			found: true, key: adapter.KeyState{Ref: "8", GroupRef: "default", Name: "other", Status: adapter.KeyDisabled},
		},
		{
			name: "no candidate is the key", text: "sk-abc%",
			route: map[string]adapter.Response{
				search("sk-abc%"):       answer(http.StatusOK, items),
				"POST /api/token/7/key": answer(http.StatusOK, `{"success":true,"data":{"key":"abcXdef"}}`),
				"POST /api/token/8/key": answer(http.StatusOK, `{"success":true,"data":{"key":"abc_def"}}`),
			},
		},
		{
			name: "too many candidates", text: "sk-a%",
			route: map[string]adapter.Response{search("sk-a%"): answer(http.StatusOK, `{"success":true,"data":{"items":[`+
				strings.Repeat(`{"id":1,"name":"n","group":"g","status":1},`, 5)+`{"id":1,"name":"n","group":"g","status":1}]}}`)},
			err: adapter.ErrUnrecognised,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newFake()
			c.routes = tc.route
			key, found, err := open(t, c, time.Now()).(adapter.KeyFinder).FindKey(t.Context(), secret.New(tc.text))
			if !errors.Is(err, tc.err) || found != tc.found || key != tc.key {
				t.Errorf("FindKey = %+v, %t, %v; want %+v, %t, %v", key, found, err, tc.key, tc.found, tc.err)
			}
		})
	}
	c := newFake()
	c.errs[search("sk-secret-text")] = errors.New("dial tcp: GET /api/token/search?token=secret-text: refused")
	_, _, err := open(t, c, time.Now()).(adapter.KeyFinder).FindKey(t.Context(), secret.New("sk-secret-text"))
	if err == nil || strings.Contains(err.Error(), "secret-text") {
		t.Errorf("the search's error: %v", err)
	}
}
