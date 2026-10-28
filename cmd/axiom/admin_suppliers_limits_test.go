package main

import (
	"net/http"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
)

// TestSetCredentialLimits: the calls a key serves at once and the requests its minute admits are set through the API and
// read back with the supplier, each change recorded; a minute of null is no ceiling of Axiom's.
func TestSetCredentialLimits(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	group := f.group(supplier, "1")
	base := "/suppliers/" + supplier
	var selected adminapi.SupplierGroup
	f.send(http.MethodPut, base+"/groups/"+group, `{"selected":true,"reason":"r"}`, http.StatusOK, &selected)
	var bound adminapi.SupplierCredential
	f.send(http.MethodPost, base+"/credentials", `{"group_id":"`+group+`","upstream_key_ref":"key-7","reason":"r"}`, http.StatusCreated, &bound)
	if bound.RequestsPerMinute != nil {
		t.Errorf("a new binding has a per-minute limit of %d", *bound.RequestsPerMinute)
	}
	limits := base + "/credentials/" + bound.Id + "/limits"
	var changed adminapi.SupplierCredential
	f.send(http.MethodPut, limits, `{"max_concurrency":8,"requests_per_minute":600,"reason":"more"}`, http.StatusOK, &changed)
	want, perMinute := bound, 600
	want.MaxConcurrency, want.RequestsPerMinute = 8, &perMinute
	if diff := cmp.Diff(want, changed); diff != "" {
		t.Errorf("the limits set (-want +got):\n%s", diff)
	}
	f.expectAudit("credential.limits", map[string]string{"reason": "more", "target_id": supplier})
	rec := f.do(http.MethodGet, base, "", as(f.s))
	expectAdmin(t, "getSupplier after the change", rec, http.StatusOK, "")
	var detail adminapi.SupplierDetail
	decode(t, rec, &detail)
	if len(detail.Credentials) != 1 || detail.Credentials[0].MaxConcurrency != 8 ||
		detail.Credentials[0].RequestsPerMinute == nil || *detail.Credentials[0].RequestsPerMinute != 600 {
		t.Errorf("the supplier after the change: %+v", detail.Credentials)
	}
	var cleared adminapi.SupplierCredential
	f.send(http.MethodPut, limits, `{"max_concurrency":8,"requests_per_minute":null,"reason":"none"}`, http.StatusOK, &cleared)
	if cleared.RequestsPerMinute != nil {
		t.Errorf("the minute cleared is still %d", *cleared.RequestsPerMinute)
	}
	f.send(http.MethodPut, limits, `{"max_concurrency":8,"requests_per_minute":600,"reason":"more"}`, http.StatusOK, &changed)
	var omitted adminapi.SupplierCredential
	f.send(http.MethodPut, limits, `{"max_concurrency":8,"reason":"none"}`, http.StatusOK, &omitted)
	if omitted.RequestsPerMinute != nil {
		t.Errorf("the omitted minute is still %d", *omitted.RequestsPerMinute)
	}
	var largest adminapi.SupplierCredential
	f.send(http.MethodPut, limits, `{"max_concurrency":8,"requests_per_minute":100000,"reason":"the ceiling"}`, http.StatusOK, &largest)
	if largest.RequestsPerMinute == nil || *largest.RequestsPerMinute != 100000 {
		t.Errorf("the largest minute: %+v", largest.RequestsPerMinute)
	}
	if n := f.auditCount("credential.limits"); n != "5" {
		t.Errorf("%s audit entries, want 5: three settings and two clearings", n)
	}
}

// TestSetCredentialLimitsRefusals: a credential the supplier lacks, and numbers outside what the contract allows, are
// refused, and nothing is recorded.
func TestSetCredentialLimitsRefusals(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	group := f.group(supplier, "1")
	base := "/suppliers/" + supplier
	var selected adminapi.SupplierGroup
	f.send(http.MethodPut, base+"/groups/"+group, `{"selected":true,"reason":"r"}`, http.StatusOK, &selected)
	var bound adminapi.SupplierCredential
	f.send(http.MethodPost, base+"/credentials", `{"group_id":"`+group+`","upstream_key_ref":"key-7","reason":"r"}`, http.StatusCreated, &bound)
	limits := base + "/credentials/" + bound.Id + "/limits"
	for name, c := range map[string]struct {
		path, body string
		status     int
		code       errorCode
	}{
		"a credential the supplier lacks": {
			base + "/credentials/" + uuid.NewV7().String() + "/limits", `{"max_concurrency":2,"requests_per_minute":null,"reason":"r"}`,
			http.StatusNotFound, codeNotFound,
		},
		"no concurrency":         {limits, `{"max_concurrency":0,"requests_per_minute":null,"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		"no requests a minute":   {limits, `{"max_concurrency":2,"requests_per_minute":0,"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		"a minute not a number":  {limits, `{"max_concurrency":2,"requests_per_minute":"many","reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		"a minute above the cap": {limits, `{"max_concurrency":2,"requests_per_minute":100001,"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		"a concurrency too high": {limits, `{"max_concurrency":2147483648,"requests_per_minute":null,"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
	} {
		expectAdmin(t, name, f.change(f.s, http.MethodPut, c.path, c.body, newKey()), c.status, c.code)
	}
	if n := f.auditCount("credential.limits"); n != "0" {
		t.Errorf("%s audit entries after refusals", n)
	}
}
