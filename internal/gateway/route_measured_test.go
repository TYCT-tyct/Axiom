package gateway

import (
	"errors"
	"testing"

	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// TestRoutePlansOnMeasuredFacts: routing uses the catalog's capabilities, parameters, context and output limit until
// detection measures something else. An endpoint measured without tools takes no tool request, an endpoint measured
// with a shorter context takes no request that needs the catalog's longer one, and an endpoint with no measurement
// keeps both. A measured max output and parameter lack reach the plan the same way.
func TestRoutePlansOnMeasuredFacts(t *testing.T) {
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

	f.health.Measure(r, quality.Facts{Unsupported: protocol.Tools})
	f.build([]suppliers.Routable{r})
	if got := f.planned(call(f.served)); len(got) != 1 || got[0] != r.ID {
		t.Errorf("a call without tools planned to %v, want the endpoint %s", got, r.ID)
	}
	excludedFor(t, f, withTools, routing.MissingCapability)

	f.health.Measure(r, quality.Facts{Context: 32_000})
	f.build([]suppliers.Routable{r})
	excludedFor(t, f, long, routing.ContextTooSmall)
	if got := f.planned(call(f.served)); len(got) != 1 || got[0] != r.ID {
		t.Errorf("a short call planned to %v, want the endpoint %s", got, r.ID)
	}

	f.health.Measure(r, quality.Facts{MaxOutput: 2048})
	f.build([]suppliers.Routable{r})
	plan, _, err := f.router.Plan(call(f.served))
	if err != nil || len(plan.Entries) != 1 || plan.Entries[0].MaxOutput != 2048 {
		t.Errorf("the plan's output limit = %+v, %v, want the measured 2048", plan.Entries, err)
	}

	f.health.Measure(r, quality.Facts{UnsupportedParameters: protocol.ParameterSet(1) << protocol.ParamTemperature})
	f.build([]suppliers.Routable{r})
	requiresTemperature := call(f.served)
	requiresTemperature.Override.RequireParameters = true
	requiresTemperature.Override.RequiredParameters = protocol.ParameterSet(1) << protocol.ParamTemperature
	excludedFor(t, f, requiresTemperature, routing.OverrideFilter)
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
