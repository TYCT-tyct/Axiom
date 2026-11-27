package sub2api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// TestKeyNamed: sub2api's search matches part of a name, so only the key with the same whole name in the group counts,
// the oldest of two, over every page; an empty page before the last is no absence; a key read by its id is as listed,
// and one sub2api answers it does not have is gone.
func TestKeyNamed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	page := func(n, pages int, items ...string) adapter.Response {
		return adapter.Response{Status: 200, Body: []byte(fmt.Sprintf(`{"code":0,"data":{"items":[%s],"total":%d,"page":%d,"page_size":100,"pages":%d}}`,
			strings.Join(items, ","), len(items), n, pages))}
	}
	first, second := "GET /api/v1/keys?page=1&page_size=100&search=axiom-1-2", "GET /api/v1/keys?page=2&page_size=100&search=axiom-1-2"
	srv := routes(map[string]adapter.Response{
		first: page(1, 2,
			`{"id":5,"name":"axiom-1-2-old","group_id":1,"status":"active"}`, `{"id":9,"name":"axiom-1-2","group_id":1,"status":"active"}`),
		second: page(2, 2,
			`{"id":7,"name":"axiom-1-2","group_id":1,"status":"disabled"}`, `{"id":3,"name":"axiom-1-2","group_id":2,"status":"active"}`),
		"GET /api/v1/keys/9": {Status: 200, Body: []byte(`{"code":0,"data":{"id":9,"name":"axiom-1-2","group_id":1,"status":"active","key":"sk-9"}}`)},
		"GET /api/v1/keys/7": {Status: 404, Body: []byte(`{"code":404,"reason":"API_KEY_NOT_FOUND","message":"api key not found"}`)},
	})
	m := manager0(t, srv, login(t, now.Add(24*time.Hour)))
	maker := m.(adapter.KeyMaker)
	key, found, _, err := maker.KeyNamed(t.Context(), "1", "axiom-1-2", nil)
	if err != nil || !found || key.Ref != "7" || key.Status != adapter.KeyDisabled {
		t.Fatalf("KeyNamed = %+v, %v, %v; want the oldest key of the name in the group, as listed", key, found, err)
	}
	if key, found, err := maker.KeyByRef(t.Context(), "9"); err != nil || !found || key.Status != adapter.KeyActive {
		t.Errorf("KeyByRef of a key sub2api has = %+v, %v, %v", key, found, err)
	}
	if _, found, err := maker.KeyByRef(t.Context(), "7"); err != nil || found {
		t.Errorf("KeyByRef of a key sub2api does not have = %v, %v, want gone", found, err)
	}
	short := routes(map[string]adapter.Response{first: page(1, 2), second: page(2, 2, `{"id":9,"name":"axiom-1-2","group_id":1,"status":"active"}`)})
	if _, found, _, err := manager0(t, short, login(t, now.Add(24*time.Hour))).(adapter.KeyMaker).KeyNamed(t.Context(), "1", "axiom-1-2", nil); err == nil || found {
		t.Errorf("KeyNamed past an empty first page of two = %v, %v; want no answer", found, err)
	}
}
