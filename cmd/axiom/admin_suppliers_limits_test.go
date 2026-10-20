package main

import (
	"net/http"
	"testing"
	"uuid"

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
	if changed.Id != bound.Id || changed.MaxConcurrency != 8 || changed.RequestsPerMinute == nil || *changed.RequestsPerMinute != 600 {
		t.Errorf("the limits set: %+v", changed)
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
	f.send(http.MethodPut, limits, `{"max_concurrency":8,"requests_per_minute":null,"reason":"none"}`, http.StatusOK, &changed)
	if changed.RequestsPerMinute != nil {
		t.Errorf("the minute cleared is still %d", *changed.RequestsPerMinute)
	}
	if n := f.auditCount("credential.limits"); n != "2" {
		t.Errorf("%s audit entries, want 2: the change and the clearing", n)
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
		"no minute at all":       {limits, `{"max_concurrency":2,"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		"a concurrency too high": {limits, `{"max_concurrency":2147483648,"requests_per_minute":null,"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
	} {
		expectAdmin(t, name, f.change(f.s, http.MethodPut, c.path, c.body, newKey()), c.status, c.code)
	}
	if n := f.auditCount("credential.limits"); n != "0" {
		t.Errorf("%s audit entries after refusals", n)
	}
}
