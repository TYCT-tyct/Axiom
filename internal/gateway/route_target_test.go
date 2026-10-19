package gateway

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// A request is written for the endpoint by the Target the snapshot gives it, so what the spec says of the model has to
// arrive in the request the supplier is sent. These tests take the whole way a call takes inside the node: the spec in
// a real catalog, RouteDeps.Build, the router's plan, and protocol.Prepare on the entry's own Target — never a Target
// made by hand — and read what the supplier would be sent.

// dialect is specAll with the keys that say how requests to the model are written.
func dialect(keys string) string { return strings.TrimSuffix(specAll, "}") + "," + keys + "}" }

const (
	chatReasons = `{"model":"m","max_tokens":10000,"reasoning":{"effort":"high"},"messages":[{"role":"user","content":"Hi"}]}`
	// A Codex turn that stores nothing, as the gateway makes every call to an upstream.
	responsesStores = `{"model":"m","input":"Hi","store":false}`
	encrypted       = `"include":["reasoning.encrypted_content"]`
)

// prepared is the request the supplier is sent for body, a call of the client's format to a model whose endpoint speaks
// format to, or the refusal of the endpoint (*protocol.NotSupported).
func (f *routeFixture) prepared(model catalog.ModelID, from, to protocol.Format, body string) (string, error) {
	f.t.Helper()
	f.build([]suppliers.Routable{routable(model, to)})
	r, err := protocol.Inspect(from, []byte(body))
	if err != nil {
		f.t.Fatal(err)
	}
	r.ServiceTiers = 1 << catalog.ServiceTierStandard
	plan, wait, err := f.router.Plan(routing.Input{Req: &r, Models: []catalog.ModelID{model}})
	if err != nil || wait != nil {
		f.t.Fatalf("Plan = %v, %v", wait, err)
	}
	up, err := protocol.Prepare(&r, []byte(body), plan.Entries[0].Target, protocol.Rewrites{Identity: func(string) string { return "h" }})
	return string(up.Body), err
}

// TestAModelIsAskedToThinkAsItsSpecSays: a Chat call that asks for reasoning is written for a Claude model the way its
// spec says it thinks, and a model whose spec says nothing is not asked in one of the ways by default: the zero
// Target asked every model to think adaptively, which a model with a thinking budget refuses.
func TestAModelIsAskedToThinkAsItsSpecSays(t *testing.T) {
	f := newRouteFixture(t)
	system := `"mid_system":true,"untrusted_system_after_tools":false,"default_sampling_only":false`
	for _, c := range []struct {
		name, spec string
		contains   []string
		lacks      []string
		refused    bool
	}{
		{"manual", dialect(`"thinking":"manual",` + system), []string{`"budget_tokens":8000`}, []string{"adaptive"}, false},
		{"adaptive", dialect(`"thinking":"adaptive",` + system), []string{`"type":"adaptive"`}, []string{"budget_tokens"}, false},
		{"no thinking", dialect(`"thinking":"none",` + system), nil, nil, true},
		{"not said", dialect(system), nil, nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			model := f.pricedModel("acme/"+strings.ReplaceAll(c.name, " ", "-"), c.spec)
			body, err := f.prepared(model, protocol.Chat, protocol.Messages, chatReasons)
			if _, ok := errors.AsType[*protocol.NotSupported](err); ok != c.refused || (err != nil && !ok) {
				t.Fatalf("got %s, %v; want the endpoint to refuse: %t", body, err, c.refused)
			}
			for _, s := range c.contains {
				if !strings.Contains(body, s) {
					t.Errorf("the supplier is sent %s, want %s in it", body, s)
				}
			}
			for _, s := range c.lacks {
				if strings.Contains(body, s) {
					t.Errorf("the supplier is sent %s, want no %s in it", body, s)
				}
			}
		})
	}
}

// TestAResponsesCallThatStoresNothingAsksForEncryptedReasoning: the gateway sends every Responses call with store false,
// so the reasoning of a turn comes back only encrypted and only if the call asks; it asks of a model whose spec says it
// returns it, and neither of one whose spec says it does not nor of one whose spec says nothing, which would refuse
// the member or be asked for what it was never said to give.
func TestAResponsesCallThatStoresNothingAsksForEncryptedReasoning(t *testing.T) {
	f := newRouteFixture(t)
	for _, c := range []struct {
		name, keys string
		asks       bool
	}{
		{"yes", `"encrypted_reasoning":true`, true},
		{"no", `"encrypted_reasoning":false`, false},
		{"not said", `"cache_breakpoints":false`, false},
	} {
		model := f.pricedModel("acme/responses-"+strings.ReplaceAll(c.name, " ", "-"), dialect(c.keys))
		for _, from := range []protocol.Format{protocol.Responses, protocol.Chat} {
			body := responsesStores
			if from == protocol.Chat {
				body = chatReasons
			}
			got, err := f.prepared(model, from, protocol.Responses, body)
			if err != nil {
				t.Fatalf("%s, from format %d: %v", c.name, from, err)
			}
			if strings.Contains(got, encrypted) != c.asks || !strings.Contains(got, `"store":false`) {
				t.Errorf("%s, from format %d: the supplier is sent %s; asks for encrypted reasoning: want %t, with the call stored nowhere", c.name, from, got, c.asks)
			}
		}
	}
}

// pricedModel lists a model with the spec and sells it at the fixture's rule.
func (f *routeFixture) pricedModel(slug, spec string) catalog.ModelID {
	f.t.Helper()
	model := f.seed(slug, spec)
	if err := f.cat.Refresh(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
	var err error
	if f.prices, err = f.salePrices(); err != nil {
		f.t.Fatal(err)
	}
	return model
}
