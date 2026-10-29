package routing_test

import (
	"errors"
	"testing"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

func TestSupplierAffordabilityUsesNativeFullCost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, balance string
		output        int64
		choices       int
		want          bool
	}{
		{"equal", "0.000201", 100, 1, true},
		{"one native quantum short", "0.000200999999999", 100, 1, false},
		{"cheaper request", "0.0001", 10, 1, true},
		{"full generation exceeds balance", "0.0001", 100, 1, false},
		{"multiple choices", "0.000201", 100, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := endpoint(t, 1)
			balance, err := platform.ParseExact(tc.balance)
			if err != nil {
				t.Fatal(err)
			}
			e.Funds = &suppliers.Funds{Balance: balance}
			e.Cost = schedule(t, "0.000001", "0.000002", "0.0000001")
			// Sale prices and native cost have unrelated scales. The cheapest sale is not the cheapest purchase.
			e.Price.Schedule = schedule(t, "100", "100", "1")
			h := newHarness(t, routing.Config{FirstSegment: 1, Overshoot: 1}, openPreset(), e)
			in := input()
			in.Req.TextBytes, in.Req.MaxOutput, in.Req.Choices = 1, tc.output, int64(tc.choices)
			in.Req.ServiceTiers = 1 << catalog.ServiceTierStandard
			checkAffordable(t, h, in, tc.want)
		})
	}
}

func TestAffordabilityIncludesTierAndToolSampling(t *testing.T) {
	t.Parallel()
	cost, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"XXX","rates":{"input":"1","cache_read":"1","output":"2","web_search":"3"},"service_tiers":{"priority":{"rates":{"input":"2","cache_read":"2","output":"4","web_search":"6"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, balance string
		want          bool
	}{
		// Priority: first input 1*2 + resampled context 100*2 + output 10*4 + one search 6 = 248 native units.
		{"exact priority tool bound", "248", true}, {"one short", "247.999999999999999", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := endpoint(t, 1)
			balance, err := platform.ParseExact(tc.balance)
			if err != nil {
				t.Fatal(err)
			}
			e.Funds, e.Cost, e.Context = &suppliers.Funds{Balance: balance}, cost, 100
			h := newHarness(t, routing.Config{}, openPreset(), e)
			in := input()
			in.Req.TextBytes, in.Req.MaxOutput = 1, 10
			in.Req.Needs, in.Req.WebSearches = protocol.WebSearch, 1
			in.Req.ServiceTiers = 1 << catalog.ServiceTierPriority
			_, _, err = h.r.Plan(in)
			if (err == nil) != tc.want {
				t.Fatalf("affordability: %v, want admitted %v", err, tc.want)
			}
		})
	}
}

func TestAffordabilityAddsNoPlanAllocations(t *testing.T) {
	e := endpoint(t, 1)
	h := newHarness(t, routing.Config{}, openPreset(), e)
	in := input()
	measure := func() float64 {
		return testing.AllocsPerRun(100, func() {
			if _, _, err := h.r.Plan(in); err != nil {
				panic(err)
			}
		})
	}
	before := measure()
	e.Funds = &suppliers.Funds{Balance: platform.AmountExact(1_000_000_000_000)}
	e.Cost = schedule(t, "0.000001", "0.000002", "0.0000001")
	h.replace(openPreset(), e)
	if after := measure(); after != before {
		t.Fatalf("Plan allocations with funds = %v, without = %v", after, before)
	}
}

func checkAffordable(t *testing.T, h *harness, in routing.Input, want bool) {
	t.Helper()
	_, wait, err := h.r.Plan(in)
	if want {
		if err != nil || wait != nil {
			t.Fatalf("affordable route: %v, %v", wait, err)
		}
	} else {
		var no *routing.NoEndpoint
		if !errors.As(err, &no) || no.Permanent || len(no.Reasons) != 1 || no.Reasons[0].Reason != routing.SupplierBalance {
			t.Fatalf("unaffordable route: %v", err)
		}
	}
}

func TestUnboundedToolKeepsItsAdmissionError(t *testing.T) {
	t.Parallel()
	e := endpoint(t, 1)
	e.Funds = &suppliers.Funds{Balance: platform.AmountExact(1_000_000_000_000)}
	e.Cost = schedule(t, "0.000001", "0.000002", "0.0000001")
	h := newHarness(t, routing.Config{}, openPreset(), e)
	in := input()
	in.Req.Needs = protocol.WebSearch
	plan := h.plan(in)
	entry := plan.Entries[0]
	_, err := billing.Quote(routing.DemandOf(in.Req), []billing.Quotable{{
		Price: entry.Price.Schedule, Model: entry.Limits, MaxOutput: entry.MaxOutput,
	}}, billing.Segments{}, nil)
	if !errors.Is(err, billing.ErrUnbounded) {
		t.Fatalf("admission: %v, want actionable unbounded-tool error", err)
	}
}

func TestMissingNativeCostDoesNotBecomeFreeCapacity(t *testing.T) {
	t.Parallel()
	e := endpoint(t, 1)
	e.Funds = &suppliers.Funds{Balance: platform.AmountExact(1_000_000_000_000)}
	// This model reports cache reads, so input/output prices alone do not bound the cost of the request.
	e.Cost = schedule(t, "0.000001", "0.000002", "")
	h := newHarness(t, routing.Config{}, openPreset(), e)
	checkAffordable(t, h, input(), false)
}
