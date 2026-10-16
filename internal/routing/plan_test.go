package routing_test

import (
	"errors"
	"testing"
	"time"
	"uuid"

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
			"format nobody converts to", func(e *routing.Endpoint) { e.Served = routing.Of(protocol.Chat); e.Native = routing.Of(protocol.Chat) },
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
		{"override only another supplier", nil, func(in *routing.Input) { in.Override.Only = []routing.SupplierID{sup(99)} }, nil, routing.OverrideFilter, true},
		{"override excludes the supplier", nil, func(in *routing.Input) { in.Override.Exclude = []routing.SupplierID{sup(1)} }, nil, routing.OverrideFilter, true},
		{"override class", nil, func(in *routing.Input) { in.Override.Classes = 1 << routing.ClassRelay }, nil, routing.OverrideFilter, true},
		{"override price limit", nil, func(in *routing.Input) {
			in.Override.MaxPrice.Set.Add(catalog.ItemOutput)
			in.Override.MaxPrice.Max[catalog.ItemOutput] = price(t, "0.000001")
		}, nil, routing.OverrideFilter, true},
		{"no sale price", func(e *routing.Endpoint) { e.Price = routing.PriceRef{} }, nil, nil, routing.NoPrice, true},
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
	in.Override.Order = []routing.SupplierID{sup(3), sup(2)}
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

// TestPlanBounds: at most three distinct entries; each carries its endpoint's price version and the output limit that
// applies; the route is recorded.
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
		{"cannot stream", func(e *routing.Endpoint) { e.Streams = false }, nil, false},
		{"does not cancel", func(e *routing.Endpoint) { e.Cancels = false }, nil, false},
		{"reasoning not visible in this format", func(e *routing.Endpoint) { e.ReasoningVisible = routing.Of(protocol.Chat) }, nil, false},
		{"the request hides reasoning", nil, func(in *routing.Input) { in.Req.HidesReasoning = true }, false},
		{"the limit is near the overshoot", nil, func(in *routing.Input) { in.Req.MaxOutput = 1500 }, false},
		{"just above it", nil, func(in *routing.Input) { in.Req.MaxOutput = 1501 }, true},
	}
	for _, c := range cases {
		h := newHarness(t, routing.Config{FirstSegment: 1000, Overshoot: 500}, openPreset(), endpoint(t, 1, func(e *routing.Endpoint) {
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
	h.now = t0.Add(30*time.Second + time.Nanosecond)
	if _, _, err := h.r.Plan(input()); !errors.Is(err, routing.ErrStale) {
		t.Errorf("past the limit: %v", err)
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
