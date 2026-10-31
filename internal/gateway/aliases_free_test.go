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
// route in the rig. mods change the endpoint before the snapshot holds it.
func (r *rig) freeEntry(mods ...func(*routing.Endpoint)) (catalog.ModelID, routing.Endpoint) {
	r.t.Helper()
	const zeroPrice = `{"v":1,"currency":"USD","rates":{"input":"0","output":"0","cache_write":"0"},"service_tiers":{"priority":{"rates":{"input":"0","output":"0"}}}}`
	free := r.listed("acme/alpha:free", basicSpec, zeroPrice)
	zero, err := catalog.ParseSchedule([]byte(zeroPrice))
	if err != nil {
		r.t.Fatal(err)
	}
	e := r.endpoints["alpha-1"]
	e.ID, e.Model, e.Codename = routing.EndpointID(uuid.NewV7()), free, "alpha-free"
	e.Free = true
	e.Price = routing.PriceRef{Version: uuid.NewV7(), Schedule: zero}
	for _, m := range mods {
		m(&e)
	}
	r.endpoints["alpha-free"] = e
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

// TestFreePicksOnlyFreeThroughRouting: openrouter/free ranks only the free catalog entries of the key, under the
// request's ranking or the preset's strategy, reports the model that served the call, and refuses a count or a
// compression before routing because no concrete model exists yet.
func TestFreePicksOnlyFreeThroughRouting(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	// The paid endpoints stream faster than the free one, so :nitro would choose one of them if free ranked every model.
	free, endpoint := r.freeEntry(func(e *routing.Endpoint) { e.Throughput = 1 })

	call := checkCall(t, r, a, "openrouter/free", endpoint.ID, free)
	if got := (&session{c: call}).servedModel(&attempt{entry: call.Plan.Entries[0]}); got != "acme/alpha:free" {
		t.Errorf("free reports model %q, want acme/alpha:free", got)
	}
	checkCall(t, r, a, "openrouter/free:nitro", endpoint.ID, free)

	// The request's models list is ignored, as it is for openrouter/auto.
	route, resp, body := r.sendNamed(a, "openrouter/free", `"models":["acme/beta"],`)
	if resp.StatusCode != http.StatusOK || len(route) == 0 || route[0] != endpoint.ID {
		t.Fatalf("free with a models list: %d route %v %s", resp.StatusCode, route, body)
	}
	checkInvalid(t, r, a, "/v1/messages/count_tokens", messageBody("openrouter/free"), "concrete model")
	checkInvalid(t, r, a, "/v1/chat/completions",
		`{"model":"openrouter/free","max_tokens":100,"plugins":[{"id":"context-compression"}],"messages":[{"role":"user","content":"hello"}]}`,
		"context window")
	r.idle(1 << 30)
}

// TestFreeRefusesWhenNothingIsFree: a site with no free entry is the permanent no-endpoint refusal the error table
// documents, with no_candidates as its reason, never a model and never a wait.
func TestFreeRefusesWhenNothingIsFree(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	resp, body := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(messageBody("openrouter/free")))
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, `"error_type":"no_endpoint"`) || !strings.Contains(body, "no_candidates") {
		t.Fatalf("%d %s, want a 404 no_endpoint for no_candidates", resp.StatusCode, body)
	}
}

// TestFreeRouterRelaysARecordedCall: the recorded Messages sample goes through openrouter/free to the free entry, the
// upstream is asked for that endpoint's name for the model, and the reply names the model that served the call, not the
// router, as openrouter/auto does.
func TestFreeRouterRelaysARecordedCall(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	_, endpoint := r.freeEntry(func(e *routing.Endpoint) { e.Target.Model = "up-free" })
	up := withExecutor(r, &flakyLedger{})
	events, recorded := sample(t, protocol.Messages)
	for _, e := range up.eps {
		if e.script == nil {
			e.script = []scripted{failure(routing.ScopeEndpoint, false)}
		}
	}
	up.eps[uuid.UUID(endpoint.ID)].script = []scripted{serve(beat{data: strings.Join(events, "")})}
	a := r.funded(1e12)
	request := strings.Replace(string(recorded), `"claude-opus-5-5"`, `"openrouter/free"`, 1)
	resp, body := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(request))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "acme/alpha:free") || strings.Contains(body, "openrouter/free") {
		t.Errorf("the reply must name acme/alpha:free, not the router:\n%s", body)
	}
	calls := up.eps[uuid.UUID(endpoint.ID)].calls
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"model": "up-free"`) {
		t.Errorf("the upstream was asked %d times, want one call for up-free", len(calls))
	}
	r.idle(1 << 30)
}
