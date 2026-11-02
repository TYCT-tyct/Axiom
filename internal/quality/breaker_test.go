package quality

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"
	"uuid"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

func TestIndependentFailures(t *testing.T) {
	for _, tc := range []struct {
		name                                      string
		sources, credentials, available, failures int
		auxiliary                                 bool
		want                                      bool
	}{
		{"independent", 2, 2, 2, 5, false, true},
		{"one bad key", 2, 1, 2, 32, false, false},
		{"one owner ordinary threshold", 1, 2, 2, 5, false, false},
		{"one owner conservative threshold", 1, 2, 2, 10, false, true},
		{"timeouts alone", 2, 2, 2, 32, true, false},
		{"single credential endpoint", 2, 1, 1, 5, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(nil)
			m.mode = Enforce
			id := uuid.NewV7()
			s := m.Live(id, tc.available)
			now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			for i := 0; i < tc.failures; i++ {
				o := Observation{Endpoint: id, Source: uuid.UUID{byte((i%tc.sources + 1) & 255)}, Credential: uuid.UUID{byte((i%tc.credentials + 1) & 255)}, Sent: now, Ended: now.Add(time.Second), Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}}
				if tc.auxiliary {
					o.Failure.Cause = suppliers.Transport{}
					o.Cut = HeaderWait
				}
				m.Observe(context.Background(), o)
			}
			open, _ := s.Routing(now)
			if !open.IsZero() != tc.want {
				t.Fatalf("open=%v, want %v", open, tc.want)
			}
		})
	}
}

func TestWindowAndLateSuccess(t *testing.T) {
	m := New(nil)
	m.mode = Enforce
	id := uuid.NewV7()
	s := m.Live(id, 1)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fail := func(i int) {
		m.Observe(context.Background(), Observation{Endpoint: id, Source: uuid.UUID{byte(i%2 + 1)}, Sent: now, Ended: now.Add(time.Second), Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}})
	}
	for i := range 4 {
		fail(i)
	}
	now = now.Add(11 * time.Minute)
	fail(0)
	if at, _ := s.Routing(now); !at.IsZero() {
		t.Fatal("old window survived idle gap")
	}
	for i := 1; i < 5; i++ {
		fail(i)
	}
	open, _ := s.Routing(now)
	// A normal attempt admitted just before the trip and finishing after it is not a re-check: only a probe restores.
	m.Observe(context.Background(), Observation{Endpoint: id, Done: true, Sent: now.Add(-time.Second), Ended: now.Add(2 * time.Second)})
	if at, _ := s.Routing(now); at.IsZero() {
		t.Fatal("a late success restored the breaker")
	}
	if at, _ := s.Routing(open.Add(time.Second)); at.IsZero() {
		t.Fatal("the breaker closed without a probe")
	}
}

func TestSlowStartRamp(t *testing.T) {
	m := New(nil)
	m.mode = Enforce
	id := uuid.NewV7()
	e := m.Live(id, 1)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	e.mu.Lock()
	e.record = healthRecord{ID: id, Mode: Enforce, Restored: now.UnixNano()}
	e.mu.Unlock()
	for _, tc := range []struct {
		after time.Duration
		limit int64
	}{{0, 1}, {6 * time.Second, 2}, {61 * time.Second, 0}} {
		if _, limit := e.Routing(now.Add(tc.after)); limit != tc.limit {
			t.Fatalf("limit after %v = %d, want %d", tc.after, limit, tc.limit)
		}
	}
}

func TestObserveModeAndHealthyTraffic(t *testing.T) {
	for _, mode := range []Mode{Observe, Enforce} {
		m := New(nil)
		m.mode = mode
		id := uuid.NewV7()
		s := m.Live(id, 1)
		now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		rng := rand.New(rand.NewPCG(42, 313)) //nolint:gosec // fixed statistical regression stream, not a secret
		for i := range 360000 {
			o := Observation{Endpoint: id, Source: uuid.UUID{byte(i%2 + 1)}, Sent: now, Ended: now.Add(time.Millisecond), Done: true}
			if rng.Float64() < .1 {
				o.Done = false
				o.Failure = &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}
			}
			m.Observe(context.Background(), o)
			now = now.Add(10 * time.Millisecond)
			if at, _ := s.Routing(now); !at.IsZero() {
				t.Fatalf("healthy stream tripped at %d", i)
			}
		}
	}
}

func TestFullWindowNeedsHalfFailures(t *testing.T) {
	m := New(nil)
	m.mode = Enforce
	id := uuid.NewV7()
	e := m.Live(id, 1)
	now := t0
	for i := range 48 {
		o := Observation{Endpoint: id, Source: uuid.UUID{byte(i%2 + 1)}, Sent: now, Ended: now.Add(time.Second), Done: i < 32}
		if !o.Done {
			o.Failure = failed500
		}
		m.Observe(t.Context(), o)
		now = now.Add(time.Second)
		at, _ := e.Routing(now)
		if !at.IsZero() != (i == 47) {
			t.Fatalf("open at outcome %d: %v", i+1, at)
		}
	}
}

func TestRecoveryIgnoresPriorGenerationFailures(t *testing.T) {
	m := New(nil)
	m.mode = Enforce
	id := uuid.NewV7()
	e := m.Live(id, 1)
	sent := t0
	for i := range 5 {
		m.Observe(t.Context(), Observation{Endpoint: id, Source: uuid.UUID{byte(i%2 + 1)}, Sent: sent, Ended: sent.Add(time.Second), Failure: failed500})
	}
	e.mu.Lock()
	e.record = healthRecord{ID: id, Mode: Enforce, Restored: sent.Add(3 * time.Second).UnixNano()}
	e.mu.Unlock()
	for i := range 10 {
		m.Observe(t.Context(), Observation{Endpoint: id, Source: uuid.UUID{byte(i%2 + 1)}, Sent: sent, Ended: sent.Add(4 * time.Second), Failure: failed500})
	}
	if at, _ := e.Routing(sent.Add(4 * time.Second)); !at.IsZero() {
		t.Fatal("prior generation failures reopened recovered endpoint")
	}
}

func TestRemovalCauses(t *testing.T) {
	for _, tc := range []struct {
		name string
		last Observation
	}{
		{"upstream_error", Observation{Failure: failed500}},
		{"empty_stream", Observation{Done: true, Empty: true}},
		{"broken_stream", Observation{Fault: protocol.FaultBroken}},
		{"timeout", Observation{Cut: Idle}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(nil)
			m.mode = Enforce
			id := uuid.NewV7()
			e := m.Live(id, 1)
			for i := range 5 {
				o := Observation{Failure: failed500}
				if i == 4 {
					o = tc.last
				}
				o.Endpoint = id
				o.Source = uuid.UUID{byte(i%2 + 1)}
				o.Sent = t0
				o.Ended = t0.Add(time.Second)
				m.Observe(t.Context(), o)
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.record.Opened == 0 || e.record.Cause != tc.name {
				t.Fatalf("removal: %+v", e.record)
			}
		})
	}
}

func TestFailuresNeedIndependentOwners(t *testing.T) {
	m := New(nil)
	m.mode = Enforce
	id := uuid.NewV7()
	e := m.Live(id, 1)
	for i := range 64 {
		o := Observation{Endpoint: id, Source: uuid.UUID{1}, Sent: t0, Ended: t0, Failure: failed500}
		if i%4 == 0 {
			o.Source = uuid.UUID{2}
			o.Failure = nil
			o.Done = true
		}
		m.Observe(t.Context(), o)
		if at, _ := e.Routing(t0); !at.IsZero() {
			t.Fatal("one owner's failures used another owner's successes as independent evidence")
		}
	}
}

func TestProbeBackoffReachesTheCapFromTheSmallestBase(t *testing.T) {
	m := New(nil)
	l := &Loop{rand: func() float64 { return .5 }}
	settings := DefaultProbeSettings()
	settings.ProbeBase = time.Second
	l.settings.Store(&settings)
	m.loop = l
	if got := m.delay(0); got != time.Second {
		t.Fatalf("first delay %v, want one second", got)
	}
	if got := m.delay(maxProbeBackoff); got != probeMax {
		t.Fatalf("delay at level %d = %v, want the %v cap", maxProbeBackoff, got, probeMax)
	}
	previous := time.Duration(0)
	for level := range maxProbeBackoff + 3 {
		d := m.delay(level)
		if d < previous || d > probeMax {
			t.Fatalf("level %d delay %v after %v", level, d, previous)
		}
		previous = d
	}
}

func TestProbeBackoffIsBoundedWithJitter(t *testing.T) {
	for _, u := range []float64{0, .5, .999999} {
		m := New(nil)
		m.loop = &Loop{rand: func() float64 { return u }}
		previous := time.Duration(0)
		for level := range 20 {
			d := m.delay(level)
			if d < defaultProbeBase || d > probeMax || d < previous {
				t.Fatalf("jitter=%v level=%d delay=%v", u, level, d)
			}
			previous = d
		}
	}
}

func TestMissingOwnerDoesNotSupplyBreakerEvidence(t *testing.T) {
	m := New(nil)
	m.mode = Enforce
	id := uuid.NewV7()
	e := m.Live(id, 1)
	for range 32 {
		m.Observe(t.Context(), Observation{Endpoint: id, Sent: t0, Ended: t0, Failure: failed500})
	}
	if at, _ := e.Routing(t0); !at.IsZero() {
		t.Fatal("missing source was treated as independent failure evidence")
	}
	if measured(m, id, t0).Health > .1 {
		t.Fatal("missing source stopped health measurements")
	}
}

func TestNormalSuccessDoesNotRestoreTheBreaker(t *testing.T) {
	m := New(nil)
	m.mode = Enforce
	id := uuid.NewV7()
	e := m.Live(id, 1)
	for i := range 5 {
		m.Observe(t.Context(), Observation{Endpoint: id, Source: uuid.UUID{byte(i%2 + 1)}, Sent: t0, Ended: t0, Failure: failed500})
	}
	// An attempt admitted before the trip can pass Blocked just before another attempt trips the breaker.
	m.Observe(t.Context(), Observation{Endpoint: id, Sent: t0.Add(time.Second), Ended: t0.Add(2 * time.Second), Done: true})
	if at, _ := e.Routing(t0.Add(2 * time.Second)); at.IsZero() {
		t.Fatal("a stale normal route restored the breaker")
	}
}

func TestBackoffResetsOnlyAfterStableRecovery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		successes int
		elapsed   time.Duration
	}{{"ten successes", 10, time.Second}, {"ten minutes", 1, 10 * time.Minute}} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(nil)
			m.mode = Enforce
			id := uuid.NewV7()
			e := m.Live(id, 1)
			e.record = healthRecord{ID: id, Mode: Enforce, Restored: t0.UnixNano(), Backoff: 3}
			for i := range tc.successes {
				at := t0.Add(time.Duration(i+1) * tc.elapsed)
				m.Observe(t.Context(), Observation{Endpoint: id, Source: uuid.UUID{1}, Sent: at, Ended: at, Done: true})
				want := 3
				if i == tc.successes-1 {
					want = 0
				}
				e.mu.Lock()
				got := e.record.Backoff
				e.mu.Unlock()
				if got != want {
					t.Fatalf("success %d: backoff=%d want=%d", i+1, got, want)
				}
			}
		})
	}
}
