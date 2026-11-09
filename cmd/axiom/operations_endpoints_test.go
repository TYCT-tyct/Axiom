package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/operationsapi"
)

// switchEndpointAs asks, as the agent, to turn an endpoint of supplier on or off under key.
func (f supplierFixture) switchEndpointAs(token, supplier, endpoint string, enabled bool, key, extra string) *httptest.ResponseRecorder {
	f.t.Helper()
	body := fmt.Sprintf(`{"enabled":%t,"reason":"it timed out all morning"%s}`, enabled, extra)
	return f.agentSend(token, http.MethodPut, "/suppliers/"+supplier+"/endpoints/"+endpoint+"/enabled", body, idempotent(key))
}

// routed says whether this node routes to the endpoint after it reloads.
func (f supplierFixture) routed(endpoint string) bool {
	f.t.Helper()
	if err := f.upstreams.Reload(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
	routable, err := f.upstreams.Routable()
	if err != nil {
		f.t.Fatal(err)
	}
	for _, r := range routable {
		if r.ID.String() == endpoint {
			return true
		}
	}
	return false
}

// endpointFixture is a supplier with one routed endpoint and an agent that may switch it.
func endpointFixture(t *testing.T) (f supplierFixture, supplier, endpoint, token string) {
	t.Helper()
	f = newSupplierFixture(t)
	endpoint = f.seedTestEndpoint("https://api.example.com")
	supplier = strings.TrimSpace(f.text(`SELECT supplier_id::text FROM suppliers.endpoints WHERE id = '` + endpoint + `'`))
	if !f.routed(endpoint) {
		t.Fatal("the seeded endpoint is not routed")
	}
	return f, supplier, endpoint, f.moneyAgent(false)
}

// TestOperationsEndpointSwitchRefusals: a preview, an endpoint the supplier lacks and a credential without
// suppliers.manage leave no operation and no switch, and the endpoint stays routed.
func TestOperationsEndpointSwitchRefusals(t *testing.T) {
	t.Parallel()
	f, supplier, endpoint, token := endpointFixture(t)
	rec := f.switchEndpointAs(token, supplier, endpoint, false, newKey(), `,"dry_run":true`)
	if preview := agentOperation(t, rec); rec.Code != http.StatusOK || !preview.DryRun || preview.State != operationsapi.OperationStateApproved {
		t.Errorf("the preview: %d %s", rec.Code, rec.Body)
	}
	expectAdmin(t, "an endpoint the supplier lacks", f.switchEndpointAs(token, supplier, uuid.NewV7().String(), false, newKey(), ""),
		http.StatusNotFound, codeNotFound)
	_, reader := f.makeCredential(f.s, `{"name":"reader","permissions":["suppliers.view"],"platform":true,
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"patrol only"}`)
	expectAdmin(t, "a credential without suppliers.manage", f.switchEndpointAs(reader, supplier, endpoint, false, newKey(), ""),
		http.StatusForbidden, codeForbidden)
	if got := f.text(`SELECT count(*)::text FROM operations.operations`); got != "0" || f.auditCount("supplier.endpoint.enabled") != "0" || !f.routed(endpoint) {
		t.Errorf("%s operations after the refusals and the preview, or the endpoint changed", got)
	}
}

// TestOperationsEndpointSwitch: the agent turns a routed endpoint off at once, as its own, and routing leaves it; a
// retry under the key finds the operation and the key with another request is refused. The owner turns it back on in
// the admin.
func TestOperationsEndpointSwitch(t *testing.T) {
	t.Parallel()
	f, supplier, endpoint, token := endpointFixture(t)
	key := newKey()
	rec := f.switchEndpointAs(token, supplier, endpoint, false, key, "")
	off := agentOperation(t, rec)
	if rec.Code != http.StatusOK || off.State != operationsapi.OperationStateSucceeded || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("turning it off: %d %s", rec.Code, rec.Body)
	}
	if f.routed(endpoint) {
		t.Error("the endpoint is still routed after it was turned off")
	}
	rec = f.switchEndpointAs(token, supplier, endpoint, false, key, "")
	if again := agentOperation(t, rec); rec.Header().Get("Idempotency-Replayed") != "true" || *again.OperationId != *off.OperationId {
		t.Errorf("the retry: %d %s", rec.Code, rec.Body)
	}
	expectAdmin(t, "the key with another request", f.switchEndpointAs(token, supplier, endpoint, true, key, ""), http.StatusConflict, codeIdempotencyReuse)
	f.expectAudit("supplier.endpoint.enabled", map[string]string{
		"actor_kind": "operations_agent", "detail->>'from'": "true", "detail->>'to'": "false", "reason": "it timed out all morning",
	})

	rec = f.change(f.s, http.MethodPut, "/suppliers/"+supplier+"/endpoints/"+endpoint+"/enabled", `{"enabled":true,"reason":"it recovered"}`, newKey())
	expectAdmin(t, "setSupplierEndpointEnabled", rec, http.StatusOK, "")
	var on adminapi.SupplierEndpoint
	decode(t, rec, &on)
	if !on.Enabled || !f.routed(endpoint) {
		t.Errorf("the owner turned it on: %+v", on)
	}
	if got := f.text(`SELECT string_agg(actor_kind, ',' ORDER BY at, id) FROM platform.audit_log WHERE action = 'supplier.endpoint.enabled'`); got != "operations_agent,user" {
		t.Errorf("the switches were made by %s", got)
	}
}

// TestEnablingAnEndpointResetsItsBreaker: a person turning an endpoint on in the admin closes its breaker in the same
// transaction and records the reset; the agent turning it on, or a person turning it off, leaves the breaker open.
func TestEnablingAnEndpointResetsItsBreaker(t *testing.T) {
	t.Parallel()
	f, supplier, endpoint, token := endpointFixture(t)
	breaker := func() string {
		return f.text(`SELECT format('%s %s %s', opened_at > 0, warned, backoff) FROM quality.endpoint_health WHERE endpoint_id = '` + endpoint + `'`)
	}
	f.text(`INSERT INTO quality.endpoint_health (endpoint_id,epoch,opened_at,next_probe_at,backoff,failures,sources,warned,cause,
		request_id,probe_owner,probe_credential,mode) VALUES ('` + endpoint + `',3,1,2,4,5,2,true,'upstream_error','00000000-0000-0000-0000-000000000000','00000000-0000-0000-0000-000000000000','00000000-0000-0000-0000-000000000000','enforce')
		RETURNING 'tripped'`)
	switchAsOwner := func(enabled bool) {
		t.Helper()
		body := fmt.Sprintf(`{"enabled":%t,"reason":"checked by hand"}`, enabled)
		expectAdmin(t, "setSupplierEndpointEnabled", f.change(f.s, http.MethodPut, "/suppliers/"+supplier+"/endpoints/"+endpoint+"/enabled", body, newKey()), http.StatusOK, "")
	}

	if rec := f.switchEndpointAs(token, supplier, endpoint, true, newKey(), ""); rec.Code != http.StatusOK || breaker() != "t t 4" {
		t.Errorf("the agent turning it on: %d, breaker %s", rec.Code, breaker())
	}
	switchAsOwner(false)
	if got := breaker(); got != "t t 4" {
		t.Errorf("a person turning it off changed the breaker: %s", got)
	}
	switchAsOwner(true)
	if got := breaker(); got != "f f 0" {
		t.Errorf("a person turning it on left the breaker: %s", got)
	}
	if got := f.text(`SELECT string_agg(kind || ':' || epoch, ',') FROM quality.health_events WHERE endpoint_id = '` + endpoint + `'`); got != "reset:4" {
		t.Errorf("the events are %s", got)
	}
}
