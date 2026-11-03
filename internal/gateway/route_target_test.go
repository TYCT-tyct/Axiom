package gateway

import (
	"errors"
	"fmt"
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
// Target asked every model to think adaptively, which a model with a thinking budget refuses. A want is "unsupported"
// for a refusal, or a part the request has ("+") or has not ("-").
func TestAModelIsAskedToThinkAsItsSpecSays(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	system := `"mid_system":true,"untrusted_system_after_tools":false,"default_sampling_only":false`
	for _, c := range []struct {
		name, spec string
		want       []string
	}{
		{"manual", dialect(`"thinking":"manual",` + system), []string{`+"budget_tokens":8000`, "-adaptive"}},
		{"adaptive", dialect(`"thinking":"adaptive",` + system), []string{`+"type":"adaptive"`, "-budget_tokens"}},
		{"not said", dialect(system), []string{"unsupported"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			model := f.pricedModel("acme/"+strings.ReplaceAll(c.name, " ", "-"), c.spec)
			body, err := f.prepared(model, protocol.Chat, protocol.Messages, chatReasons)
			for _, want := range c.want {
				if complaint := missing(body, err, want); complaint != "" {
					t.Error(complaint)
				}
			}
		})
	}
}

// missing says how the request the supplier is sent, or the refusal, differs from want; "" when it does not.
func missing(body string, err error, want string) string {
	refused, _ := errors.AsType[*protocol.NotSupported](err)
	switch {
	case want == "unsupported" && refused == nil:
		return fmt.Sprintf("got %s, %v; want the endpoint to refuse the request", body, err)
	case want == "unsupported":
		return ""
	case err != nil:
		return fmt.Sprintf("got %v; want the request prepared", err)
	case want[0] == '+' && !strings.Contains(body, want[1:]):
		return fmt.Sprintf("the supplier is sent %s, want %s in it", body, want[1:])
	case want[0] == '-' && strings.Contains(body, want[1:]):
		return fmt.Sprintf("the supplier is sent %s, want no %s in it", body, want[1:])
	}
	return ""
}

// TestAResponsesCallThatStoresNothingAsksForEncryptedReasoning: the gateway sends every Responses call with store false,
// so the reasoning of a turn comes back only encrypted and only if the call asks; it asks of a model whose spec says it
// returns it, and neither of one whose spec says it does not nor of one whose spec says nothing, which would refuse
// the member or be asked for what it was never said to give.
func TestAResponsesCallThatStoresNothingAsksForEncryptedReasoning(t *testing.T) {
	t.Parallel()
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
	if f.prices, err = f.fixturePrices(); err != nil {
		f.t.Fatal(err)
	}
	return model
}
