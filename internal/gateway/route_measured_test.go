package gateway

import (
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// TestRoutePlansOnMeasuredFacts: routing uses the catalog's capabilities and context until detection measures
// something else. An endpoint measured without tools takes no tool request, and an endpoint measured with a shorter
// context takes no request that needs the catalog's longer one.
func TestRoutePlansOnMeasuredFacts(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	r := routable(f.served, protocol.Messages)
	withTools := call(f.served, func(req *protocol.Request) { req.Needs = protocol.Tools })
	long := call(f.served, func(req *protocol.Request) { req.TextBytes = 64_000 * 4 }) // 64000 tokens of input

	// No measurement: the catalog's 200000-token context and its tools serve both requests.
	f.build([]suppliers.Routable{r})
	if got := f.planned(withTools); len(got) != 1 || got[0] != r.ID {
		t.Fatalf("no measurement: a tool request planned to %v, want the endpoint %s", got, r.ID)
	}
	if got := f.planned(long); len(got) != 1 || got[0] != r.ID {
		t.Fatalf("no measurement: a 64000-token request planned to %v, want the endpoint %s", got, r.ID)
	}

	if !f.health.Measure(r, protocol.Messages, quality.Facts{Unsupported: protocol.Tools}) {
		t.Fatal("the tool measurement was refused")
	}
	f.build([]suppliers.Routable{r})
	if got := f.planned(call(f.served)); len(got) != 1 || got[0] != r.ID {
		t.Errorf("a call without tools planned to %v, want the endpoint %s", got, r.ID)
	}
	excludedFor(t, f, withTools, routing.MissingCapability)

	if !f.health.Measure(r, protocol.Messages, quality.Facts{Context: 32_000}) {
		t.Fatal("the context measurement was refused")
	}
	f.build([]suppliers.Routable{r})
	excludedFor(t, f, long, routing.ContextTooSmall)
	if got := f.planned(call(f.served)); len(got) != 1 || got[0] != r.ID {
		t.Errorf("a short call planned to %v, want the endpoint %s", got, r.ID)
	}
}

// TestRoutePlansOnMeasuredLimitsAndParameters: a measured output limit reaches the entry, and a measured parameter
// lack excludes a request that requires it.
func TestRoutePlansOnMeasuredLimitsAndParameters(t *testing.T) {
	t.Parallel()
	const temperature = protocol.ParameterSet(1) << protocol.ParamTemperature
	f := newRouteFixture(t)
	r := routable(f.served, protocol.Messages)
	f.build([]suppliers.Routable{r})
	if !f.health.Measure(r, protocol.Messages, quality.Facts{
		MaxOutput: 2048, UnsupportedParameters: temperature,
	}) {
		t.Fatal("the measurement was refused")
	}
	f.build([]suppliers.Routable{r})
	plan, _, err := f.router.Plan(call(f.served))
	if err != nil || len(plan.Entries) != 1 || plan.Entries[0].MaxOutput != 2048 {
		t.Errorf("the plan's output limit = %+v, %v, want the measured 2048", plan.Entries, err)
	}
	requiresTemperature := call(f.served)
	requiresTemperature.Override.RequireParameters = true
	requiresTemperature.Override.RequiredParameters = temperature
	excludedFor(t, f, requiresTemperature, routing.OverrideFilter)
}

// TestRoutePlansOnMeasuredFactsReplacingTheCatalog: a measurement also replaces the catalog upward. A measured
// capability the catalog did not declare is added, a lack is flipped back to working, and measured ceilings higher than
// the catalog's are used.
func TestRoutePlansOnMeasuredFactsReplacingTheCatalog(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	r := routable(f.served, protocol.Messages)
	withTools := call(f.served, func(req *protocol.Request) { req.Needs = protocol.Tools })
	fileSearch := call(f.served, func(req *protocol.Request) { req.Needs = protocol.FileSearch })
	longer := call(f.served, func(req *protocol.Request) { req.TextBytes = 300_000 * 4 })

	f.build([]suppliers.Routable{r})
	// Start from measured lacks and a low ceiling, so the second measurement has something to flip and raise.
	if !f.health.Measure(r, protocol.Messages, quality.Facts{Unsupported: protocol.Tools, Context: 32_000, MaxOutput: 2048}) {
		t.Fatal("the first measurement was refused")
	}
	f.build([]suppliers.Routable{r})
	excludedFor(t, f, withTools, routing.MissingCapability)
	excludedFor(t, f, fileSearch, routing.MissingCapability)
	excludedFor(t, f, longer, routing.ContextTooSmall)

	if !f.health.Measure(r, protocol.Messages, quality.Facts{
		Supported: protocol.Tools | protocol.FileSearch, Context: 400_000, MaxOutput: 16_000,
	}) {
		t.Fatal("the replacing measurement was refused")
	}
	f.build([]suppliers.Routable{r})
	if got := f.planned(withTools); len(got) != 1 || got[0] != r.ID {
		t.Errorf("a measured working tool capability planned to %v, want the endpoint %s", got, r.ID)
	}
	if got := f.planned(fileSearch); len(got) != 1 || got[0] != r.ID {
		t.Errorf("a measured file search capability planned to %v, want the endpoint %s", got, r.ID)
	}
	if got := f.planned(longer); len(got) != 1 || got[0] != r.ID {
		t.Errorf("a 300000-token request planned to %v, want the endpoint measured at 400000 tokens %s", got, r.ID)
	}
	plan, _, err := f.router.Plan(call(f.served))
	if err != nil || len(plan.Entries) != 1 || plan.Entries[0].MaxOutput != 16_000 {
		t.Errorf("the plan's output limit = %+v, %v, want the measured 16000", plan.Entries, err)
	}
}

// TestRoutePlansOnMeasuredFactsReplacingParameters: a parameter measured missing and then working is added back,
// without the capability and limit changes of the other replacement test.
func TestRoutePlansOnMeasuredFactsReplacingParameters(t *testing.T) {
	t.Parallel()
	const temperature = protocol.ParameterSet(1) << protocol.ParamTemperature
	f := newRouteFixture(t)
	r := routable(f.served, protocol.Messages)
	requiresTemperature := call(f.served)
	requiresTemperature.Override.RequireParameters = true
	requiresTemperature.Override.RequiredParameters = temperature
	f.build([]suppliers.Routable{r})
	if !f.health.Measure(r, protocol.Messages, quality.Facts{UnsupportedParameters: temperature}) {
		t.Fatal("the first measurement was refused")
	}
	f.build([]suppliers.Routable{r})
	excludedFor(t, f, requiresTemperature, routing.OverrideFilter)
	if !f.health.Measure(r, protocol.Messages, quality.Facts{SupportedParameters: temperature}) {
		t.Fatal("the replacing measurement was refused")
	}
	f.build([]suppliers.Routable{r})
	if got := f.planned(requiresTemperature); len(got) != 1 || got[0] != r.ID {
		t.Errorf("a measured working temperature planned to %v, want the endpoint %s", got, r.ID)
	}
}

// TestRoutePlansMeasuredPerFormat: a fact measured in Chat is not used for Responses. The same endpoint can lack tools
// in Chat and have them in Responses, and can have a shorter context in Responses than in Chat; each request is judged
// by the format it is actually called in, not the endpoint's first format or its catalog baseline.
func TestRoutePlansMeasuredPerFormat(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	r := routable(f.served, protocol.Chat, protocol.Responses)
	f.build([]suppliers.Routable{r})
	if !f.health.Measure(r, protocol.Chat, quality.Facts{Unsupported: protocol.Tools, Context: 128_000}) {
		t.Fatal("the Chat measurement was refused")
	}
	if !f.health.Measure(r, protocol.Responses, quality.Facts{Context: 32_000}) {
		t.Fatal("the Responses measurement was refused")
	}
	f.build([]suppliers.Routable{r})
	chatTools := call(f.served, func(req *protocol.Request) {
		req.Format, req.Needs = protocol.Chat, protocol.Tools
	})
	excludedFor(t, f, chatTools, routing.MissingCapability)
	responsesTools := call(f.served, func(req *protocol.Request) {
		req.Format, req.Needs = protocol.Responses, protocol.Tools
	})
	if got := f.planned(responsesTools); len(got) != 1 || got[0] != r.ID {
		t.Errorf("Responses tools planned to %v, want the endpoint %s", got, r.ID)
	}
	longChat := call(f.served, func(req *protocol.Request) {
		req.Format, req.TextBytes = protocol.Chat, 64_000*4
	})
	if got := f.planned(longChat); len(got) != 1 || got[0] != r.ID {
		t.Errorf("a 64000-token Chat request planned to %v, want the endpoint measured at 128000 %s", got, r.ID)
	}
	longResponses := call(f.served, func(req *protocol.Request) {
		req.Format, req.TextBytes = protocol.Responses, 64_000*4
	})
	excludedFor(t, f, longResponses, routing.ContextTooSmall)
}

// TestRoutePlansMeasuredParametersFromRequest: a capability parameter the request actually writes, such as tool_choice,
// is part of the inventory provider.require_parameters checks. Measuring it unsupported must exclude the endpoint even
// though the tools capability itself is still present.
func TestRoutePlansMeasuredParametersFromRequest(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	r := routable(f.served, protocol.Chat)
	f.build([]suppliers.Routable{r})
	if !f.health.Measure(r, protocol.Chat, quality.Facts{UnsupportedParameters: protocol.ParameterSet(1) << protocol.ParamToolChoice}) {
		t.Fatal("the parameter measurement was refused")
	}
	f.build([]suppliers.Routable{r})
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"f","parameters":{}}}],"tool_choice":"auto","provider":{"require_parameters":true}}`)
	req, err := protocol.Inspect(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	over := routing.Override{RequireParameters: true}
	if err := requireParameters(&req, body, &over); err != nil {
		t.Fatal(err)
	}
	in := routing.Input{Req: &req, Models: []catalog.ModelID{f.served}, Override: over}
	if !over.RequiredParameters.Has(protocol.ParamToolChoice) {
		t.Fatalf("the request inventory %b does not name tool_choice", over.RequiredParameters)
	}
	excludedFor(t, f, in, routing.OverrideFilter)
}

// TestPublicPageProjectsMeasuredEndpointFacts: the model page's endpoint entry uses the same effective surface as
// routing. Before measurement it claims the catalog's tools and 200000-token context; after measuring tools unsupported
// and a 32000-token context, it claims neither.
func TestPublicPageProjectsMeasuredEndpointFacts(t *testing.T) {
	t.Parallel()
	f, r := measuredPublicFixture(t)
	publicEndpointFor(t, f, r).check(t, true, 200_000)
	if !f.health.Measure(r, protocol.Chat, quality.Facts{
		Unsupported: protocol.Tools, UnsupportedParameters: protocol.ParameterSet(1) << protocol.ParamTemperature,
		Context: 32_000,
	}) {
		t.Fatal("the measurement was refused")
	}
	f.build([]suppliers.Routable{r})
	publicEndpointFor(t, f, r).check(t, false, 32_000)
}

// TestEndpointDiscoveryProjectsMeasuredFacts: the OpenRouter endpoint list uses the endpoint's effective parameters
// and limits, not the model-level baseline. A measured parameter and capability are removed and the measured context
// replaces the catalog's.
func TestEndpointDiscoveryProjectsMeasuredFacts(t *testing.T) {
	t.Parallel()
	f, r := measuredPublicFixture(t)
	discoveryEndpointFor(t, f, r).check(t, true, true, 200_000)
	if !f.health.Measure(r, protocol.Chat, quality.Facts{
		Unsupported: protocol.Tools, UnsupportedParameters: protocol.ParameterSet(1) << protocol.ParamTemperature,
		Context: 32_000,
	}) {
		t.Fatal("the measurement was refused")
	}
	f.build([]suppliers.Routable{r})
	discoveryEndpointFor(t, f, r).check(t, false, false, 32_000)
}

// measuredPublicFixture builds one served endpoint with a public codename and publishes the catalog baseline.
func measuredPublicFixture(t *testing.T) (*routeFixture, suppliers.Routable) {
	t.Helper()
	f := newRouteFixture(t)
	r := routable(f.served, protocol.Chat)
	r.Codename = "g-public"
	f.build([]suppliers.Routable{r})
	return f, r
}

// publicEndpointView is the public page's endpoint entry for the test endpoint.
type publicEndpointView struct{ got *PublicEndpoint }

func publicEndpointFor(t *testing.T, f *routeFixture, r suppliers.Routable) publicEndpointView {
	t.Helper()
	for _, m := range f.models.Public(billing.Audience{}) {
		for i := range m.Endpoints {
			if m.Endpoints[i].Name == r.Codename {
				return publicEndpointView{got: &m.Endpoints[i]}
			}
		}
	}
	t.Fatalf("the endpoint %s is missing from the public page", r.Codename)
	return publicEndpointView{}
}

func (v publicEndpointView) check(t *testing.T, tools bool, context int64) {
	t.Helper()
	if slices.Contains(v.got.Abilities, "tools") != tools {
		t.Errorf("public endpoint tools = %v, want %v", slices.Contains(v.got.Abilities, "tools"), tools)
	}
	if v.got.ContextLength != context || v.got.MaxOutput != 8192 {
		t.Errorf("public endpoint limits = %d/%d, want %d/8192", v.got.ContextLength, v.got.MaxOutput, context)
	}
}

// discoveryEndpointView is the OpenRouter discovery entry for the test endpoint.
type discoveryEndpointView struct{ got orEndpoint }

func discoveryEndpointFor(t *testing.T, f *routeFixture, r suppliers.Routable) discoveryEndpointView {
	t.Helper()
	for i := range f.models.published().models {
		m := &f.models.published().models[i]
		if m.model != f.served {
			continue
		}
		var reply struct {
			Data orEndpointModel `json:"data"`
		}
		if err := jsonx.Unmarshal(m.endpoints, &reply); err != nil {
			t.Fatal(err)
		}
		for _, e := range reply.Data.Endpoints {
			if e.Tag == r.Codename {
				return discoveryEndpointView{got: e}
			}
		}
	}
	t.Fatalf("the endpoint %s is missing from discovery", r.Codename)
	return discoveryEndpointView{}
}

func (v discoveryEndpointView) check(t *testing.T, tools, temperature bool, context int64) {
	t.Helper()
	if slices.Contains(v.got.SupportedParameters, "tools") != tools {
		t.Errorf("discovery tools = %v, want %v", slices.Contains(v.got.SupportedParameters, "tools"), tools)
	}
	if slices.Contains(v.got.SupportedParameters, "temperature") != temperature {
		t.Errorf("discovery temperature = %v, want %v", slices.Contains(v.got.SupportedParameters, "temperature"), temperature)
	}
	if v.got.ContextLength != context || v.got.MaxCompletionTokens != 8192 {
		t.Errorf("discovery limits = %d/%d, want %d/8192", v.got.ContextLength, v.got.MaxCompletionTokens, context)
	}
}

// excludedFor: Plan leaves the only candidate out for reason and says the reason is permanent.
func excludedFor(t *testing.T, f *routeFixture, in routing.Input, reason routing.Reason) {
	t.Helper()
	_, wait, err := f.router.Plan(in)
	var none *routing.NoEndpoint
	if wait != nil || !errors.As(err, &none) {
		t.Fatalf("Plan = wait %v, error %v; want NoEndpoint", wait, err)
	}
	if !none.Permanent || len(none.Reasons) == 0 || none.Reasons[0].Reason != reason {
		t.Fatalf("Plan left the endpoint out as %+v, want permanent %v", none, reason)
	}
}

// TestRouteBuildRanksByToolErrors: the tool-call error rate real answers showed reaches the snapshot, so a request with
// tools puts an endpoint whose calls came back malformed behind a less reliable one whose calls were sound, while a
// request without tools still goes by reliability (docs/domains/routing.md「排序的先后」).
func TestRouteBuildRanksByToolErrors(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	flaky, sound := routable(f.served, protocol.Messages), routable(f.served, protocol.Messages)
	answer := func(id uuid.UUID, toolErrors int64) {
		now := time.Now()
		f.health.Observe(t.Context(), quality.Observation{
			Endpoint: id, Done: true, Delivered: true, OutputTokens: 10, ToolChecks: 2, ToolErrors: toolErrors,
			Sent: now.Add(-time.Second), FirstToken: now.Add(-time.Second / 2), Ended: now,
		})
	}
	for range 10 {
		answer(flaky.ID, 1)
		answer(sound.ID, 0)
	}
	f.health.Observe(t.Context(), quality.Observation{ // the sound one's reliability falls to 0.8
		Endpoint: sound.ID, Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{Status: 500}},
		Ended: time.Now(),
	})
	f.build([]suppliers.Routable{flaky, sound})
	best := routing.BestQuality
	plain := call(f.served)
	plain.Override.Sort = &best
	if got := f.planned(plain); len(got) != 2 || got[0] != flaky.ID {
		t.Fatalf("route %v without tools: the more reliable endpoint is not first", got)
	}
	tools := call(f.served, func(r *protocol.Request) { r.Needs |= protocol.Tools })
	tools.Override.Sort = &best
	if got := f.planned(tools); len(got) != 2 || got[0] != sound.ID {
		t.Errorf("route %v with tools: the endpoint whose tool calls came back malformed is still first", got)
	}
}
