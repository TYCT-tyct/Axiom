package gateway

import (
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
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
	good := routable(f.served, protocol.Messages, protocol.Responses)
	u := published(t, f, snap, good, now)
	if u.Quality != 1 || u.Confidence != 0 {
		t.Errorf("unknown endpoint: quality %v confidence %v, want 1 and 0", u.Quality, u.Confidence)
	}
	observed(t, f, 10, quality.Observation{
		Endpoint: good.ID, Done: true, InputTokens: 100, OutputTokens: 100,
		Sent: now, FirstToken: now.Add(500 * time.Millisecond), Ended: now.Add(1500 * time.Millisecond),
	})
	if g := published(t, f, snap, good, now); g.Quality != 1 || g.Confidence != 1 || g.Health != 1 {
		t.Errorf("measured endpoint: quality %v confidence %v health %v, want all 1", g.Quality, g.Confidence, g.Health)
	}
	bad := routable(f.served, protocol.Messages, protocol.Responses)
	observed(t, f, 5, quality.Observation{
		Endpoint: bad.ID, Ended: now,
		Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{Status: 500}},
	})
	b := published(t, f, snap, bad, now)
	if !(u.Quality > b.Quality && u.Confidence == 0 && b.Confidence > 0) {
		t.Errorf("unknown (%v, %v) and bad (%v, %v) are not distinct", u.Quality, u.Confidence, b.Quality, b.Confidence)
	}
}

// published is what the snapshot says of r, or a fatal error why it cannot be an endpoint.
func published(t *testing.T, f *routeFixture, snap *catalog.Snapshot, r suppliers.Routable, now time.Time) routing.Endpoint {
	t.Helper()
	e, why := endpointOf(snap, f.prices, nil, r, f.health.Measured(r.ID, now))
	if why != "" {
		t.Fatalf("endpoint %s: %s", r.ID, why)
	}
	return e
}

// observed feeds the same attempt ending n times: the measurements need several samples to count.
func observed(t *testing.T, f *routeFixture, n int, o quality.Observation) {
	t.Helper()
	for range n {
		f.health.Observe(t.Context(), o)
	}
}
