package routing_test

import (
	"math"
	"slices"
	"testing"

	"axiom/internal/routing"
)

func bestQualityPreset() routing.Preset {
	p := openPreset()
	p.Strategy = routing.BestQuality
	return p
}

// scored sets the measured quality score and its confidence.
func scored(quality, confidence float64) func(*routing.Endpoint) {
	return func(e *routing.Endpoint) { e.Quality, e.Confidence = quality, confidence }
}

// TestBestQualityOrdersByTheScore: the quality-first strategy orders the measured endpoints by quality times confidence,
// so a well-evidenced score beats a thin one of the same quality. A never-measured endpoint (confidence 0) is an
// unknown, not a zero: it goes after the measured ones rather than in front of a measured bad one.
func TestBestQualityOrdersByTheScore(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		endpoints []routing.Endpoint
		want      []routing.EndpointID
	}{
		{
			"score and confidence",
			[]routing.Endpoint{endpoint(t, 1, scored(0.2, 1)), endpoint(t, 2, scored(0.9, 1)), endpoint(t, 3, scored(1, 0.5))},
			[]routing.EndpointID{ep(2), ep(3), ep(1)}, // 0.9, 0.5, 0.2
		},
		{
			"unknown after measured",
			[]routing.Endpoint{endpoint(t, 1, scored(0.2, 1)), endpoint(t, 2, scored(1, 0)), endpoint(t, 3, scored(0.9, 1))},
			[]routing.EndpointID{ep(3), ep(1), ep(2)},
		},
	} {
		h := newHarness(t, routing.Config{}, bestQualityPreset(), c.endpoints...)
		if got := endpoints(h.plan(input())); !slices.Equal(got, c.want) {
			t.Errorf("%s: route %v, want %v", c.name, got, c.want)
		}
	}
}

// TestBestQualityUnknownByPrice: among never-measured endpoints the order is balanced's price-weighted draw, not their
// IDs, so an unknown endpoint is treated as neutral rather than as a score of zero that ties them all.
func TestBestQualityUnknownByPrice(t *testing.T) {
	t.Parallel()
	half := func(e *routing.Endpoint) { e.Price.Schedule = schedule(t, "0.0000005", "0.000001", "0.00000005") }
	h := newHarness(t, routing.Config{}, bestQualityPreset(),
		endpoint(t, 1, scored(1, 0)), endpoint(t, 2, scored(1, 0)), endpoint(t, 3, scored(1, 0), half))
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
