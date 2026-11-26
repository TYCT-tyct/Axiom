package sub2api

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// TestFindKey: the search of the account's keys answers every key whose name or text contains the text; the key is the
// one whose text is the same whole, so a key that only contains it, or matches it in another case, is not. When more
// match than one page holds and none of those is the key, there is no answer; an error of the search passes on only its
// kind, never the text the request carried.
func TestFindKey(t *testing.T) {
	t.Parallel()
	search := func(text string) string {
		return "GET /api/v1/keys?" + url.Values{"search": {text}, "page": {"1"}, "page_size": {"20"}}.Encode()
	}
	found := func(total int, keys ...string) adapter.Response {
		return adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"items":[` + strings.Join(keys, ",") +
			`],"total":` + strconv.Itoa(total) + `,"page":1,"page_size":20,"pages":1}}`)}
	}
	long := `{"id":7,"name":"sk-abc-def","key":"sk-abc-def-longer","group_id":3,"status":"active"}`
	upper := `{"id":8,"name":"other","key":"SK-ABC-DEF","group_id":4,"status":"active"}`
	mine := `{"id":9,"name":"mine","key":"sk-abc-def","group_id":5,"status":"quota_exhausted"}`
	cases := []struct {
		name  string
		route adapter.Response
		err   error
		found bool
		key   adapter.KeyRef
		group string
	}{
		{name: "the key among those containing it", route: found(3, long, upper, mine), found: true, key: "9", group: "5"},
		{name: "only keys containing it", route: found(2, long, upper)},
		{name: "more than a page, the key not on it", route: found(21, long, upper), err: adapter.ErrUnrecognised},
		{name: "more than a page, the key on it", route: found(21, long, mine), found: true, key: "9", group: "5"},
		{name: "an answer not understood", route: adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"items":[]}}`)}, err: adapter.ErrUnrecognised},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := routes(map[string]adapter.Response{search("sk-abc-def"): tc.route})
			m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
			key, ok, err := m.(adapter.KeyFinder).FindKey(t.Context(), secret.New("sk-abc-def"))
			if !errors.Is(err, tc.err) || ok != tc.found || key.Ref != tc.key || key.GroupRef != tc.group {
				t.Errorf("FindKey = %+v, %t, %v; want %s in %q, %t, %v", key, ok, err, tc.key, tc.group, tc.found, tc.err)
			}
		})
	}
	srv := routes(map[string]adapter.Response{})
	_, _, err := manager0(t, srv, login(t, epoch.Add(24*time.Hour))).(adapter.KeyFinder).FindKey(t.Context(), secret.New("sk-secret-text"))
	if err == nil || strings.Contains(err.Error(), "secret-text") {
		t.Errorf("the search's error: %v", err)
	}
}
