package quality

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

func TestFailedRecheckTakesAnEndpointOffline(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	removeEndpoint(t, m, id, *now)
	e := m.Live(id, 1)
	if e.Offline(*now) {
		t.Fatal("a trip alone took the endpoint offline")
	}
	*now = now.Add(30 * time.Second)
	tick(t, l)
	f.mu.Lock()
	f.result = suppliers.TestResult{Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}}
	f.mu.Unlock()
	finishProbe(t, l, f)
	if !e.Offline(*now) {
		t.Fatal("a failed re-check did not take the endpoint offline")
	}
	m.Observe(t.Context(), Observation{Endpoint: id, Source: uuid.NewV7(), Sent: *now, Ended: *now, Done: true})
	if at, _ := e.Routing(*now); at.IsZero() {
		t.Fatal("a real success restored an offline endpoint")
	}
	*now = now.Add(time.Minute)
	tick(t, l)
	f.mu.Lock()
	f.result = suppliers.TestResult{Streamed: true}
	f.mu.Unlock()
	finishProbe(t, l, f)
	if e.Offline(*now) {
		t.Fatal("a passing probe left the endpoint offline")
	}
	if at, _ := e.Routing(*now); !at.IsZero() {
		t.Fatal("a passing probe did not restore the endpoint")
	}
}

func TestUnprobeableEndpointsStayDegraded(t *testing.T) {
	for name, ep := range map[string]suppliers.Routable{
		"owned":  {Site: uuid.NewV7(), Credentials: 1, Formats: []protocol.Format{protocol.Chat}},
		"direct": {Credentials: 1, Formats: []protocol.Format{protocol.Embeddings}},
	} {
		t.Run(name, func(t *testing.T) {
			checkUnprobeable(t, ep)
		})
	}
}

func checkUnprobeable(t *testing.T, ep suppliers.Routable) {
	t.Helper()
	m, l, f, now := fixtureLoop(t, Enforce)
	id := uuid.NewV7()
	ep.ID = id
	f.endpoints = append(f.endpoints, ep)
	if err := m.Prepare(t.Context(), f.endpoints); err != nil {
		t.Fatal(err)
	}
	removeEndpoint(t, m, id, *now)
	e := m.Live(id, 1)
	if at, _ := e.Routing(*now); at.IsZero() {
		t.Fatal("an unprobeable endpoint did not trip")
	}
	if e.Offline(*now) {
		t.Fatal("an endpoint without a probe went offline without a re-check")
	}
	*now = now.Add(30 * time.Second)
	tick(t, l)
	if l.active != 0 {
		t.Fatal("probed an endpoint that cannot answer a generation")
	}
	m.Observe(t.Context(), Observation{Endpoint: id, Source: uuid.NewV7(), Sent: *now, Ended: *now, Done: true})
	if at, _ := e.Routing(*now); !at.IsZero() {
		t.Fatal("a real success did not restore a degraded endpoint without a probe")
	}
}

func TestLateSuccessDoesNotRestoreUnprobeable(t *testing.T) {
	for name, ep := range map[string]suppliers.Routable{
		"owned":  {Site: uuid.NewV7(), Credentials: 1, Formats: []protocol.Format{protocol.Chat}},
		"direct": {Credentials: 1, Formats: []protocol.Format{protocol.Embeddings}},
	} {
		t.Run(name, func(t *testing.T) {
			checkLateSuccess(t, ep)
		})
	}
}

func checkLateSuccess(t *testing.T, ep suppliers.Routable) {
	t.Helper()
	m, _, f, now := fixtureLoop(t, Enforce)
	id := uuid.NewV7()
	ep.ID = id
	f.endpoints = append(f.endpoints, ep)
	if err := m.Prepare(t.Context(), f.endpoints); err != nil {
		t.Fatal(err)
	}
	removeEndpoint(t, m, id, *now)
	e := m.Live(id, 1)
	m.Observe(t.Context(), Observation{Endpoint: id, Source: uuid.NewV7(), Sent: now.Add(-2 * time.Second), Ended: now.Add(time.Second), Done: true})
	if at, _ := e.Routing(*now); at.IsZero() {
		t.Fatal("a success sent before the trip restored the endpoint")
	}
	m.Observe(t.Context(), Observation{Endpoint: id, Source: uuid.NewV7(), Sent: now.Add(2 * time.Second), Ended: now.Add(3 * time.Second), Done: true})
	if at, _ := e.Routing(*now); !at.IsZero() {
		t.Fatal("a success sent after the trip did not restore the endpoint")
	}
}

func TestProbeUsesAGenerationFormat(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := uuid.NewV7()
	f.endpoints = append(f.endpoints, suppliers.Routable{ID: id, Credentials: 1, Formats: []protocol.Format{protocol.Embeddings, protocol.Chat}})
	if err := m.Prepare(t.Context(), f.endpoints); err != nil {
		t.Fatal(err)
	}
	removeEndpoint(t, m, id, *now)
	*now = now.Add(30 * time.Second)
	tick(t, l)
	select {
	case request := <-f.calls:
		if request.Format != protocol.Chat {
			t.Fatalf("probed with format %d, want chat", request.Format)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no probe was sent")
	}
	finishProbe(t, l, f)
}
