package main

import (
	"net/http"
	"testing"

	"axiom/internal/platform/adminapi"
)

// TestAdminRetention: the default window, a save with its audit entry, replay, and the contract's refusals.
func TestAdminRetention(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	rec := f.do(http.MethodGet, "/retention", "", as(s))
	expectAdmin(t, "getRetention", rec, http.StatusOK, "")
	var retention adminapi.Retention
	decode(t, rec, &retention)
	if retention.HotDays == nil || *retention.HotDays != 90 {
		t.Fatalf("default window: %s", rec.Body)
	}
	key := newKey()
	body := `{"hot_days":30,"reason":"keep less"}`
	expectAdmin(t, "setRetention", f.change(s, http.MethodPut, "/retention", body, key), http.StatusCreated, "")
	replay := f.change(s, http.MethodPut, "/retention", body, key)
	if replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("retention did not replay")
	}
	rec = f.do(http.MethodGet, "/retention", "", as(s))
	decode(t, rec, &retention)
	if retention.HotDays == nil || *retention.HotDays != 30 {
		t.Fatalf("saved window: %s", rec.Body)
	}
	f.expectAudit("retention.update", map[string]string{"reason": "keep less", "detail->>'hot_days'": "30"})
	for _, bad := range []string{`{"hot_days":-1,"reason":"negative"}`, `{"hot_days":3651,"reason":"too long"}`} {
		expectAdmin(t, "invalid retention", f.change(s, http.MethodPut, "/retention", bad, newKey()), http.StatusBadRequest, codeInvalidRequest)
	}
}
