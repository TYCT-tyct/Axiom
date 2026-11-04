package routing_test

import (
	"slices"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/routing"
)

// tiered is a schedule with a standard price, a priority price and, above 100000 prompt tokens, a dearer standard
// input price, so the price a request pays depends on the tier and the length.
func tiered(tb testing.TB, standard, priority, long string) catalog.Schedule {
	tb.Helper()
	s, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"` + standard + `","output":"0.000002"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":100000,"rates":{"input":"` + long + `"}}]},
		"service_tiers":{"priority":{"rates":{"input":"` + priority + `","output":"0.000004"},
			"context_tiers":[{"above":100000,"rates":{}}]}}}`))
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

func limitOn(tb testing.TB, it catalog.Item, max string) routing.PriceLimit {
	var l routing.PriceLimit
	l.Set.Add(it)
	p, err := platform.ParseUnitPrice(max)
	if err != nil {
		tb.Fatal(err)
	}
	l.Max[it] = p
	return l
}

// TestPriceLimitBindsTheTierBilled: the limit is held against the highest price the request may be charged, over the
// service tiers it may be served at and the length it may reach, not against the standard tier's base rate.
func TestPriceLimitBindsTheTierBilled(t *testing.T) {
	t.Parallel()
	// Input costs 1e-6 at standard, 3e-6 at priority, and 5e-6 at standard above 100000 tokens; the limit is 2e-6.
	dear := endpoint(t, 1, func(e *routing.Endpoint) { e.Price.Schedule = tiered(t, "0.000001", "0.000003", "0.000005") })
	preset := openPreset()
	preset.MaxPrice = limitOn(t, catalog.ItemInput, "0.000002")
	h := newHarness(t, routing.Config{}, preset, dear)
	cases := []struct {
		name  string
		tiers catalog.ServiceTierSet
		bytes int64
		fits  bool
	}{
		{"a short standard request", 1 << catalog.ServiceTierStandard, 4000, true},
		{"a priority request", 1 << catalog.ServiceTierPriority, 4000, false},
		{"auto may be served at priority", 1<<catalog.ServiceTierStandard | 1<<catalog.ServiceTierPriority, 4000, false},
		{"a long standard request", 1 << catalog.ServiceTierStandard, 400000, false},
	}
	for _, c := range cases {
		in := input()
		in.Req.ServiceTiers, in.Req.TextBytes = c.tiers, c.bytes
		_, _, err := h.r.Plan(in)
		if (err == nil) != c.fits {
			t.Errorf("%s: error %v, want fits %v", c.name, err, c.fits)
		}
	}
	// The request's own limit binds the same way.
	h = newHarness(t, routing.Config{}, openPreset(), dear)
	in := input()
	in.Req.ServiceTiers = 1 << catalog.ServiceTierPriority
	in.Override.MaxPrice = limitOn(t, catalog.ItemInput, "0.000002")
	if _, _, err := h.r.Plan(in); err == nil {
		t.Error("the request's limit let a priority request through")
	}
}

// TestNamedOrderDoesNotLiftNoFallbacks: a preset that closes the fallbacks keeps them closed whatever order the request
// names, or whether it names the same one.
func TestNamedOrderDoesNotLiftNoFallbacks(t *testing.T) {
	t.Parallel()
	preset := openPreset()
	preset.NoFallbacks, preset.OrderNames = true, []string{"g1"}
	h := newHarness(t, routing.Config{}, preset, endpoint(t, 1), endpoint(t, 2), endpoint(t, 3))
	for name, order := range map[string][]string{
		"no order": nil, "the same order": {"g1"}, "another order": {"g2", "g3"}, "a longer order": {"g1", "g2"},
	} {
		in := input()
		in.Override.OrderNames = order
		p, _, err := h.r.Plan(in)
		if err != nil || len(p.Entries) != 1 || first(p) != ep(1) {
			t.Errorf("%s: %v %v", name, p.Entries, err)
		}
	}
	r := mustRouter(t)
	bad := openPreset()
	bad.NoFallbacks = true
	if _, err := r.NewSnapshot(t0, nil, []routing.Preset{bad}); err == nil {
		t.Error("a preset that closes the fallbacks and names no suppliers")
	}
}

// TestServiceTierMatching: an endpoint that offers none of the tiers a request may be served at is left out, for each
// tier, and the reason is permanent.
func TestServiceTierMatching(t *testing.T) {
	t.Parallel()
	only := func(tiers ...catalog.ServiceTier) func(*routing.Endpoint) {
		return func(e *routing.Endpoint) {
			e.ServiceTiers = 0
			for _, st := range tiers {
				e.ServiceTiers.Add(st)
			}
		}
	}
	standard, flex, priority, fast := catalog.ServiceTierStandard, catalog.ServiceTierFlex, catalog.ServiceTierPriority, catalog.ServiceTierFast
	h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1, only(standard)), endpoint(t, 2, only(flex, priority)), endpoint(t, 3, only(fast)))
	set := func(tiers ...catalog.ServiceTier) (s catalog.ServiceTierSet) {
		for _, st := range tiers {
			s.Add(st)
		}
		return s
	}
	for _, c := range []struct {
		name  string
		tiers catalog.ServiceTierSet
		want  []int
	}{
		{"standard", set(standard), []int{1}},
		{"flex", set(flex), []int{2}},
		{"priority", set(priority), []int{2}},
		{"fast", set(fast), []int{3}},
		{"auto: standard or priority", set(standard, priority), []int{1, 2}},
	} {
		in := input()
		in.Req.ServiceTiers = c.tiers
		var got []int
		for _, e := range h.plan(in).Entries {
			got = append(got, int(e.Endpoint[15]))
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: endpoints %v, want %v", c.name, got, c.want)
		}
	}
	h = newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1, only(standard)))
	in := input()
	in.Req.ServiceTiers = set(priority)
	none := h.excluded(in)
	if none.Reasons[0].Reason != routing.ServiceTierNotServed || !none.Permanent {
		t.Errorf("%+v", none)
	}
	if _, err := mustRouter(t).NewSnapshot(t0, []routing.Endpoint{endpoint(t, 1, func(e *routing.Endpoint) { e.ServiceTiers = 0 })}, nil); err == nil {
		t.Error("an endpoint that offers no tier")
	}
}

// TestContextAndOutputShareOneRule: the output limit that decides whether the request fits is the one the entry carries.
func TestContextAndOutputShareOneRule(t *testing.T) {
	t.Parallel()
	// The input is estimated at 1000 tokens (4000 bytes).
	cases := []struct {
		name     string
		context  int64
		endpoint int64 // the endpoint's output limit and default
		request  int64 // the request's own limit, 0 for none
		want     int64 // the entry's limit, 0 when the endpoint is left out
	}{
		{"the default is cut to what the context leaves", 8000, 8000, 0, 7000},
		{"the default fits whole", 100000, 8000, 0, 8000},
		{"the request's limit fits", 8000, 8000, 2000, 2000},
		{"the request's limit is held to the endpoint's", 8000, 2000, 10000, 2000},
		{"the request's limit does not fit the context", 8000, 8000, 7500, 0},
		{"no room for any output", 1000, 8000, 0, 0},
	}
	for _, c := range cases {
		h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1, func(e *routing.Endpoint) { e.Context, e.MaxOutput = c.context, c.endpoint }))
		in := input()
		in.Req.MaxOutput = c.request
		p, _, err := h.r.Plan(in)
		got := int64(0)
		if err == nil {
			got = p.Entries[0].MaxOutput
		}
		if got != c.want {
			t.Errorf("%s: limit %d (%v), want %d", c.name, got, err, c.want)
		}
	}
}

// TestSessionKeyIsStableForShortConversations: the key rests on the conversation's first message, so later messages and
// the fields after the conversation do not change it; a different first message does.
func TestSessionKeyIsStableForShortConversations(t *testing.T) {
	t.Parallel()
	r := mustRouter(t)
	first := `{"role":"user","content":"hello there"}`
	bodies := []string{
		`{"model":"m","messages":[` + first + `]}`,
		`{"model":"m","messages":[` + first + `,{"role":"assistant","content":"hi"},{"role":"user","content":"more"}]}`,
		`{"model":"m","messages":[` + first + `],"temperature":0.2,"stream":true,"tools":[{"name":"x"}]}`,
		`{"model":"m", "messages": [ ` + first + ` ], "max_tokens": 7}`,
	}
	want := key(t, r, acct, "m", bodies[0])
	for i, b := range bodies[1:] {
		if key(t, r, acct, "m", b) != want {
			t.Errorf("body %d changed the key", i+1)
		}
	}
	if key(t, r, acct, "m", `{"model":"m","messages":[{"role":"user","content":"another start"}]}`) == want {
		t.Error("another first message gave the same key")
	}
	// Responses: input may be a string.
	if key(t, r, acct, "m", `{"input":"same start","stream":true}`) != key(t, r, acct, "m", `{"stream":false,"input":"same start"}`) {
		t.Error("a string input is not stable")
	}
}

// TestMaxAttemptsIsAHardLimit: the configured most attempts is never raised to the length of the route; entries past it
// are dropped.
func TestMaxAttemptsIsAHardLimit(t *testing.T) {
	t.Parallel()
	for attempts, wantEntries := range map[int]int{1: 1, 2: 2, 3: 3, 5: 3} {
		preset := openPreset()
		preset.MaxAttempts = attempts
		h := newHarness(t, routing.Config{}, preset, endpoint(t, 1), endpoint(t, 2), endpoint(t, 3), endpoint(t, 4))
		p := h.plan(input())
		if p.MaxAttempts != attempts || len(p.Entries) != wantEntries || len(p.Record.Route) != wantEntries {
			t.Errorf("MaxAttempts %d: plan allows %d attempts over %d entries, want %d and %d", attempts, p.MaxAttempts, len(p.Entries), attempts, wantEntries)
		}
	}
	h := newHarness(t, routing.Config{MaxAttempts: 2}, openPreset(), endpoint(t, 1), endpoint(t, 2), endpoint(t, 3))
	if p := h.plan(input()); p.MaxAttempts != 2 || len(p.Entries) != 2 {
		t.Errorf("the router's default: %d attempts, %d entries", p.MaxAttempts, len(p.Entries))
	}
	// A request naming a file by an upstream's id gets one attempt, however long the route would otherwise be.
	ref := input()
	ref.FileRef = true
	if p := h.plan(ref); p.MaxAttempts != 1 || len(p.Entries) != 1 {
		t.Errorf("a file reference: %d attempts, %d entries", p.MaxAttempts, len(p.Entries))
	}
}

// TestRepeatedModelsGiveDistinctEndpoints: a model named twice is ranked once, so no endpoint is in the route twice.
func TestRepeatedModelsGiveDistinctEndpoints(t *testing.T) {
	t.Parallel()
	h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1), endpoint(t, 2, func(e *routing.Endpoint) { e.Model = other }))
	in := input()
	in.Models = []catalog.ModelID{model, model, other, model}
	p := h.plan(in)
	got := []routing.EndpointID{}
	for _, e := range p.Entries {
		got = append(got, e.Endpoint)
	}
	if !slices.Equal(got, []routing.EndpointID{ep(1), ep(2)}) {
		t.Errorf("route %v, want the two endpoints once each", got)
	}
}

// TestSnapshotCopiesWhatTheCallerKeeps: changing the slices the snapshot was built from does not change the snapshot.
func TestSnapshotCopiesWhatTheCallerKeeps(t *testing.T) {
	t.Parallel()
	order := []string{"g2", "g1"}
	preset := openPreset()
	preset.OrderNames = order
	h := newHarness(t, routing.Config{}, preset, endpoint(t, 1), endpoint(t, 2))
	order[0], order[1] = "g1", "g2" // the caller reuses its slice
	if p := h.plan(input()); first(p) != ep(2) {
		t.Errorf("the snapshot followed the caller's slice: first %v", p.Entries)
	}
}
