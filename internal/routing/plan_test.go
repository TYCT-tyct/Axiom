//nolint:revive // the routing table and its helpers are one fixture; the file is the test
package routing_test

import (
	"errors"
	"regexp"
	"strconv"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// excluded plans for a request against one endpoint and returns the NoEndpoint, or fails if there is a plan.
func (h *harness) excluded(in routing.Input) routing.NoEndpoint {
	h.t.Helper()
	_, wait, err := h.r.Plan(in)
	var none *routing.NoEndpoint
	if wait != nil || !errors.As(err, &none) {
		h.t.Fatalf("want NoEndpoint, got wait %v, error %v", wait, err)
	}
	return *none
}

func price(tb testing.TB, s string) platform.UnitPrice {
	tb.Helper()
	p, err := platform.ParseUnitPrice(s)
	if err != nil {
		tb.Fatal(err)
	}
	return p
}

// TestFilters: each hard condition leaves the endpoint out with its reason, and permanent exactly for the reasons
// that waiting does not cure. Capability matching is among them.
func TestFilters(t *testing.T) {
	t.Parallel()
	tools := func(in *routing.Input) { in.Req.Needs = protocol.Tools }
	cases := []struct {
		name      string
		endpoint  func(*routing.Endpoint)
		request   func(*routing.Input)
		preset    func(*routing.Preset)
		reason    routing.Reason
		permanent bool
	}{
		{"disabled", func(e *routing.Endpoint) { e.Enabled = false }, nil, nil, routing.Disabled, true},
		{"breaker open", func(e *routing.Endpoint) { e.BreakerOpen = true }, nil, nil, routing.BreakerOpen, false},
		{"supplier balance", func(e *routing.Endpoint) { e.BalanceOK = false }, nil, nil, routing.SupplierBalance, false},
		{"no capability", func(e *routing.Endpoint) { e.Caps = 0 }, tools, nil, routing.MissingCapability, true},
		{
			"format only the original serves", func(e *routing.Endpoint) { e.Native = routing.Of(protocol.Chat) },
			func(in *routing.Input) { in.NativeOnly = true }, nil, routing.FormatNotServed, true,
		},
		{
			// Every pair of the three formats converts, so only an endpoint that serves none refuses a request's format.
			"format no endpoint serves", func(e *routing.Endpoint) { e.Served = 0; e.Native = 0 },
			nil, nil, routing.FormatNotServed, true,
		},
		{
			// The gateway reads every answer as a stream but the compact call's, whose answer arrives whole.
			"an endpoint that cannot stream", func(e *routing.Endpoint) { e.Streams = false },
			nil, nil, routing.FormatNotServed, true,
		},
		{"context too small", func(e *routing.Endpoint) { e.Context = 500 }, nil, nil, routing.ContextTooSmall, true},
		{
			"output does not fit the context", func(e *routing.Endpoint) { e.Context = 2000 },
			func(in *routing.Input) { in.Req.MaxOutput = 2000 }, nil, routing.ContextTooSmall, true,
		},
		{
			"preset class", func(e *routing.Endpoint) { e.Class = routing.ClassRelay }, nil,
			func(p *routing.Preset) { p.Classes = 1 << routing.ClassOfficial }, routing.PresetFilter, true,
		},
		{
			"preset data policy", func(e *routing.Endpoint) { e.Policy = routing.PolicyMayTrain }, nil,
			func(p *routing.Preset) { p.Policies = 1 << routing.PolicyNoRetention }, routing.PresetFilter, true,
		},
		{"preset quality", func(e *routing.Endpoint) { e.Quality = 0.4 }, nil, func(p *routing.Preset) { p.MinQuality = 0.5 }, routing.PresetFilter, true},
		{"preset confidence", func(e *routing.Endpoint) { e.Confidence = 0.4 }, nil, func(p *routing.Preset) { p.MinConfidence = 0.5 }, routing.PresetFilter, true},
		{"preset verified only", func(e *routing.Endpoint) { e.Verified = false }, nil, func(p *routing.Preset) { p.VerifiedOnly = true }, routing.PresetFilter, true},
		{"preset price limit", nil, nil, func(p *routing.Preset) {
			p.MaxPrice.Set.Add(catalog.ItemInput)
			p.MaxPrice.Max[catalog.ItemInput] = price(t, "0.0000005")
		}, routing.PresetFilter, true},
		{"override only another supplier", nil, func(in *routing.Input) { in.Override.OnlyNames = []string{"g99"} }, nil, routing.OverrideFilter, true},
		{"override excludes the supplier", nil, func(in *routing.Input) { in.Override.ExcludeNames = []string{"g1"} }, nil, routing.OverrideFilter, true},
		{"override class", nil, func(in *routing.Input) { in.Override.Classes = 1 << routing.ClassRelay }, nil, routing.OverrideFilter, true},
		{"override verified only", func(e *routing.Endpoint) { e.Verified = false }, func(in *routing.Input) { in.Override.VerifiedOnly = true }, nil, routing.NotVerified, false},
		{"override price limit", nil, func(in *routing.Input) {
			in.Override.MaxPrice.Set.Add(catalog.ItemOutput)
			in.Override.MaxPrice.Max[catalog.ItemOutput] = price(t, "0.000001")
		}, nil, routing.OverrideFilter, true},
		{"no sale price", func(e *routing.Endpoint) { e.Price = routing.PriceRef{} }, nil, nil, routing.NoPrice, true},
		{"an unknown billing item", func(e *routing.Endpoint) { e.UnknownItem = true }, nil, nil, routing.UnknownItem, true},
		{"no egress", func(e *routing.Endpoint) { e.Egresses = 0 }, nil, nil, routing.NoEgress, false},
	}
	for _, c := range cases {
		preset := openPreset()
		if c.preset != nil {
			c.preset(&preset)
		}
		h := newHarness(t, routing.Config{}, preset, endpoint(t, 1, func(e *routing.Endpoint) {
			if c.endpoint != nil {
				c.endpoint(e)
			}
		}))
		in := input()
		if c.request != nil {
			c.request(&in)
		}
		none := h.excluded(in)
		if len(none.Reasons) != 1 || none.Reasons[0] != (routing.Excluded{Endpoint: ep(1), Reason: c.reason}) || none.Permanent != c.permanent {
			t.Errorf("%s: %+v permanent %v, want %v permanent %v", c.name, none.Reasons, none.Permanent, c.reason, c.permanent)
		}
	}
}

// TestNothingToPlan covers the other ways a plan is empty: a model with no endpoints, a model the key may not use,
// and an endpoint the account cannot see, which is neither listed nor counted.
func TestNothingToPlan(t *testing.T) {
	t.Parallel()
	foreign := func(e *routing.Endpoint) { e.Owner = routing.Owner{Account: routing.AccountID(id(77))} }
	h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1, foreign))
	none := h.excluded(input())
	if len(none.Reasons) != 1 || none.Reasons[0].Reason != routing.NoCandidates || !none.Permanent {
		t.Errorf("an endpoint another account owns: %+v", none)
	}
	in := input()
	in.Admission.Allowed = map[catalog.ModelID]struct{}{other: {}}
	none = h.excluded(in)
	if none.Reasons[0].Reason != routing.NotAllowed || !none.Permanent {
		t.Errorf("a model the key may not use: %+v", none)
	}
	// What the platform, the site and the account itself own is visible.
	mine := func(e *routing.Endpoint) { e.Owner = routing.Owner{Account: acct} }
	theirSite := func(e *routing.Endpoint) { e.Owner = routing.Owner{Site: site} }
	h = newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1, mine), endpoint(t, 2, theirSite), endpoint(t, 3), endpoint(t, 4, foreign))
	if got := len(h.plan(input()).Entries); got != 3 {
		t.Errorf("%d entries from the 3 visible endpoints", got)
	}
}

// TestCapabilityNeverSelected is the acceptance criterion: whatever the weights, an endpoint without a capability the
// request needs is never in the route.
func TestCapabilityNeverSelected(t *testing.T) {
	t.Parallel()
	endpoints := []routing.Endpoint{
		endpoint(t, 1, func(e *routing.Endpoint) { e.Caps = protocol.Images; e.Quality = 1 }),
		endpoint(t, 2), endpoint(t, 3, func(e *routing.Endpoint) { e.Caps = 0 }),
	}
	h := newHarness(t, routing.Config{}, openPreset(), endpoints...)
	in := input()
	in.Req.Needs = protocol.Tools
	for range 500 {
		p := h.plan(in)
		if len(p.Entries) != 1 || first(p) != ep(2) {
			t.Fatalf("route %+v", p.Entries)
		}
	}
}

// TestOverridesOnlyTighten: the request's provider parameter cannot widen what the preset allows, and the lower of the
// two price limits wins.
func TestOverridesOnlyTighten(t *testing.T) {
	t.Parallel()
	relay := endpoint(t, 2, func(e *routing.Endpoint) { e.Class = routing.ClassRelay })
	official := endpoint(t, 1)
	preset := openPreset()
	preset.Classes = 1 << routing.ClassOfficial
	h := newHarness(t, routing.Config{}, preset, official, relay)
	in := input()
	in.Override.Classes = routing.AllClasses // asking for more changes nothing
	if p := h.plan(in); len(p.Entries) != 1 || first(p) != ep(1) {
		t.Errorf("an override widened the preset: %+v", p.Entries)
	}
	in.Override.Classes = 1 << routing.ClassRelay // asking for a different class leaves nothing
	h.excluded(in)

	// Price limits: the preset allows output up to 3e-6 and the override 1e-6; the lower applies, and the other way round.
	pricey := endpoint(t, 3, func(e *routing.Endpoint) {
		e.Price.Schedule = schedule(t, "0.000001", "0.0000025", "")
	})
	preset = openPreset()
	preset.MaxPrice.Set.Add(catalog.ItemOutput)
	preset.MaxPrice.Max[catalog.ItemOutput] = price(t, "0.000003")
	h = newHarness(t, routing.Config{}, preset, pricey)
	h.plan(input()) // within the preset's limit
	in = input()
	in.Override.MaxPrice.Set.Add(catalog.ItemOutput)
	in.Override.MaxPrice.Max[catalog.ItemOutput] = price(t, "0.000002")
	h.excluded(in)
	in.Override.MaxPrice.Max[catalog.ItemOutput] = price(t, "0.000009") // a higher limit does not lift the preset's
	preset.MaxPrice.Max[catalog.ItemOutput] = price(t, "0.000002")
	h.replace(preset, pricey)
	h.excluded(in)
}

// TestOrder: a named order comes first, whatever the strategy; the session's endpoint comes next within it; closing the
// fallbacks leaves only the named suppliers; performance wishes put the endpoints that miss them behind.
func TestOrder(t *testing.T) {
	t.Parallel()
	cheap := endpoint(t, 1, func(e *routing.Endpoint) { e.Price.Schedule = schedule(t, "0.0000001", "0.0000002", "") })
	mid := endpoint(t, 2)
	dear := endpoint(t, 3, func(e *routing.Endpoint) { e.Price.Schedule = schedule(t, "0.00001", "0.00002", "") })
	preset := openPreset()
	preset.Strategy = routing.Cheapest
	h := newHarness(t, routing.Config{}, preset, cheap, mid, dear)

	if got := h.plan(input()).Entries; got[0].Endpoint != ep(1) || got[1].Endpoint != ep(2) || got[2].Endpoint != ep(3) {
		t.Errorf("cheapest first: %+v", got)
	}
	in := input()
	in.Override.OrderNames = []string{"g3", "g2"}
	if got := h.plan(in).Entries; got[0].Endpoint != ep(3) || got[1].Endpoint != ep(2) || got[2].Endpoint != ep(1) {
		t.Errorf("named order, then the rest: %+v", got)
	}
	in.Override.NoFallbacks = true
	if got := h.plan(in).Entries; len(got) != 2 || got[0].Endpoint != ep(3) || got[1].Endpoint != ep(2) {
		t.Errorf("only the named suppliers: %+v", got)
	}
}

// TestSortAndPerformance: the request's sort replaces the preset's; a performance wish is a preference, so the endpoint
// that misses it goes behind and is not excluded.
func TestSortAndPerformance(t *testing.T) {
	t.Parallel()
	preset := openPreset()
	preset.Strategy = routing.Cheapest
	h := newHarness(t, routing.Config{}, preset, endpoint(t, 1, func(e *routing.Endpoint) { e.Throughput = 50 }),
		endpoint(t, 2, func(e *routing.Endpoint) { e.Throughput = 300 }))
	in := input()
	fastest := routing.Fastest
	in.Override.Sort = &fastest
	if got := h.plan(in).Entries; got[0].Endpoint != ep(2) {
		t.Errorf("the request's sort: %+v", got)
	}
	in.Override.MinThroughput = 100
	in.Override.Sort = nil
	if got := h.plan(in).Entries; len(got) != 2 || got[0].Endpoint != ep(2) {
		t.Errorf("a throughput wish: %+v", got)
	}
}

// TestPerformancePercentiles: the percentile form of the performance wishes reads the endpoint's rolling window, not
// the moving average, and an endpoint with no percentile measured misses neither.
func TestPerformancePercentiles(t *testing.T) {
	t.Parallel()
	p := openPreset()
	p.Strategy = routing.Cheapest
	h := newHarness(t, routing.Config{}, p,
		endpoint(t, 1, func(e *routing.Endpoint) {
			e.ThroughputPercentiles = [4]float64{500, 500, 500, 500}
			e.LatencyPercentiles = [4]time.Duration{time.Second, time.Second, time.Second, 5 * time.Second}
		}),
		endpoint(t, 2, func(e *routing.Endpoint) {
			e.ThroughputPercentiles = [4]float64{10, 10, 10, 10}
			e.LatencyPercentiles = [4]time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond}
		}),
		endpoint(t, 3))
	in := input()
	in.Override.MinThroughputPercentiles = routing.Cutoffs{Values: [4]float64{100}, Set: 1 << routing.P50}
	if got := h.plan(in).Entries; len(got) != 3 || got[2].Endpoint != ep(2) {
		t.Errorf("a throughput percentile wish: %+v", got)
	}
	in.Override.MinThroughputPercentiles = routing.Cutoffs{
		Values: [4]float64{100, 0, 0, 1000}, Set: 1<<routing.P50 | 1<<routing.P99,
	}
	if got := h.plan(in).Entries; got[0].Endpoint != ep(3) {
		t.Errorf("every named cutoff must hold; an unmeasured endpoint still misses none: %+v", got)
	}
	in.Override.MinThroughputPercentiles = routing.Cutoffs{}
	in.Override.MaxLatencyPercentiles = routing.Cutoffs{Values: [4]float64{0, 0, 0, 1}, Set: 1 << routing.P99}
	if got := h.plan(in).Entries; len(got) != 3 || got[2].Endpoint != ep(1) {
		t.Errorf("a latency percentile wish: %+v", got)
	}
}

// checkLimits checks the limits an entry carries are its endpoint's, as the helper endpoint has them.
func checkLimits(t *testing.T, i int, e routing.Entry) {
	t.Helper()
	if e.Limits.ID != e.Model || e.Limits.ContextLength != 1_000_000 || e.Limits.OutputSpeed != 100 || e.Limits.Reports != 1<<catalog.ItemCacheRead {
		t.Errorf("entry %d limits: %+v", i, e.Limits)
	}
}

// TestPlanBounds: at most three distinct entries; each carries its endpoint's price version, the output limit that
// applies and the limits billing quotes it by; the route is recorded.
func TestPlanBounds(t *testing.T) {
	t.Parallel()
	var endpoints []routing.Endpoint
	for n := 1; n <= 6; n++ {
		endpoints = append(endpoints, endpoint(t, n, func(e *routing.Endpoint) { e.MaxOutput = int64(1000 * n) }))
	}
	h := newHarness(t, routing.Config{}, openPreset(), endpoints...)
	in := input()
	in.Req.MaxOutput = 2500
	p := h.plan(in)
	if len(p.Entries) != routing.MaxEntries {
		t.Fatalf("%d entries", len(p.Entries))
	}
	seen := map[routing.EndpointID]bool{}
	for i, e := range p.Entries {
		n := int(e.Endpoint[15])
		want := min(int64(2500), int64(1000*n))
		if seen[e.Endpoint] || e.MaxOutput != want || e.Price.Version != uuid.UUID(id(100+n)) || e.Model != model || p.Record.Route[i] != e.Endpoint {
			t.Errorf("entry %d: %+v", i, e)
		}
		checkLimits(t, i, e)
		seen[e.Endpoint] = true
	}
	if p.MaxAttempts != 4 || !p.StartBy.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("attempts %d, start by %v", p.MaxAttempts, p.StartBy)
	}
}

// TestSegmentable: computed per entry and request.
func TestSegmentable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		e    func(*routing.Endpoint)
		in   func(*routing.Input)
		want bool
	}{
		{"all of it", nil, nil, true},
		// An endpoint that cannot stream is not routed for a generation call (TestFilters): the compact call, whose
		// answer arrives whole, is the one that reaches one, and it is never metered in segments.
		{"cannot stream", func(e *routing.Endpoint) { e.Streams = false }, func(in *routing.Input) { in.NoSegments = true }, false},
		{"does not cancel", func(e *routing.Endpoint) { e.Cancels = false }, nil, false},
		{"its output speed is not known", func(e *routing.Endpoint) { e.OutputSpeed = 0 }, nil, false},
		{"reasoning not visible in this format", func(e *routing.Endpoint) { e.ReasoningVisible = routing.Of(protocol.Chat) }, nil, false},
		// The visibility that matters is the called format's: this endpoint speaks only Chat and is called in it, so a
		// Messages stream's visibility says nothing about what the gateway reads.
		{"reasoning not visible in the format it is called in", func(e *routing.Endpoint) {
			e.Native, e.ReasoningVisible = routing.Of(protocol.Chat), routing.Of(protocol.Messages)
			e.Served = routing.Of(protocol.Messages, protocol.Chat)
		}, nil, false},
		{"the model does not reason", func(e *routing.Endpoint) {
			e.Caps &^= protocol.Reasoning
			e.ReasoningVisible = 0
		}, nil, true},
		{"the request hides reasoning", nil, func(in *routing.Input) { in.Req.HidesReasoning = true }, false},
		{"the limit is near the overshoot", nil, func(in *routing.Input) { in.Req.MaxOutput = 1500 }, false},
		{"just above it", nil, func(in *routing.Input) { in.Req.MaxOutput = 1501 }, true},
		{"the gateway cannot meter the call", nil, func(in *routing.Input) { in.NoSegments = true }, false},
	}
	for _, c := range cases {
		// 1000 output tokens a segment, with an allowance of 500 at the endpoint's 100 tokens a second: 5 seconds.
		h := newHarness(t, routing.Config{Segments: billing.Segments{Output: 1000, Overshoot: 5 * time.Second}}, openPreset(), endpoint(t, 1, func(e *routing.Endpoint) {
			if c.e != nil {
				c.e(e)
			}
		}))
		in := input()
		if c.in != nil {
			c.in(&in)
		}
		if got := h.plan(in).Entries[0].Segmentable; got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// With no segment size configured, which is M1's, every entry is reserved whole: a segment of 0 tokens would
	// reserve no output at all.
	h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1, func(*routing.Endpoint) {}))
	if h.plan(input()).Entries[0].Segmentable {
		t.Error("an entry is segmentable though no segment size is configured")
	}
}

// TestWait: when every endpoint that passed is full the answer is a wait, not a refusal; a full endpoint beside one
// with room is left out of the route; a refusal beside a full one is still a wait.
func TestWait(t *testing.T) {
	t.Parallel()
	limited := func(e *routing.Endpoint) { e.MaxConcurrent = 1 }
	h := newHarness(t, routing.Config{RetryAfter: 3 * time.Second}, openPreset(), endpoint(t, 1, limited), endpoint(t, 2, limited))
	h.r.Begin(ep(1))
	if p := h.plan(input()); len(p.Entries) != 1 || first(p) != ep(2) {
		t.Errorf("one full: %+v", p.Entries)
	}
	h.r.Begin(ep(2))
	_, wait, err := h.r.Plan(input())
	if err != nil || wait == nil || wait.RetryAfter != 3*time.Second {
		t.Errorf("all full: wait %v, error %v", wait, err)
	}
	h.r.End(ep(1))
	if p := h.plan(input()); first(p) != ep(1) {
		t.Errorf("room again: %+v", p.Entries)
	}
	h.replace(openPreset(), endpoint(t, 1, limited), endpoint(t, 2, func(e *routing.Endpoint) { e.Enabled = false }))
	h.r.End(ep(2))
	h.r.Begin(ep(1))
	if _, wait, err := h.r.Plan(input()); wait == nil || err != nil {
		t.Errorf("a full endpoint beside a refused one: wait %v, error %v", wait, err)
	}
}

// TestServes: a model is served when the snapshot has an endpoint for it, whatever state the endpoint is in right now,
// and however old the snapshot is; a model without one, or a router without a snapshot, serves nothing.
func TestServes(t *testing.T) {
	t.Parallel()
	if mustRouter(t).ServesFor(model, billing.Audience{}) {
		t.Error("a router without a snapshot serves a model")
	}
	h := newHarness(t, routing.Config{}, openPreset(),
		endpoint(t, 1, func(e *routing.Endpoint) { e.Enabled, e.BreakerOpen, e.MaxConcurrent = false, true, 1 }))
	h.r.Begin(ep(1))
	h.now = t0.Add(time.Hour) // far past MaxStale: Plan refuses, the list still knows the route
	if _, _, err := h.r.Plan(input()); !errors.Is(err, routing.ErrStale) {
		t.Fatalf("a snapshot an hour old: %v, want ErrStale", err)
	}
	if !h.r.ServesFor(model, billing.Audience{}) || h.r.ServesFor(other, billing.Audience{}) {
		t.Errorf("served: model %v, other %v; want true, false", h.r.ServesFor(model, billing.Audience{}), h.r.ServesFor(other, billing.Audience{}))
	}
}

// TestNoEndpointPermanence: a mix of permanent and temporary reasons is temporary, since waiting may cure it.
func TestNoEndpointPermanence(t *testing.T) {
	t.Parallel()
	h := newHarness(t, routing.Config{}, openPreset(),
		endpoint(t, 1, func(e *routing.Endpoint) { e.Enabled = false }), endpoint(t, 2, func(e *routing.Endpoint) { e.BreakerOpen = true }))
	none := h.excluded(input())
	if none.Permanent || len(none.Reasons) != 2 {
		t.Errorf("%+v", none)
	}
}

// TestSnapshotStaleness: a snapshot older than the limit is not used, an older build never replaces a newer one, and a
// preset the snapshot lacks is an error.
func TestSnapshotStaleness(t *testing.T) {
	t.Parallel()
	h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1))
	h.plan(input())
	h.now = t0.Add(30 * time.Second)
	h.plan(input()) // at the limit
	if !h.r.Fresh() {
		t.Error("at the limit: Fresh is false, but Plan uses the snapshot")
	}
	h.now = t0.Add(30*time.Second + time.Nanosecond)
	if _, _, err := h.r.Plan(input()); !errors.Is(err, routing.ErrStale) {
		t.Errorf("past the limit: %v", err)
	}
	if h.r.Fresh() {
		t.Error("past the limit: Fresh is true, but Plan refuses the snapshot")
	}
	old, err := h.r.NewSnapshot(t0.Add(-time.Hour), nil, []routing.Preset{openPreset()})
	if err != nil {
		t.Fatal(err)
	}
	h.replace(openPreset(), endpoint(t, 1))
	h.r.Replace(old)
	h.plan(input())
	in := input()
	in.Admission.Preset = routing.PresetID(id(55))
	if _, _, err := h.r.Plan(in); !errors.Is(err, routing.ErrUnknownPreset) {
		t.Errorf("unknown preset: %v", err)
	}
	empty := mustRouter(t)
	if _, _, err := empty.Plan(input()); !errors.Is(err, routing.ErrStale) {
		t.Errorf("no snapshot: %v", err)
	}
	if empty.Fresh() {
		t.Error("no snapshot: Fresh is true")
	}
}

// TestSnapshotRejectsBadData: mistakes in the data fail at build.
func TestSnapshotRejectsBadData(t *testing.T) {
	t.Parallel()
	r := mustRouter(t)
	for name, c := range map[string]struct {
		endpoints []routing.Endpoint
		presets   []routing.Preset
	}{
		"an endpoint twice":          {[]routing.Endpoint{endpoint(t, 1), endpoint(t, 1)}, []routing.Preset{openPreset()}},
		"a preset twice":             {nil, []routing.Preset{openPreset(), openPreset()}},
		"a preset that allows none":  {nil, []routing.Preset{{ID: pre}}},
		"serves less than it speaks": {[]routing.Endpoint{endpoint(t, 1, func(e *routing.Endpoint) { e.Served = 0 })}, []routing.Preset{openPreset()}},
	} {
		if _, err := r.NewSnapshot(t0, c.endpoints, c.presets); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestEntryTellsHowToCallTheEndpoint: an entry carries the supplier and the endpoint's target, with the format the
// request is called in — its own when the endpoint serves it, for a Chat request also when the endpoint speaks Chat, else the
// one it is converted to (docs/domains/gateway.md「转发规则」) — and whether the endpoint
// streams, which every endpoint of a call read as a stream must. The compact call's answer arrives whole, so it is the
// one request an endpoint that does not stream can serve.
func TestEntryTellsHowToCallTheEndpoint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		client     protocol.Format
		native     routing.FormatSet
		streams    bool
		noSegments bool
		want       protocol.Format
	}{
		{"its own format", protocol.Messages, routing.Of(protocol.Messages, protocol.Chat), true, false, protocol.Messages},
		{"chat to an endpoint that speaks both", protocol.Chat, routing.Of(protocol.Messages, protocol.Chat), true, false, protocol.Chat},
		{"chat to chat", protocol.Chat, routing.Of(protocol.Chat), true, false, protocol.Chat},
		{"chat prefers its own format", protocol.Chat, routing.Of(protocol.Responses, protocol.Messages, protocol.Chat), true, false, protocol.Chat},
		{"chat to messages", protocol.Chat, routing.Of(protocol.Messages), true, false, protocol.Messages},
		{"chat to responses", protocol.Chat, routing.Of(protocol.Responses), true, false, protocol.Responses},
		{"chat prefers messages", protocol.Chat, routing.Of(protocol.Responses, protocol.Messages), true, false, protocol.Messages},
		{"the compact call needs no stream", protocol.Responses, routing.Of(protocol.Responses), false, true, protocol.Responses},
	}
	for _, c := range cases {
		e := endpoint(t, 1, func(e *routing.Endpoint) {
			e.Native, e.Served, e.Streams = c.native, routing.Of(protocol.Chat)|c.native, c.streams
			e.Target = protocol.Target{Model: "upstream-name", EncryptedReasoning: catalog.ClaimYes, Format: protocol.Responses}
		})
		in := input()
		in.Req.Format, in.NoSegments = c.client, c.noSegments
		entry := newHarness(t, routing.Config{}, openPreset(), e).plan(in).Entries[0]
		if entry.Target.Format != c.want || entry.Target.Model != "upstream-name" || entry.Target.EncryptedReasoning != catalog.ClaimYes ||
			entry.Target.Streams != c.streams || entry.Supplier != sup(1) {
			t.Errorf("%s: supplier %v, target %+v, want format %d streaming %t", c.name, entry.Supplier, entry.Target, c.want, c.streams)
		}
		// The reply's usage follows the call format: only an OpenAI call reserves for cache reads and writes that overlap.
		if want := c.want != protocol.Messages; entry.Limits.OpenAIUsage != want {
			t.Errorf("%s: OpenAIUsage = %t, want %t", c.name, entry.Limits.OpenAIUsage, want)
		}
	}
}

// TestOutputCapQuotedIsTheCapSent: a request that sets no output cap is reserved at the entry's limit, which the
// context may have cut below the model's default, so that limit, not the default, is what the upstream is told, in
// each way a request is called (I1). A model whose limit is unknown has no default to send.
func TestOutputCapQuotedIsTheCapSent(t *testing.T) {
	t.Parallel()
	chat := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	cases := []struct {
		name    string
		client  protocol.Format
		native  routing.FormatSet
		body    string
		member  string // where the cap is in the body sent
		context int64  // the input is estimated at 1000 tokens
		limit   int64  // the endpoint's output limit and default, 0 for unknown
		want    int64  // the cap sent, 0 for none
	}{
		{"chat to messages, cut by the context", protocol.Chat, routing.Of(protocol.Messages), chat, `"max_tokens":`, 8000, 8000, 7000},
		{"chat to responses, cut by the context", protocol.Chat, routing.Of(protocol.Responses), chat, `"max_output_tokens":`, 8000, 8000, 7000},
		{"responses to responses, cut by the context", protocol.Responses, routing.Of(protocol.Responses), `{"model":"m","input":"hi"}`, `"max_output_tokens":`, 8000, 8000, 7000},
		{"the default fits whole", protocol.Responses, routing.Of(protocol.Responses), `{"model":"m","input":"hi"}`, `"max_output_tokens":`, 100000, 8000, 8000},
		{"the limit is unknown", protocol.Responses, routing.Of(protocol.Responses), `{"model":"m","input":"hi"}`, `"max_output_tokens":`, 8000, 0, 0},
	}
	for _, c := range cases {
		e := endpoint(t, 1, func(e *routing.Endpoint) {
			e.Native, e.Served, e.Context, e.MaxOutput = c.native, c.native|routing.Of(protocol.Chat), c.context, c.limit
		})
		in := input()
		in.Req.Format = c.client
		entry := newHarness(t, routing.Config{}, openPreset(), e).plan(in).Entries[0]
		r, err := protocol.Inspect(c.client, []byte(c.body))
		if err != nil {
			t.Fatal(err)
		}
		u, err := protocol.Prepare(&r, []byte(c.body), entry.Target, protocol.Rewrites{Identity: func(s string) string { return s }})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		sent := int64(0)
		if m := regexp.MustCompile(regexp.QuoteMeta(c.member) + `(\d+)`).FindSubmatch(u.Body); m != nil {
			if sent, err = strconv.ParseInt(string(m[1]), 10, 64); err != nil {
				t.Fatal(err)
			}
		}
		if sent != c.want || c.limit > 0 && sent != entry.MaxOutput {
			t.Errorf("%s: the upstream is sent a cap of %d, the entry is quoted at %d, want %d\n%s", c.name, sent, entry.MaxOutput, c.want, u.Body)
		}
	}
}

// TestEntryDirectFormats: a direct call is read whole, so an endpoint need not stream it, and it is never converted:
// a model that does not list the format has no entry. A generation is read as a stream, so an endpoint that cannot
// stream it is left out (docs/domains/gateway.md「转发规则」).
func TestEntryDirectFormats(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		format  protocol.Format
		streams bool
		want    bool
	}{
		{protocol.Embeddings, false, true},
		{protocol.Rerank, false, true},
		{protocol.Completions, true, true},
		{protocol.Completions, false, false},
		{protocol.Chat, true, true},
		{protocol.Chat, false, false},
	} {
		e := endpoint(t, 1, func(e *routing.Endpoint) {
			e.Native, e.Served, e.Streams = routing.Of(tc.format), routing.Of(tc.format), tc.streams
			e.Target = protocol.Target{Model: "upstream-name"}
		})
		in := input()
		in.Req.Format, in.NativeOnly, in.NoSegments = tc.format, true, tc.format.Direct()
		plan, wait, err := newHarness(t, routing.Config{}, openPreset(), e).r.Plan(in)
		if wait != nil {
			t.Fatalf("%v streaming %t: wait %v", tc.format, tc.streams, wait)
		}
		if got := len(plan.Entries) == 1; got != tc.want {
			t.Errorf("%v streaming %t: %d entries, error %v, want one: %t", tc.format, tc.streams, len(plan.Entries), err, tc.want)
			continue
		}
		if !tc.want {
			continue
		}
		entry := plan.Entries[0]
		if entry.Target.Format != tc.format || entry.Target.Streams != tc.streams || entry.Segmentable {
			t.Errorf("%v streaming %t: target %+v, segmentable %t", tc.format, tc.streams, entry.Target, entry.Segmentable)
		}
	}
	// A chat endpoint does not serve a direct format or a legacy completion: neither is converted to it, whatever
	// NativeOnly says.
	for _, f := range []protocol.Format{protocol.Embeddings, protocol.Completions} {
		e := endpoint(t, 1, func(e *routing.Endpoint) {
			e.Native, e.Served = routing.Of(protocol.Chat), routing.Of(protocol.Chat)
		})
		in := input()
		in.Req.Format, in.NativeOnly = f, f.Direct()
		plan, _, _ := newHarness(t, routing.Config{}, openPreset(), e).r.Plan(in) //nolint:errcheck // no endpoint is the expected permanent result here
		if len(plan.Entries) != 0 {
			t.Errorf("%v on a chat endpoint: %d entries", f, len(plan.Entries))
		}
	}
}

// TestCompletionsNeverSegment: a completion is reserved in full even when segmentation is configured and the call
// would qualify: every prompt and every choice generate, and the batch's output bound is promised whole
// (docs/domains/billing.md「决策与理由」).
func TestCompletionsNeverSegment(t *testing.T) {
	t.Parallel()
	e := endpoint(t, 1, func(e *routing.Endpoint) {
		e.Native, e.Served = routing.Of(protocol.Completions), routing.Of(protocol.Completions)
	})
	in := input()
	in.Req = &protocol.Request{
		Format: protocol.Completions, Model: "m", Choices: 2, TextBytes: 10, MaxInputBytes: 5,
		MaxOutput: 4096, MaxOutputSet: true, Stream: true, ServiceTiers: 1 << catalog.ServiceTierStandard,
	}
	in.NativeOnly = true
	h := newHarness(t, routing.Config{Segments: billing.Segments{Output: 512, Overshoot: time.Second}}, openPreset(), e)
	entry := h.plan(in).Entries[0]
	if entry.Segmentable {
		t.Error("a completion entry is segmentable: its output would be reserved one segment at a time")
	}
	if entry.MaxOutput != 4096 {
		t.Errorf("output cap %d, want the request's 4096 for all %d choices", entry.MaxOutput, in.Req.Choices)
	}
}

// TestCompletionsExplicitZeroCap: legacy completions' max_tokens 0 is a request for no output (the prompt's logprobs
// with echo), not a missing cap: routing must not replace it with the endpoint's default and reserve output that cannot
// be generated. Omitting the member still takes the default.
func TestCompletionsExplicitZeroCap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		set     bool
		wantCap int64
	}{
		{name: "explicit zero", set: true, wantCap: 0},
		{name: "unset", set: false, wantCap: 8192},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := endpoint(t, 1, func(e *routing.Endpoint) {
				e.Native, e.Served = routing.Of(protocol.Completions), routing.Of(protocol.Completions)
			})
			in := input()
			in.Req = &protocol.Request{
				Format: protocol.Completions, Model: "m", Choices: 1, TextBytes: 10, MaxInputBytes: 10,
				MaxOutput: 0, MaxOutputSet: tc.set, Stream: true, ServiceTiers: 1 << catalog.ServiceTierStandard,
			}
			in.NativeOnly = true
			entry := newHarness(t, routing.Config{}, openPreset(), e).plan(in).Entries[0]
			if entry.MaxOutput != tc.wantCap || entry.Target.MaxOutput != tc.wantCap {
				t.Errorf("entry cap %d, target cap %d, want %d", entry.MaxOutput, entry.Target.MaxOutput, tc.wantCap)
			}
		})
	}
}

// TestCompletionsDemandIsABatch: the prompt array is one price banded at the total of its prompts, whose longest takes
// the context check, so billing reads them apart.
func TestCompletionsDemandIsABatch(t *testing.T) {
	d := routing.DemandOf(&protocol.Request{Format: protocol.Completions, TextBytes: 9000, MaxInputBytes: 4000, Choices: 6})
	if d.TextBytes != 9000 || d.InputBytes != 4000 || d.Choices != 6 || d.Direct {
		t.Errorf("demand %+v", d)
	}
}

// TestRerankDemandReservesItsSearchUnit: a rerank request does not ask for the model's web_search ability, but its
// reported search_units are priced as web searches, so the reservation must include one (protocol 的「直接格式」).
func TestRerankDemandReservesItsSearchUnit(t *testing.T) {
	d := routing.DemandOf(&protocol.Request{Format: protocol.Rerank, WebSearches: 1})
	if len(d.ServerTools) != 1 || d.ServerTools[0].Item != catalog.ItemWebSearch || d.ServerTools[0].MaxUses != 1 {
		t.Fatalf("rerank demand server tools = %+v, want one web search", d.ServerTools)
	}
	chat := routing.DemandOf(&protocol.Request{Format: protocol.Chat})
	if len(chat.ServerTools) != 0 {
		t.Errorf("a chat request without web search demand got %+v", chat.ServerTools)
	}
}

// TestFileParsesAreFlatFileParses: each file the gateway parses before admission is one per-use file_parse unit that
// does not add a sampling, so the reservation covers it and the request's own search count is left alone.
func TestFileParsesAreFlatFileParses(t *testing.T) {
	d := routing.DemandOf(&protocol.Request{Format: protocol.Chat, FileParses: 3, Needs: protocol.WebSearch, WebSearches: 2})
	var flat, native int
	for _, tool := range d.ServerTools {
		switch {
		case tool.Item == catalog.ItemFileParse && tool.Flat:
			flat++
			if tool.MaxUses != 3 {
				t.Errorf("flat uses = %d, want 3", tool.MaxUses)
			}
		case tool.Item == catalog.ItemWebSearch && !tool.Flat:
			native++
			if tool.MaxUses != 2 {
				t.Errorf("native uses = %d, want 2", tool.MaxUses)
			}
		default:
			t.Fatalf("server tool %+v, want a flat file_parse and the native web_search", tool)
		}
	}
	if flat != 1 || native != 1 {
		t.Fatalf("server tools = %+v, want one flat parse unit and the native search", d.ServerTools)
	}
	if d.Documents != 0 {
		t.Errorf("documents = %d, want none after parsing", d.Documents)
	}
}

// TestEachEndpointIsFilteredOnItsOwnPrice: provider.max_price and provider.sort compare the price of the endpoint being
// considered, not a model-wide price that no endpoint charges (docs/domains/billing.md「定价」; OpenRouter《Provider
// Routing》「Price Limits」).
func TestEachEndpointIsFilteredOnItsOwnPrice(t *testing.T) {
	t.Parallel()

	cheap := endpoint(t, 1)
	dear := endpoint(t, 2, func(e *routing.Endpoint) {
		e.Price.Schedule = schedule(t, "0.000003", "0.000004", "")
	})
	h := newHarness(t, routing.Config{}, openPreset(), cheap, dear)
	in := input()
	cheapest := routing.Cheapest
	in.Override.Sort = &cheapest
	p := h.plan(in)
	if len(p.Entries) != 2 || p.Entries[0].Endpoint != cheap.ID {
		t.Fatalf("cheapest order: %v", p.Entries)
	}
	if got := p.Entries[0].Price.Version; got != cheap.Price.Version {
		t.Errorf("the cheap endpoint carries version %s, want %s", got, cheap.Price.Version)
	}
	in.Override.MaxPrice = limitOn(t, catalog.ItemInput, "0.000002")
	p = h.plan(in)
	if len(p.Entries) != 1 || p.Entries[0].Endpoint != cheap.ID {
		t.Fatalf("the cap kept %v, want only the cheap endpoint", p.Entries)
	}
	in.Override.MaxPrice = limitOn(t, catalog.ItemOutput, "0.000003")
	if p := h.plan(in); len(p.Entries) != 1 || p.Entries[0].Endpoint != cheap.ID {
		t.Fatalf("the output cap kept %v, want only the cheap endpoint", p.Entries)
	}
}
