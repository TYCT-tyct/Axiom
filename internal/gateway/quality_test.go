package gateway

import (
	"testing"
	"time"

	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/suppliers"
)

// TestRoutePublishesMeasuredQuality: the snapshot takes the score and its confidence from the monitor that measured
// them. An endpoint nothing counted keeps the neutral quality 1 and the confidence 0; a measured bad endpoint has a low
// score and a nonzero confidence, so routing can tell the unknown from the bad.
func TestRoutePublishesMeasuredQuality(t *testing.T) {
	f := newRouteFixture(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	snap, err := f.cat.Current()
	if err != nil {
		t.Fatal(err)
	}
	unknown := routable(f.served, protocol.Messages, protocol.Responses)
	u, why := endpointOf(snap, f.prices, nil, unknown, f.health.Measured(unknown.ID, now))
	if why != "" {
		t.Fatalf("unknown endpoint: %s", why)
	}
	if u.Quality != 1 || u.Confidence != 0 {
		t.Errorf("unknown endpoint: quality %v confidence %v, want 1 and 0", u.Quality, u.Confidence)
	}
	for range 10 {
		f.health.Observe(t.Context(), quality.Observation{
			Endpoint: unknown.ID, Done: true, InputTokens: 100, OutputTokens: 100,
			Sent: now, FirstToken: now.Add(500 * time.Millisecond), Ended: now.Add(1500 * time.Millisecond),
		})
	}
	good, why := endpointOf(snap, f.prices, nil, unknown, f.health.Measured(unknown.ID, now))
	if why != "" {
		t.Fatalf("measured endpoint: %s", why)
	}
	if good.Quality != 1 || good.Confidence != 1 || good.Health != 1 {
		t.Errorf("measured endpoint: quality %v confidence %v health %v, want all 1", good.Quality, good.Confidence, good.Health)
	}
	bad := routable(f.served, protocol.Messages, protocol.Responses)
	for range 5 {
		f.health.Observe(t.Context(), quality.Observation{
			Endpoint: bad.ID, Ended: now,
			Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{Status: 500}},
		})
	}
	b, why := endpointOf(snap, f.prices, nil, bad, f.health.Measured(bad.ID, now))
	if why != "" {
		t.Fatalf("bad endpoint: %s", why)
	}
	if !(u.Quality > b.Quality && u.Confidence == 0 && b.Confidence > 0) {
		t.Errorf("unknown (%v, %v) and bad (%v, %v) are not distinct", u.Quality, u.Confidence, b.Quality, b.Confidence)
	}
}
