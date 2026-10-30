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

// TestAliasAndFreeEntries: an alias resolves to its target for discovery and calls, a :free entry is a catalog model
// of its own, and openrouter/auto picks one through routing and reports that model's public name.
func TestAliasAndFreeEntries(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)

	if _, err := r.catalog.SaveAlias(t.Context(), catalog.SaveAliasRequest{
		Op:   catalog.Op{Actor: audit.System("gateway.test"), Key: uuid.NewV7(), Reason: "test"},
		Name: "~acme/family-latest", Model: r.alpha,
	}); err != nil {
		t.Fatal(err)
	}
	r.publish()
	resp, body := r.do(http.MethodGet, "/api/v1/models/~acme/family-latest", a.key, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"id":"acme/alpha"`) {
		t.Fatalf("alias detail: %d %s", resp.StatusCode, body)
	}

	free := r.listed("acme/alpha:free", basicSpec, `{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`)
	zero, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	freeEndpoint := r.endpoints["alpha-1"]
	freeEndpoint.ID, freeEndpoint.Model, freeEndpoint.Codename = routing.EndpointID(uuid.NewV7()), free, "free-1"
	freeEndpoint.Price = routing.PriceRef{Version: uuid.NewV7(), Schedule: zero}
	r.install(routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}, append(r.routes, freeEndpoint)...)

	resp, body = r.do(http.MethodGet, "/api/v1/models/acme/alpha:free", a.key, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"id":"acme/alpha:free"`) {
		t.Fatalf("free detail: %d %s", resp.StatusCode, body)
	}

	route, resp, body := r.sendNamed(a, "acme/alpha:free", "")
	if resp.StatusCode != http.StatusOK || len(route) != 1 || route[0] != freeEndpoint.ID {
		t.Fatalf("free call: %d route %v %s", resp.StatusCode, route, body)
	}
	call := r.calls()[len(r.calls())-1]
	if call.Plan.Entries[0].Model != free {
		t.Errorf("free call routed to model %v, want %v", call.Plan.Entries[0].Model, free)
	}

	route, resp, body = r.sendNamed(a, "~acme/family-latest", "")
	if resp.StatusCode != http.StatusOK || len(route) == 0 || route[0] != r.endpoints["alpha-1"].ID {
		t.Fatalf("alias call: %d route %v %s", resp.StatusCode, route, body)
	}
	call = r.calls()[len(r.calls())-1]
	if call.Plan.Entries[0].Model != r.alpha {
		t.Errorf("alias call routed to model %v, want %v", call.Plan.Entries[0].Model, r.alpha)
	}

	route, resp, body = r.sendNamed(a, "openrouter/auto", "")
	if resp.StatusCode != http.StatusOK || len(route) == 0 || route[0] != freeEndpoint.ID {
		t.Fatalf("auto call: %d route %v %s", resp.StatusCode, route, body)
	}
	call = r.calls()[len(r.calls())-1]
	if call.Plan.Entries[0].Model != free {
		t.Errorf("auto routed to model %v, want the cheapest, %v", call.Plan.Entries[0].Model, free)
	}
	if got := (&session{c: call}).servedModel(&attempt{entry: call.Plan.Entries[0]}); got != "acme/alpha:free" {
		t.Errorf("auto reports model %q, want acme/alpha:free", got)
	}
	resp, body = r.do(http.MethodPost, "/v1/messages/count_tokens", a.key, strings.NewReader(messageBody("openrouter/auto")))
	if code, _ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" || !strings.Contains(body, "concrete model") {
		t.Errorf("count for auto: %d %s, want a 400 asking for the concrete model", resp.StatusCode, body)
	}
	resp, body = r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(`{"model":"openrouter/auto","max_tokens":100,"plugins":[{"id":"context-compression"}],"messages":[{"role":"user","content":"hello"}]}`))
	if code, _ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" || !strings.Contains(body, "context window") {
		t.Errorf("compression with auto: %d %s, want a 400 about the context window", resp.StatusCode, body)
	}
	r.idle(1 << 30)
}
