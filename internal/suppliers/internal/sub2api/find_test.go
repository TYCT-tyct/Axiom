package sub2api

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// TestFindKey: the key is found in the account's list of keys, read page by page; it is the one whose text is the same
// whole, so a key that only contains the text, or has it in another case, is not. No request carries the text.
func TestFindKey(t *testing.T) {
	t.Parallel()
	pageOf := func(n, pages int, keys ...string) adapter.Response {
		return adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"items":[` + strings.Join(keys, ",") +
			`],"total":3,"page":` + strconv.Itoa(n) + `,"page_size":100,"pages":` + strconv.Itoa(pages) + `}}`)}
	}
	path := func(n int) string { return "GET /api/v1/keys?page=" + strconv.Itoa(n) + "&page_size=100" }
	long := `{"id":7,"name":"sk-abc-def","key":"sk-abc-def-longer","group_id":3,"status":"active"}`
	upper := `{"id":8,"name":"other","key":"SK-ABC-DEF","group_id":4,"status":"active"}`
	mine := `{"id":9,"name":"mine","key":"sk-abc-def","group_id":5,"status":"quota_exhausted"}`
	cases := []struct {
		name  string
		route map[string]adapter.Response
		err   error
		found bool
		key   adapter.KeyRef
		group string
	}{
		{name: "on the first page", route: map[string]adapter.Response{path(1): pageOf(1, 1, long, upper, mine)}, found: true, key: "9", group: "5"},
		{name: "on the second page", route: map[string]adapter.Response{path(1): pageOf(1, 2, long, upper), path(2): pageOf(2, 2, mine)}, found: true, key: "9", group: "5"},
		{name: "only keys containing it", route: map[string]adapter.Response{path(1): pageOf(1, 1, long, upper)}},
		{name: "an answer not understood", route: map[string]adapter.Response{path(1): {Status: 200, Body: []byte(`{"code":0,"data":{"items":[]}}`)}}, err: adapter.ErrUnrecognised},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := routes(tc.route)
			m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
			key, ok, err := m.(adapter.KeyFinder).FindKey(t.Context(), secret.New("sk-abc-def"))
			if !errors.Is(err, tc.err) || ok != tc.found || key.Ref != tc.key || key.GroupRef != tc.group {
				t.Errorf("FindKey = %+v, %t, %v; want %s in %q, %t, %v", key, ok, err, tc.key, tc.group, tc.found, tc.err)
			}
			for _, r := range srv.requests {
				if strings.Contains(r.Path+string(r.Body), "abc-def") {
					t.Errorf("%s %s carries the key's text", r.Method, r.Path)
				}
			}
		})
	}
}
