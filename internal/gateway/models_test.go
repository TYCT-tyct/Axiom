package gateway

import (
	"net/http"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/routing"
)

// messagesShell reports whether body is in Messages' shell, whose top-level type member OpenAI's shell lacks.
func messagesShell(t *testing.T, body string) bool {
	t.Helper()
	var top map[string]jsonx.Raw
	if err := jsonx.Unmarshal([]byte(body), &top); err != nil {
		t.Fatalf("not a JSON body: %q: %v", body, err)
	}
	_, ok := top["type"]
	return ok
}

// TestPathsAndMethodsNoRouteTakes: a path nothing serves is 404 and a method its path does not take is 405 with Allow,
// both in the protocol's error shell like every other refusal: Messages' under /v1/messages in either prefix, Chat's
// elsewhere, with the request ID on the answer (docs/domains/gateway.md「错误」).
func TestPathsAndMethodsNoRouteTakes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for name, tc := range map[string]struct {
		method, path string
		status       int
		code, typ    string
		allow        string
		messages     bool
	}{
		"a path nothing serves":                            {http.MethodGet, "/v1/nothing", http.StatusNotFound, "not_found", "not_found_error", "", false},
		"a path nothing serves, the other prefix":          {http.MethodGet, "/api/v1/nothing", http.StatusNotFound, "not_found", "not_found_error", "", false},
		"a Messages path nothing serves":                   {http.MethodGet, "/v1/messages/nothing", http.StatusNotFound, "not_found", "not_found_error", "", true},
		"a Messages path nothing serves, the other prefix": {http.MethodGet, "/api/v1/messages/nothing", http.StatusNotFound, "not_found", "not_found_error", "", true},
		"a POST-only path asked with GET":                  {http.MethodGet, "/v1/messages/count_tokens", http.StatusMethodNotAllowed, "method_not_allowed", "invalid_request_error", "POST", true},
		"a GET-only path asked with POST":                  {http.MethodPost, "/v1/models", http.StatusMethodNotAllowed, "method_not_allowed", "invalid_request_error", "GET, HEAD", false},
		"a call path asked with GET":                       {http.MethodGet, "/v1/messages", http.StatusMethodNotAllowed, "method_not_allowed", "invalid_request_error", "POST", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp, body := r.do(tc.method, tc.path, "", nil)
			code, typ := errorOf(t, body)
			if resp.StatusCode != tc.status || code != tc.code || typ != tc.typ || resp.Header.Get("Allow") != tc.allow ||
				resp.Header.Get("Content-Type") != "application/json" {
				t.Errorf("%d %s %s, Allow %q, Content-Type %q; want %d %s %s %q application/json:\n%s",
					resp.StatusCode, code, typ, resp.Header.Get("Allow"), resp.Header.Get("Content-Type"),
					tc.status, tc.code, tc.typ, tc.allow, body)
			}
			if id := resp.Header.Get("X-Request-ID"); id == "" || resp.Header.Get("X-Generation-Id") != id {
				t.Errorf("X-Request-ID %q, X-Generation-Id %q", id, resp.Header.Get("X-Generation-Id"))
			}
			if got := messagesShell(t, body); got != tc.messages {
				t.Errorf("Messages shell %v, want %v: %s", got, tc.messages, body)
			}
		})
	}
}

// TestModelListIsScopedToTheKey: the list and the single-model endpoint show the models the key's admission allows,
// and nothing else. A model that exists but is not the key's is 404 like a model that does not exist; an alias is the
// model behind it, as a call resolves it.
func TestModelListIsScopedToTheKey(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	alpha := r.newAccount(1e9)
	restrict(r, alpha, "{acme/alpha}")
	aliased := r.newAccount(1e9)
	restrict(r, aliased, "{claude-live}") // the maker's name of anthropic/claude-live
	checkListIsScoped(t, r, alpha, []string{"acme/alpha"})
	checkListIsScoped(t, r, aliased, []string{"anthropic/claude-live", "claude-live"})
	checkOneModel(t, r, alpha, aliased)
	t.Run("an Anthropic client reads the same 404 in Messages' shell", func(t *testing.T) {
		resp, body := r.do(http.MethodGet, "/v1/models/acme/beta", alpha.key, nil, "Anthropic-Version", "2023-06-01")
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "model_not_found" || !messagesShell(t, body) {
			t.Errorf("an Anthropic client: %d %s", resp.StatusCode, body)
		}
	})
}

// checkListIsScoped: the key's list is exactly want, in both prefixes.
func checkListIsScoped(t *testing.T, r *rig, a account, want []string) {
	t.Helper()
	for _, path := range []string{"/v1/models", "/api/v1/models"} {
		if diff := cmp.Diff(want, r.listIDs(a, path)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", path, diff)
		}
	}
}

// checkOneModel: the one model is the list's entry, under the name that was asked for, and a model the key may not use,
// one that does not exist and another key's model are the same 404.
func checkOneModel(t *testing.T, r *rig, alpha, aliased account) {
	t.Helper()
	for _, tc := range []struct {
		path               string
		a                  account
		id, display, owner string
	}{
		{"/v1/models/acme/alpha", alpha, "acme/alpha", "alpha", "acme"},
		{"/v1/models/claude-live", aliased, "claude-live", "claude-live", "anthropic"},
		{"/v1/models/anthropic/claude-live", aliased, "anthropic/claude-live", "claude-live", "anthropic"},
	} {
		resp, body := r.do(http.MethodGet, tc.path, tc.a.key, nil)
		var m listedModel
		if resp.StatusCode != http.StatusOK || jsonx.Unmarshal([]byte(body), &m) != nil || m.ID != tc.id ||
			m.DisplayName != tc.display || m.OwnedBy != tc.owner || !m.complete() {
			t.Errorf("%s: %d %+v\n%s", tc.path, resp.StatusCode, m, body)
		}
	}
	for _, tc := range []struct {
		name, path string
		a          account
	}{
		{"a model the key may not call", "/v1/models/acme/beta", alpha},
		{"a model that does not exist", "/v1/models/acme/none", alpha},
		{"a model of another key", "/v1/models/acme/alpha", aliased},
	} {
		resp, body := r.do(http.MethodGet, tc.path, tc.a.key, nil)
		if code, typ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "model_not_found" || typ != "not_found_error" {
			t.Errorf("%s: %d %s %s, want 404 model_not_found not_found_error:\n%s", tc.name, resp.StatusCode, code, typ, body)
		}
	}
}

// TestModelListKeepsModelsWhoseEndpointsAreBusy: the list is the key's permission over the models that have an endpoint,
// not over the routing state of the moment, so a model whose endpoints are all broken or all at capacity right now stays
// listed, in the list and asked for alone; a call to it is what waits or is refused.
func TestModelListKeepsModelsWhoseEndpointsAreBusy(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		endpoint func(*routing.Endpoint)
		busy     func(*rig)
		call     int // what a call to acme/alpha is answered; 0 is not asked, since a full model makes it wait
	}{
		"every breaker open": {func(e *routing.Endpoint) { e.BreakerOpen = true }, func(*rig) {}, http.StatusServiceUnavailable},
		"every endpoint at capacity": {func(e *routing.Endpoint) { e.MaxConcurrent = 1 }, func(r *rig) {
			for _, e := range r.endpoints {
				r.router.Begin(e.ID)
			}
		}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(o *rigOptions) { o.endpoints = func(_ string, e *routing.Endpoint) { tc.endpoint(e) } })
			tc.busy(r)
			a := r.newAccount(1e9)
			want := []string{"acme/alpha", "acme/beta", "anthropic/claude-live", "claude-live"}
			if diff := cmp.Diff(want, r.listIDs(a, "/v1/models")); diff != "" {
				t.Errorf("the list (-want +got):\n%s", diff)
			}
			if resp, body := r.do(http.MethodGet, "/v1/models/acme/alpha", a.key, nil); resp.StatusCode != http.StatusOK {
				t.Errorf("a listed model asked for alone: %d %s", resp.StatusCode, body)
			}
			if tc.call != 0 {
				if resp, body := r.message(a, "acme/alpha"); resp.StatusCode != tc.call {
					t.Errorf("a call to a listed model: %d %s", resp.StatusCode, body)
				}
			}
			r.idle(1 << 30)
		})
	}
}

// TestModelNothingServesIsNotListed: a model the catalog lists but no endpoint serves (the routing snapshot leaves out a
// model without a price or a spec) is not in the list and is a 404 asked for alone, under either of its names, like a
// model that does not exist: a client that picked it could only be refused. It appears once the routing snapshot has an
// endpoint for it.
func TestModelNothingServesIsNotListed(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	restrict(r, a, "{claude-test,acme/alpha}") // the key may call the model, so it is the route that leaves it out
	if diff := cmp.Diff([]string{"acme/alpha"}, r.listIDs(a, "/v1/models")); diff != "" {
		t.Errorf("without an endpoint: the list (-want +got):\n%s", diff)
	}
	for _, path := range []string{"/v1/models/claude-test", "/v1/models/anthropic/claude-test"} {
		resp, body := r.do(http.MethodGet, path, a.key, nil)
		if code, typ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "model_not_found" || typ != "not_found_error" {
			t.Errorf("without an endpoint: %s: %d %s %s", path, resp.StatusCode, code, typ)
		}
	}
	r.attachEndpoint(r.bare)
	want := []string{"acme/alpha", "anthropic/claude-test", "claude-test"}
	if diff := cmp.Diff(want, r.listIDs(a, "/v1/models")); diff != "" {
		t.Errorf("with an endpoint: the list (-want +got):\n%s", diff)
	}
	if resp, body := r.do(http.MethodGet, "/v1/models/claude-test", a.key, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("with an endpoint, asked for alone: %d %s", resp.StatusCode, body)
	}
}

// attachEndpoint gives the model an endpoint in a new routing snapshot, as the rebuild after a supplier is attached makes.
func (r *rig) attachEndpoint(m catalog.ModelID) {
	r.t.Helper()
	e := r.endpoints["claude-1"]
	e.ID, e.Supplier, e.Model = routing.EndpointID(uuid.NewV7()), routing.SupplierID(uuid.NewV7()), m
	r.endpoints[m.String()] = e
	endpoints := make([]routing.Endpoint, 0, len(r.endpoints))
	for _, e := range r.endpoints {
		endpoints = append(endpoints, e)
	}
	r.install(routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}, endpoints...)
}

// TestModelListEdges: a key that may call no model gets an empty list without first_id or last_id; the list and the
// single model are never cached; HEAD is the GET without a body; and OpenRouter's other paths under /models, which a
// model name must not swallow, are not found rather than a model that does not exist.
func TestModelListEdges(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	none := r.newAccount(1e9)
	restrict(r, none, "{}")
	all := r.newAccount(1e9)
	resp, body := r.do(http.MethodGet, "/v1/models", none.key, nil)
	var list map[string]any
	if err := jsonx.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if data, ok := list["data"].([]any); resp.StatusCode != http.StatusOK || !ok || len(data) != 0 || list["has_more"] != false ||
		list["first_id"] != nil || list["last_id"] != nil || list["object"] != "list" {
		t.Errorf("a key with no models: %d %s", resp.StatusCode, body)
	}
	for _, path := range []string{"/v1/models", "/v1/models/acme/alpha", "/api/v1/models"} {
		if resp, body := r.do(http.MethodGet, path, all.key, nil); resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control %q: %s", path, resp.Header.Get("Cache-Control"), body)
		}
	}
	if resp, body := r.do(http.MethodHead, "/v1/models", all.key, nil); resp.StatusCode != http.StatusOK || body != "" ||
		resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("HEAD: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	for _, path := range []string{"/v1/models/count", "/api/v1/models/user", "/v1/models/acme/alpha/endpoints"} {
		resp, body := r.do(http.MethodGet, path, all.key, nil)
		if code, typ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "not_found" || typ != "not_found_error" {
			t.Errorf("%s: %d %s %s, want 404 not_found", path, resp.StatusCode, code, typ)
		}
	}
}

// restrict sets the models a key may call.
func restrict(r *rig, a account, models string) {
	r.t.Helper()
	r.sql("UPDATE accounts.api_keys SET models = '" + models + "' WHERE id = '" + uuid.UUID(a.keyID).String() + "'")
}
