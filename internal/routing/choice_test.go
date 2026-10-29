package routing_test

import (
	"math"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"

	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// priced is an endpoint whose input and output cost in and out dollars per million tokens, so the blend is in+out.
func priced(tb testing.TB, n int, in, out string) routing.Endpoint {
	return endpoint(tb, n, func(e *routing.Endpoint) { e.Price.Schedule = schedule(tb, in, out, "") })
}

// TestWeightedChoice: the endpoint a balanced plan tries first is drawn with probability proportional to 1/price, and
// the rest follow the same law among what remains (sampling without replacement).
func TestWeightedChoice(t *testing.T) {
	t.Parallel()
	// Blends 3e-6, 6e-6 and 12e-6: weights 4 : 2 : 1.
	h := newHarness(t, routing.Config{}, openPreset(),
		priced(t, 1, "0.000001", "0.000002"), priced(t, 2, "0.000002", "0.000004"), priced(t, 3, "0.000004", "0.000008"))
	const trials = 40000
	var firsts [4]int
	var afterFirst [4]int // how often endpoint 2 came second when endpoint 1 was first
	for range trials {
		p := h.plan(input())
		firsts[p.Entries[0].Endpoint[15]]++
		if p.Entries[0].Endpoint == ep(1) && p.Entries[1].Endpoint == ep(2) {
			afterFirst[2]++
		}
	}
	for n, want := range map[int]float64{1: 4.0 / 7, 2: 2.0 / 7, 3: 1.0 / 7} {
		if got := float64(firsts[n]) / trials; math.Abs(got-want) > 0.01 {
			t.Errorf("endpoint %d first %.3f of the time, want %.3f", n, got, want)
		}
	}
	if got := float64(afterFirst[2]) / float64(firsts[1]); math.Abs(got-2.0/3) > 0.02 {
		t.Errorf("endpoint 2 second after 1 first: %.3f, want %.3f", got, 2.0/3)
	}
}

// TestWeightFactors: a halved weight for a low supplier balance, and the health and quality scores, scale the chance.
func TestWeightFactors(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*routing.Endpoint){
		"low balance":  func(e *routing.Endpoint) { e.BalanceLow = true },
		"poor health":  func(e *routing.Endpoint) { e.Health = 0.5 },
		"poor quality": func(e *routing.Endpoint) { e.Quality = 0.5 },
	}
	for name, mod := range cases {
		h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1, mod), endpoint(t, 2))
		first1 := 0
		for range 20000 {
			if first(h.plan(input())) == ep(1) {
				first1++
			}
		}
		if got := float64(first1) / 20000; math.Abs(got-1.0/3) > 0.015 {
			t.Errorf("%s: %.3f of the time first, want 1/3", name, got)
		}
	}
}

// TestThrottle: a 429 cuts the endpoint's weight at once and the weight returns over the recovery time.
func TestThrottle(t *testing.T) {
	t.Parallel()
	h := newHarness(t, routing.Config{Recovery: time.Minute, ThrottleFloor: 0.1}, openPreset(), endpoint(t, 1), endpoint(t, 2))
	share := func() float64 {
		n := 0
		for range 20000 {
			if first(h.plan(input())) == ep(1) {
				n++
			}
		}
		return float64(n) / 20000
	}
	if got := share(); math.Abs(got-0.5) > 0.015 {
		t.Fatalf("before: %.3f", got)
	}
	h.r.Throttled(ep(1))
	if got := share(); math.Abs(got-0.1/1.1) > 0.012 {
		t.Errorf("right after a 429: %.3f, want %.3f", got, 0.1/1.1)
	}
	h.now = h.now.Add(30 * time.Second)                     // halfway: the factor is 0.55
	h.replace(openPreset(), endpoint(t, 1), endpoint(t, 2)) // a fresh snapshot, since the old one is stale by now; the state lives on
	if got := share(); math.Abs(got-0.55/1.55) > 0.015 {
		t.Errorf("halfway: %.3f, want %.3f", got, 0.55/1.55)
	}
	h.now = h.now.Add(30 * time.Second)
	h.replace(openPreset(), endpoint(t, 1), endpoint(t, 2))
	if got := share(); math.Abs(got-0.5) > 0.015 {
		t.Errorf("recovered: %.3f", got)
	}
}

// TestToolCallsReorder: with tool calls in the request, balanced and quality-first rank by the measured tool error
// rate; the cheapest and the fastest do not, since reordering them would break the session's stickiness.
func TestToolCallsReorder(t *testing.T) {
	t.Parallel()
	flaky := func(e *routing.Endpoint) { e.ToolErrorRate = 0.9 }
	for strategy, wantFlakyFirst := range map[routing.Strategy]bool{routing.BestQuality: false, routing.Cheapest: true, routing.Fastest: true} {
		preset := openPreset()
		preset.Strategy = strategy
		// The flaky endpoint is the cheapest, the quickest and, but for the tool errors, as good as the other.
		h := newHarness(t, routing.Config{}, preset,
			endpoint(t, 1, flaky, func(e *routing.Endpoint) {
				e.Price.Schedule = schedule(t, "0.0000001", "0.0000002", "")
				e.Throughput = 500
			}), endpoint(t, 2))
		in := input()
		in.Req.Needs = protocol.Tools
		if got := first(h.plan(in)) == ep(1); got != wantFlakyFirst {
			t.Errorf("strategy %d: the flaky endpoint first = %v, want %v", strategy, got, wantFlakyFirst)
		}
	}
	h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1, flaky), endpoint(t, 2))
	in := input()
	in.Req.Needs = protocol.Tools
	n := 0
	for range 10000 {
		if first(h.plan(in)) == ep(1) {
			n++
		}
	}
	if got := float64(n) / 10000; got > 0.02 {
		t.Errorf("balanced with tools: the flaky endpoint is first %.3f of the time", got)
	}
}

// TestSticky: the session's endpoint is first whenever sticking pays and it is healthy; otherwise the strategy decides.
func TestSticky(t *testing.T) {
	t.Parallel()
	cheap := func(e *routing.Endpoint) { e.Price.Schedule = schedule(t, "0.0000001", "0.0000002", "") }
	cases := []struct {
		name   string
		mod    func(*routing.Endpoint)
		sticky bool // set Input.Sticky
		want   bool // the session's endpoint (2, the dearer) is first
		mid    func(h *harness)
	}{
		{"it pays and it is healthy", nil, true, true, nil},
		{"no session", nil, false, false, nil},
		{"the cache costs as much as input", func(e *routing.Endpoint) { e.Price.Schedule = schedule(t, "0.000001", "0.000002", "0.000001") }, true, false, nil},
		{"no cache price", func(e *routing.Endpoint) { e.Price.Schedule = schedule(t, "0.000001", "0.000002", "") }, true, false, nil},
		{"unhealthy", func(e *routing.Endpoint) { e.Health = 0.4 }, true, false, nil},
		{"throttled", nil, true, false, func(h *harness) { h.r.Throttled(ep(2)) }},
		{"breaker open", func(e *routing.Endpoint) { e.BreakerOpen = true }, true, false, nil},
		{"full", func(e *routing.Endpoint) { e.MaxConcurrent = 1 }, true, false, func(h *harness) { h.r.Begin(ep(2)) }},
	}
	for _, c := range cases {
		preset := openPreset()
		preset.Strategy = routing.Cheapest
		h := newHarness(t, routing.Config{}, preset, endpoint(t, 1, cheap), endpoint(t, 2, c.mod))
		if c.mid != nil {
			c.mid(h)
		}
		in := input()
		if c.sticky {
			in.Sticky = ep(2)
		}
		if got := first(h.plan(in)) == ep(2); got != c.want {
			t.Errorf("%s: the session's endpoint first = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestStickyIsFirstWhateverTheWeights is the property: for any seed and any weights, an eligible session endpoint heads
// the route, and a named order still comes before it.
func TestStickyIsFirstWhateverTheWeights(t *testing.T) {
	t.Parallel()
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 7)) //nolint:gosec // a seeded generator keeps the tests repeatable
		var endpoints []routing.Endpoint
		for n := 1; n <= 5; n++ {
			in, out := rng.IntN(9)+1, rng.IntN(9)+1
			endpoints = append(endpoints, endpoint(t, n, func(e *routing.Endpoint) {
				e.Price.Schedule = schedule(t, "0.00000"+strconv.Itoa(in), "0.00000"+strconv.Itoa(out), "0.0000001")
				e.Quality = 0.1 + rng.Float64()*0.9
			}))
		}
		draw := rand.New(rand.NewPCG(seed, 1)) //nolint:gosec // a seeded generator keeps the tests repeatable
		h := newHarness(t, routing.Config{Rand: draw.Float64}, openPreset(), endpoints...)
		in := input()
		in.Sticky = ep(rng.IntN(5) + 1)
		if got := first(h.plan(in)); got != in.Sticky {
			t.Fatalf("seed %d: first %v, the session's %v", seed, got, in.Sticky)
		}
		in.Override.OrderNames = []string{"g1"}
		in.Sticky = ep(3)
		if got := first(h.plan(in)); got != ep(1) {
			t.Fatalf("seed %d: with an order naming supplier 1, first is %v", seed, got)
		}
	}
}

// TestStickyStore: the session's endpoint is remembered for the time given, forgotten on request, and the store stays
// bounded.
func TestStickyStore(t *testing.T) {
	t.Parallel()
	h := newHarness(t, routing.Config{StickyTTL: time.Hour, StickyMax: 32}, openPreset(), endpoint(t, 1))
	k := h.r.SessionKey(acct, input().Req, []byte(`{"session_id":"s"}`))
	if _, ok := h.r.Stuck(k); ok {
		t.Fatal("stuck before anything was stored")
	}
	h.r.Stick(k, ep(1), time.Minute)
	if got, ok := h.r.Stuck(k); !ok || got != ep(1) {
		t.Errorf("stuck %v %v", got, ok)
	}
	h.now = h.now.Add(time.Minute)
	if _, ok := h.r.Stuck(k); ok {
		t.Error("still stuck after the time")
	}
	h.r.Stick(k, ep(1), 0) // the router's default hour
	h.now = h.now.Add(59 * time.Minute)
	if _, ok := h.r.Stuck(k); !ok {
		t.Error("the default time was not used")
	}
	h.r.Forget(k)
	if _, ok := h.r.Stuck(k); ok {
		t.Error("stuck after Forget")
	}
	for i := range 1000 {
		h.r.Stick(h.r.SessionKey(acct, input().Req, []byte(`{"session_id":"`+strconv.Itoa(i)+`"}`)), ep(1), time.Hour)
	}
	if n := h.r.StickyLen(); n > 48 { // 16 shards of at most 32/16+1
		t.Errorf("%d sessions kept, for a limit of 32", n)
	}
}
