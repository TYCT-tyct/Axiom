package routing_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// probeSource is quality's real probe path with a fixed answer: the test controls the clock and the answer, so no
// supplier is involved.
type probeSource struct {
	mu        sync.Mutex
	endpoints []suppliers.Routable
	result    suppliers.TestResult
	answered  chan struct{}
}

func (p *probeSource) Routable() ([]suppliers.Routable, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]suppliers.Routable(nil), p.endpoints...), nil
}

func (p *probeSource) TestEndpoint(context.Context, suppliers.TestRequest) (suppliers.TestResult, error) {
	p.mu.Lock()
	result := p.result
	p.mu.Unlock()
	p.answered <- struct{}{}
	return result, nil
}

// healthy starts the health loop on the given endpoints of the test's clock.
func healthy(t *testing.T, h *harness, endpoints ...routing.Endpoint) (*quality.Monitor, *quality.Loop, *probeSource) {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t, quality.Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	src := &probeSource{answered: make(chan struct{}, 8)}
	for _, e := range endpoints {
		src.endpoints = append(src.endpoints, suppliers.Routable{
			ID: suppliers.EndpointID(e.ID), Credentials: 1, Formats: []protocol.Format{protocol.Messages},
		})
	}
	m, l, err := quality.Start(t.Context(), quality.Config{
		DB: db, Scope: pg.PlatformScope(), Mode: quality.Enforce, Source: src, Pauses: h.r,
		Now: func() time.Time { return h.now }, Rand: func() float64 { return .5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, l, src
}

// probe runs one due probe with the given answer and waits until its result is persisted.
func probe(t *testing.T, l *quality.Loop, src *probeSource, result suppliers.TestResult) {
	t.Helper()
	src.mu.Lock()
	src.result = result
	src.mu.Unlock()
	if err := l.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-src.answered
	if err := l.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
}

var (
	probeFailed = suppliers.TestResult{Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}}
	probePassed = suppliers.TestResult{Streamed: true}
)

func trip(t *testing.T, m *quality.Monitor, n int, now time.Time) {
	t.Helper()
	for i := range 5 {
		m.Observe(t.Context(), quality.Observation{Endpoint: uuid.UUID(ep(n)), Source: uuid.UUID{byte(i%2 + 1)}, Sent: now.Add(-time.Second), Ended: now, Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}})
	}
}

// TestTrippedEndpointsTakeNoTraffic: a tripped endpoint leaves the route at once, even when it is the only candidate,
// and Plan says the request has nowhere to go instead of handing it to the broken endpoint.
func TestTrippedEndpointsTakeNoTraffic(t *testing.T) {
	h := newHarness(t, routing.Config{MaxStale: time.Hour}, openPreset())
	m, _, _ := healthy(t, h)
	a, b := endpoint(t, 1), endpoint(t, 2)
	a.LiveHealth = m.Live(uuid.UUID(a.ID), 1)
	b.LiveHealth = m.Live(uuid.UUID(b.ID), 1)
	h.replace(openPreset(), a, b)
	in := input()
	trip(t, m, 1, h.now)
	plan := h.plan(in)
	if len(plan.Entries) != 1 || plan.Entries[0].Endpoint != b.ID {
		t.Fatalf("after one trip: %+v", plan.Entries)
	}
	if h.r.Blocked(plan.Entries[0]) {
		t.Fatal("Blocked split a healthy entry")
	}
	h.now = h.now.Add(time.Second)
	trip(t, m, 2, h.now)
	p, wait, err := h.r.Plan(in)
	if wait != nil {
		t.Fatal("queued a request whose candidates are all tripped")
	}
	var none *routing.NoEndpoint
	if !errors.As(err, &none) || none.Permanent || len(p.Entries) != 0 {
		t.Fatalf("all tripped: plan %+v, wait %v, error %v; want a temporary NoEndpoint", p.Entries, wait, err)
	}
	if len(none.Reasons) != 2 || none.Reasons[0].Reason != routing.BreakerOpen || none.Reasons[1].Reason != routing.BreakerOpen {
		t.Fatalf("reasons %+v, want both breaker_open", none.Reasons)
	}
	// The entry planned before the trip is refused before it is sent.
	first := routing.Entry{Endpoint: a.ID, Supplier: a.Supplier, Model: a.Model}
	if !h.r.Blocked(first) {
		t.Fatal("Blocked let a tripped endpoint receive the attempt")
	}
}

// TestProbeRestoresAndSlowStart: only a passing probe brings a tripped endpoint back; its first requests go through a
// slow-start window, which is ignored when the recovering endpoint is the only candidate.
func TestProbeRestoresAndSlowStart(t *testing.T) {
	h := newHarness(t, routing.Config{MaxStale: time.Hour}, openPreset())
	a, b := endpoint(t, 1), endpoint(t, 2)
	m, l, src := healthy(t, h, a, b)
	a.LiveHealth = m.Live(uuid.UUID(a.ID), 1)
	b.LiveHealth = m.Live(uuid.UUID(b.ID), 1)
	h.replace(openPreset(), a, b)
	trip(t, m, 1, h.now)
	if len(h.plan(input()).Entries) != 1 {
		t.Fatal("a tripped endpoint stayed in the route")
	}
	h.now = h.now.Add(30 * time.Second)
	probe(t, l, src, probeFailed)
	if len(h.plan(input()).Entries) != 1 {
		t.Fatal("a failed re-check put the endpoint back")
	}
	h.now = h.now.Add(time.Minute)
	probe(t, l, src, probePassed)
	if got := endpoints(h.plan(input())); len(got) != 2 || !slices.Contains(got, a.ID) || !slices.Contains(got, b.ID) {
		t.Fatalf("route after recovery %v, want both endpoints", got)
	}
	h.r.Begin(a.ID)
	if got := endpoints(h.plan(input())); !slices.Contains(got, b.ID) || slices.Contains(got, a.ID) {
		t.Fatalf("slow start let %v through, want only the healthy endpoint", got)
	}
	h.replace(openPreset(), a)
	if got := endpoints(h.plan(input())); !slices.Equal(got, []routing.EndpointID{a.ID}) {
		t.Fatalf("single endpoint queued behind its own slow start: %v", got)
	}
	h.r.End(a.ID)
}

// TestFastestUnmeasuredAfterTheBreakers: when the only measured endpoint's breaker is open and unmeasured ones remain,
// the route is ranked without any measurement, and the record says so.
func TestFastestUnmeasuredAfterTheBreakers(t *testing.T) {
	h := newHarness(t, routing.Config{MaxStale: time.Hour}, fastestPreset())
	a, b := endpoint(t, 1, speed(100, time.Second)), endpoint(t, 2, unmeasured)
	m, _, _ := healthy(t, h, a, b)
	a.LiveHealth = m.Live(uuid.UUID(a.ID), 1)
	b.LiveHealth = m.Live(uuid.UUID(b.ID), 1)
	h.replace(fastestPreset(), a, b)
	if h.plan(input()).Record.Unmeasured {
		t.Fatal("marked unmeasured while a measured endpoint is in the route")
	}
	trip(t, m, 1, h.now)
	p := h.plan(input())
	if got := endpoints(p); !slices.Equal(got, []routing.EndpointID{ep(2)}) {
		t.Fatalf("route %v, want only the unmeasured endpoint", got)
	}
	if !p.Record.Unmeasured {
		t.Error("the route left with only unmeasured endpoints is not marked")
	}
}
