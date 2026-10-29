package routing_test

import (
	"slices"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

func TestProviderGroupFilters(t *testing.T) {
	t.Parallel()
	named := func(name string) func(*routing.Endpoint) {
		return func(e *routing.Endpoint) { e.Codename, e.Supplier = name, sup(1) }
	}
	for _, tc := range []struct {
		name    string
		over    routing.Override
		preset  func(*routing.Preset)
		private bool
		want    []routing.EndpointID
	}{
		{name: "only one group of a supplier", over: routing.Override{OnlyNames: []string{"g2"}}, want: []routing.EndpointID{ep(2)}},
		{name: "ignore one group of a supplier", over: routing.Override{ExcludeNames: []string{"g1"}}, want: []routing.EndpointID{ep(2)}},
		{name: "closed named order", over: routing.Override{OrderNames: []string{"missing", "g2", "g2"}, NoFallbacks: true}, want: []routing.EndpointID{ep(2)}},
		{name: "unknown only", over: routing.Override{OnlyNames: []string{"missing"}}},
		{name: "unknown closed order", over: routing.Override{OrderNames: []string{"missing"}, NoFallbacks: true}},
		{name: "ignore wins over order", over: routing.Override{OrderNames: []string{"g2"}, ExcludeNames: []string{"g2"}, NoFallbacks: true}},
		{name: "request order replaces preset priority", over: routing.Override{OrderNames: []string{"g2", "g1"}}, preset: func(p *routing.Preset) { p.Order = []routing.SupplierID{sup(1)} }, want: []routing.EndpointID{ep(2), ep(1)}},
		{name: "private group", over: routing.Override{OnlyNames: []string{"g2"}}, private: true},
		{name: "preset stays restrictive", over: routing.Override{OnlyNames: []string{"g2"}}, preset: func(p *routing.Preset) { p.Classes = 1 << routing.ClassRelay }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := openPreset()
			if tc.preset != nil {
				tc.preset(&p)
			}
			second := endpoint(t, 2, named("g2"))
			if tc.private {
				second.Owner.Account = routing.AccountID(id(999))
			}
			h := newHarness(t, routing.Config{}, p, endpoint(t, 1, named("g1")), second)
			in := input()
			in.Override = tc.over
			if len(tc.want) == 0 {
				checkProviderExcluded(t, h, in, tc.private)
			} else if got := endpoints(h.plan(in)); !slices.Equal(got, tc.want) {
				t.Errorf("route %v, want %v", got, tc.want)
			}
		})
	}
}

func checkProviderExcluded(t *testing.T, h *harness, in routing.Input, private bool) {
	t.Helper()
	got := h.excluded(in)
	if !got.Permanent {
		t.Fatalf("expected permanent exclusion: %+v", got)
	}
	if private && slices.ContainsFunc(got.Reasons, func(e routing.Excluded) bool { return e.Endpoint == ep(2) }) {
		t.Fatal("private endpoint appeared in exclusions")
	}
}

func TestProviderRequireParameters(t *testing.T) {
	t.Parallel()
	var all protocol.ParameterSet
	for p := range protocol.NumParameters {
		all = all.With(p)
	}
	noTemperature := all &^ (protocol.ParameterSet(1) << protocol.ParamTemperature)
	dear := func(e *routing.Endpoint) {
		e.Price.Schedule = schedule(t, "0.000002", "0.000004", "")
	}
	for _, tc := range []struct {
		name     string
		require  bool
		required protocol.ParameterSet
		first    protocol.ParameterSet
		second   protocol.ParameterSet
		want     []routing.EndpointID
	}{
		{"not required", false, 0, all, 0, []routing.EndpointID{ep(1), ep(2)}},
		{"both declare it", true, all, all, all, []routing.EndpointID{ep(1), ep(2)}},
		{"one declares it", true, all, all, noTemperature, []routing.EndpointID{ep(1)}},
		{"neither declares it", true, all, noTemperature, noTemperature, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			preset := openPreset()
			preset.Strategy = routing.Cheapest
			h := newHarness(t, routing.Config{}, preset,
				endpoint(t, 1, func(e *routing.Endpoint) { e.Parameters = tc.first }), endpoint(t, 2, dear, func(e *routing.Endpoint) { e.Parameters = tc.second }))
			in := input()
			in.Override = routing.Override{RequireParameters: tc.require, RequiredParameters: tc.required}
			if len(tc.want) == 0 {
				checkProviderExcluded(t, h, in, false)
			} else if got := endpoints(h.plan(in)); !slices.Equal(got, tc.want) {
				t.Errorf("route %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProviderSortPartition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		global, closed bool
		want           []routing.EndpointID
	}{
		{"model partition", false, false, []routing.EndpointID{ep(1), ep(3), ep(2)}},
		{"global partition", true, false, []routing.EndpointID{ep(2), ep(1), ep(3)}},
		{"top per model without supplier fallback", false, true, []routing.EndpointID{ep(1), ep(2)}},
		{"global top per model without supplier fallback", true, true, []routing.EndpointID{ep(2), ep(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			second := endpoint(t, 2, func(e *routing.Endpoint) {
				e.Model = other
				e.Price.Schedule = schedule(t, "0.0000001", "0.0000002", "")
			})
			h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1), second, endpoint(t, 3))
			in := input()
			in.Models = []catalog.ModelID{model, other}
			cheapest := routing.Cheapest
			in.Override = routing.Override{Sort: &cheapest, SortGlobal: tc.global, NoFallbacks: tc.closed}
			if got := endpoints(h.plan(in)); !slices.Equal(got, tc.want) {
				t.Errorf("route %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProviderLowestLatency(t *testing.T) {
	t.Parallel()
	h := newHarness(t, routing.Config{}, openPreset(),
		endpoint(t, 1, speed(1000, time.Second)), endpoint(t, 2, speed(10, 100*time.Millisecond)), endpoint(t, 3, unmeasured))
	in := input()
	latency := routing.LowestLatency
	in.Override.Sort = &latency
	if got, want := endpoints(h.plan(in)), []routing.EndpointID{ep(2), ep(1), ep(3)}; !slices.Equal(got, want) {
		t.Errorf("route %v, want %v", got, want)
	}
}

func BenchmarkProviderPlan(b *testing.B) {
	for _, tc := range []struct {
		name string
		over routing.Override
	}{
		{"default", routing.Override{}},
		{"named", routing.Override{OrderNames: []string{"g2", "g1"}, OnlyNames: []string{"g1", "g2"}}},
		{"closed", routing.Override{NoFallbacks: true}},
		{"global", routing.Override{SortGlobal: true}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			a, c := endpoint(b, 1), endpoint(b, 2)
			a.Codename, c.Codename = "g1", "g2"
			h := newHarness(b, routing.Config{}, openPreset(), a, c)
			in := input()
			in.Override = tc.over
			b.ReportAllocs()
			for b.Loop() {
				h.plan(in)
			}
		})
	}
}
