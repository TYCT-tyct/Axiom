package gateway

import (
	"net/http"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// TestServedModelComesFromTheRoute: the route snapshot can be newer than the catalog snapshot the request read. The
// model name is frozen on the entry, so a model listed between the two is still named correctly in the response.
func TestServedModelComesFromTheRoute(t *testing.T) {
	t.Parallel()
	s := &session{c: &Call{Request: protocol.Request{Model: "openrouter/auto"}}}
	if got := s.servedModel(&attempt{entry: routing.Entry{ModelSlug: "acme/new@20260101"}}); got != "acme/new" {
		t.Errorf("the response names %q, want acme/new", got)
	}
}

// saveAlias points a catalog alias at a model.
func saveAlias(t *testing.T, r *rig, name string, model catalog.ModelID) {
	t.Helper()
	if _, err := r.catalog.SaveAlias(t.Context(), catalog.SaveAliasRequest{
		Actor: audit.System("gateway.test"), Key: uuid.NewV7(), Reason: "test", Name: name, Model: model,
	}); err != nil {
		t.Fatal(err)
	}
}

// freeEntry lists acme/alpha:free at a zero price and serves it from an endpoint priced at zero, so it is the cheapest
// route in the rig.
func (r *rig) freeEntry() (catalog.ModelID, routing.Endpoint) {
	r.t.Helper()
	const zeroPrice = `{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`
	free := r.listed("acme/alpha:free", basicSpec, zeroPrice)
	zero, err := catalog.ParseSchedule([]byte(zeroPrice))
	if err != nil {
		r.t.Fatal(err)
	}
	e := r.endpoints["alpha-1"]
	e.ID, e.Model, e.Codename = routing.EndpointID(uuid.NewV7()), free, "alpha-free"
	e.Price = routing.PriceRef{Version: uuid.NewV7(), Schedule: zero}
	r.install(routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}, append(r.routes, e)...)
	return free, e
}

// checkModel asks for a model and expects its OpenRouter ID to be the standard ID.
func checkModel(t *testing.T, r *rig, a account, name, id string) {
	t.Helper()
	resp, body := r.do(http.MethodGet, "/api/v1/models/"+name, a.key, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"id":"`+id+`"`) {
		t.Fatalf("%s: %d %s", name, resp.StatusCode, body)
	}
}

// checkCall names a model and expects the route to begin at the endpoint serving want; it returns the admitted call.
func checkCall(t *testing.T, r *rig, a account, name string, endpoint routing.EndpointID, want catalog.ModelID) *Call {
	t.Helper()
	route, resp, body := r.sendNamed(a, name, "")
	if resp.StatusCode != http.StatusOK || len(route) == 0 || route[0] != endpoint {
		t.Fatalf("%s: %d route %v %s", name, resp.StatusCode, route, body)
	}
	call := r.calls()[len(r.calls())-1]
	if got := call.Plan.Entries[0].Model; got != want {
		t.Fatalf("%s routed to model %v, want %v", name, got, want)
	}
	return call
}

// checkInvalid posts a request the gateway must refuse as an invalid request, for the reason named.
func checkInvalid(t *testing.T, r *rig, a account, path, body, reason string) {
	t.Helper()
	resp, got := r.do(http.MethodPost, path, a.key, strings.NewReader(body))
	code, _ := errorOf(t, got)
	if resp.StatusCode != http.StatusBadRequest || code != "invalid_request" || !strings.Contains(got, reason) {
		t.Errorf("%s: %d %s, want 400 invalid_request for %q", path, resp.StatusCode, got, reason)
	}
}

// TestAliasAndFreeEntries: an alias resolves to its target for discovery and calls, and a :free entry is a catalog model
// of its own rather than the paid base.
func TestAliasAndFreeEntries(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	saveAlias(t, r, "~acme/family-latest", r.alpha)
	r.publish()

	checkModel(t, r, a, "~acme/family-latest", "acme/alpha")
	checkCall(t, r, a, "~acme/family-latest", r.endpoints["alpha-1"].ID, r.alpha)

	free, endpoint := r.freeEntry()
	checkModel(t, r, a, "acme/alpha:free", "acme/alpha:free")
	checkCall(t, r, a, "acme/alpha:free", endpoint.ID, free)
	r.idle(1 << 30)
}

// TestAutoPicksThroughRouting: openrouter/auto ranks every model, takes the cheapest route and reports the model that
// served the call; a request whose shape needs one model is refused.
func TestAutoPicksThroughRouting(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	free, endpoint := r.freeEntry()

	call := checkCall(t, r, a, "openrouter/auto", endpoint.ID, free)
	if got := (&session{c: call}).servedModel(&attempt{entry: call.Plan.Entries[0]}); got != "acme/alpha:free" {
		t.Errorf("auto reports model %q, want acme/alpha:free", got)
	}
	checkInvalid(t, r, a, "/v1/messages/count_tokens", messageBody("openrouter/auto"), "concrete model")
	checkInvalid(t, r, a, "/v1/chat/completions",
		`{"model":"openrouter/auto","max_tokens":100,"plugins":[{"id":"context-compression"}],"messages":[{"role":"user","content":"hello"}]}`,
		"context window")
	r.idle(1 << 30)
}
