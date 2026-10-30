package routing_test

import (
	"context"
	"slices"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

type unusedProbe struct{}

func (unusedProbe) Routable() ([]suppliers.Routable, error) { return nil, nil }
func (unusedProbe) TestEndpoint(context.Context, suppliers.TestRequest) (suppliers.TestResult, error) {
	panic("routing does not probe")
}

func routingHealth(t *testing.T, h *harness) *quality.Monitor {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t, quality.Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	m, _, err := quality.Start(t.Context(), quality.Config{DB: db, Scope: pg.PlatformScope(), Mode: quality.Enforce, Source: unusedProbe{}, Pauses: h.r, Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func trip(t *testing.T, m *quality.Monitor, n int, now time.Time) {
	t.Helper()
	for i := range 5 {
		m.Observe(t.Context(), quality.Observation{Endpoint: uuid.UUID(ep(n)), Source: uuid.UUID{byte(i%2 + 1)}, Sent: now.Add(-time.Second), Ended: now, Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}})
	}
}

func TestLiveHealthLastResortAndSlowStart(t *testing.T) {
	h := newHarness(t, routing.Config{MaxStale: time.Hour}, openPreset())
	m := routingHealth(t, h)
	a, b := endpoint(t, 1), endpoint(t, 2)
	a.LiveHealth = m.Live(uuid.UUID(a.ID), 1)
	b.LiveHealth = m.Live(uuid.UUID(b.ID), 1)
	h.replace(openPreset(), a, b)
	in := input()
	before := h.plan(in).Entries[0]
	trip(t, m, 1, h.now)
	plan := h.plan(in)
	if len(plan.Entries) != 1 || plan.Entries[0].Endpoint != b.ID {
		t.Fatalf("live removal: %+v", plan.Entries)
	}
	if before.Endpoint == a.ID && !h.r.Blocked(before) {
		t.Fatal("Blocked ignored live state")
	}
	h.now = h.now.Add(time.Second)
	trip(t, m, 2, h.now)
	plan = h.plan(in)
	if len(plan.Entries) != 2 || plan.Entries[0].Endpoint != b.ID || !plan.Entries[0].BreakerFallback || h.r.Blocked(plan.Entries[0]) {
		t.Fatalf("last resort: %+v", plan.Entries)
	}
	h.now = h.now.Add(time.Second)
	m.Observe(t.Context(), quality.Observation{Endpoint: uuid.UUID(a.ID), Done: true, BreakerFallback: true, Sent: h.now, Ended: h.now})
	m.Observe(t.Context(), quality.Observation{Endpoint: uuid.UUID(b.ID), Done: true, BreakerFallback: true, Sent: h.now, Ended: h.now})
	h.r.Begin(a.ID)
	plan = h.plan(in)
	if len(plan.Entries) != 1 || plan.Entries[0].Endpoint != b.ID {
		t.Fatalf("slow start: %+v", plan.Entries)
	}
	h.replace(openPreset(), a)
	if len(h.plan(in).Entries) != 1 {
		t.Fatal("slow start queued the only endpoint")
	}
	h.r.End(a.ID)
}

func TestOwnedFallbackDoesNotBypassOtherFilters(t *testing.T) {
	h := newHarness(t, routing.Config{MaxStale: time.Hour}, openPreset())
	m := routingHealth(t, h)
	a, b := endpoint(t, 1), endpoint(t, 2)
	a.LiveHealth = m.Live(uuid.UUID(a.ID), 1)
	a.Owner = routing.Owner{Account: acct}
	h.replace(openPreset(), a, b)
	trip(t, m, 1, h.now)
	if len(h.plan(input()).Entries) != 1 {
		t.Fatal("owned endpoint bypassed cooldown")
	}
	h.now = h.now.Add(10 * time.Minute)
	plan := h.plan(input())
	if len(plan.Entries) != 2 || plan.Entries[1].Endpoint != a.ID || !plan.Entries[1].BreakerFallback {
		t.Fatalf("owned last slot: %+v", plan.Entries)
	}
	a.Enabled = false
	h.replace(openPreset(), a, b)
	if len(h.plan(input()).Entries) != 1 {
		t.Fatal("disabled endpoint became last resort")
	}
}

func TestLastResortPreservesRequestPriorities(t *testing.T) {
	for _, tc := range []string{"model", "global model", "codename", "slow", "same tier"} {
		t.Run(tc, func(t *testing.T) {
			h := newHarness(t, routing.Config{MaxStale: time.Hour}, openPreset())
			m := routingHealth(t, h)
			a, b := endpoint(t, 1), endpoint(t, 2)
			a.LiveHealth = m.Live(uuid.UUID(a.ID), 1)
			b.LiveHealth = m.Live(uuid.UUID(b.ID), 1)
			in := input()
			want := a.ID
			switch tc {
			case "model":
				b.Model = other
				in.Models = append(in.Models, other)
			case "global model":
				b.Model = other
				in.Models = append(in.Models, other)
				in.Override.SortGlobal = true
				want = b.ID
			case "codename":
				a.Codename, b.Codename = "g1", "g2"
				in.Override.OrderNames = []string{"g1", "g2"}
			case "slow":
				b.Throughput = 1
				in.Override.MinThroughput = 50
			case "same tier":
				want = b.ID
			}
			h.replace(openPreset(), a, b)
			trip(t, m, 1, h.now)
			h.now = h.now.Add(time.Second)
			trip(t, m, 2, h.now)
			plan := h.plan(in)
			if len(plan.Entries) != 2 || plan.Entries[0].Endpoint != want || !plan.Entries[0].BreakerFallback {
				t.Fatalf("%s priority: %+v", tc, plan.Entries)
			}
		})
	}
}

// TestFastestUnmeasuredAfterTheBreakers: when the only measured endpoint's breaker is open and unmeasured ones remain,
// the route is ranked without any measurement, and the record says so.
func TestFastestUnmeasuredAfterTheBreakers(t *testing.T) {
	h := newHarness(t, routing.Config{MaxStale: time.Hour}, fastestPreset())
	m := routingHealth(t, h)
	a, b := endpoint(t, 1, speed(100, time.Second)), endpoint(t, 2, unmeasured)
	a.LiveHealth = m.Live(uuid.UUID(a.ID), 1)
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
