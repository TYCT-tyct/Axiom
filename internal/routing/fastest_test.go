package routing_test

import (
	"math"
	"slices"
	"testing"
	"time"

	"axiom/internal/routing"
)

func fastestPreset() routing.Preset {
	p := openPreset()
	p.Strategy = routing.Fastest
	return p
}

func speed(throughput float64, latency time.Duration) func(*routing.Endpoint) {
	return func(e *routing.Endpoint) { e.Throughput, e.Latency = throughput, latency }
}

var unmeasured = speed(0, 0)

// TestFastestRanksByMeasurement: the measured endpoints go by throughput, then by time to first token, whatever their
// IDs; the unmeasured ones go after them.
func TestFastestRanksByMeasurement(t *testing.T) {
	t.Parallel()
	never := func() float64 { return 0.5 } // no request explores
	h := newHarness(t, routing.Config{Rand: never}, fastestPreset(),
		endpoint(t, 1, unmeasured),
		endpoint(t, 2, speed(50, 100*time.Millisecond)),
		endpoint(t, 3, speed(200, 900*time.Millisecond)),
		endpoint(t, 4, speed(200, 300*time.Millisecond)))
	p := h.plan(input())
	if got, want := endpoints(p), []routing.EndpointID{ep(4), ep(3), ep(2)}; !slices.Equal(got, want) {
		t.Errorf("route %v, want %v", got, want)
	}
	h.replace(fastestPreset(), endpoint(t, 1, unmeasured), endpoint(t, 2, speed(50, 0)))
	if got := endpoints(h.plan(input())); !slices.Equal(got, []routing.EndpointID{ep(2), ep(1)}) {
		t.Errorf("route %v: the unmeasured endpoint is not after the measured one", got)
	}
}

// TestFastestUnmeasuredByPrice: unmeasured endpoints are ordered among themselves as Balanced orders them, by a
// price-weighted draw, never by their IDs: of three, one at half the others' price comes first half of the time, the
// other two a quarter each.
func TestFastestUnmeasuredByPrice(t *testing.T) {
	t.Parallel()
	half := func(e *routing.Endpoint) { e.Price.Schedule = schedule(t, "0.0000005", "0.000001", "0.00000005") }
	h := newHarness(t, routing.Config{}, fastestPreset(),
		endpoint(t, 1, unmeasured), endpoint(t, 2, unmeasured), endpoint(t, 3, unmeasured, half))
	const n = 20000
	firsts := map[routing.EndpointID]int{}
	for range n {
		firsts[first(h.plan(input()))]++
	}
	for e, want := range map[routing.EndpointID]float64{ep(1): 0.25, ep(2): 0.25, ep(3): 0.5} {
		if got := float64(firsts[e]) / n; math.Abs(got-want) > 0.02 {
			t.Errorf("endpoint %v first %.3f of the time, want %.2f", e, got, want)
		}
	}
}

// TestFastestExplores: a request whose draw is below 5% puts an unmeasured endpoint first, ahead of a faster measured
// one, so that it gets measured; a draw of 5% or more does not.
func TestFastestExplores(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		draw      float64
		wantFirst routing.EndpointID
	}{{0.049, ep(2)}, {0.05, ep(1)}} {
		h := newHarness(t, routing.Config{Rand: func() float64 { return c.draw }}, fastestPreset(),
			endpoint(t, 1, speed(100, time.Second)), endpoint(t, 2, unmeasured))
		if got := first(h.plan(input())); got != c.wantFirst {
			t.Errorf("draw %v: first %v, want %v", c.draw, got, c.wantFirst)
		}
	}
	// The order and the session still come first: exploring only reorders within the strategy.
	h := newHarness(t, routing.Config{Rand: func() float64 { return 0 }}, fastestPreset(),
		endpoint(t, 1, speed(100, time.Second)), endpoint(t, 2, unmeasured))
	in := input()
	in.Sticky = ep(1)
	if got := first(h.plan(in)); got != ep(1) {
		t.Errorf("the session's endpoint lost to exploring: first %v", got)
	}
}

func endpoints(p routing.Plan) []routing.EndpointID {
	out := make([]routing.EndpointID, len(p.Entries))
	for i, e := range p.Entries {
		out[i] = e.Endpoint
	}
	return out
}
