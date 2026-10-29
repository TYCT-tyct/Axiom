package quality

import (
	"math"
	"sync"
	"testing"
	"time"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

var failed500 = &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{Status: 500}}

// answered is an attempt that ended normally: sent at sent, its first token after ttft, then output tokens over gen.
func answered(id suppliers.EndpointID, sent time.Time, ttft, gen time.Duration, input, output int64) Observation {
	return Observation{
		Endpoint: id, Done: true, InputTokens: input, OutputTokens: output,
		Sent: sent, FirstToken: sent.Add(ttft), Ended: sent.Add(ttft + gen),
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestHealth: health is 1 minus the moving average of failures with α 0.2, 1 before anything is counted, as sub2api's
// (openai_account_scheduler.go, report).
func TestHealth(t *testing.T) {
	m := New(nil)
	id := suppliers.EndpointID{1}
	if got := m.Measured(id, t0); got != (Measures{Health: 1}) {
		t.Fatalf("never observed: %+v", got)
	}
	want := 1.0
	for i, failed := range []bool{true, true, false, true, false, false} {
		o := answered(id, t0, time.Second, time.Second, 10, 10)
		if failed {
			o = Observation{Endpoint: id, Failure: failed500, Ended: t0}
		}
		m.Observe(t.Context(), o)
		errRate := 1 - want
		if failed {
			want = 1 - (errRate + 0.2*(1-errRate))
		} else {
			want = 1 - 0.8*errRate
		}
		if got := m.Measured(id, t0).Health; !near(got, want) {
			t.Fatalf("after %d outcomes health = %v, want %v", i+1, got, want)
		}
	}
}

// TestMeasured: time to first token is sampled only for a short input, speed only for an answer that ended normally
// with at least 50 tokens; each is a moving average whose first sample seeds it, and counts as measured from the fifth
// sample until 30 minutes pass without one.
func TestMeasured(t *testing.T) {
	m := New(nil)
	id := suppliers.EndpointID{2}
	for i := range 4 {
		m.Observe(t.Context(), answered(id, t0, 2*time.Second, 2*time.Second, latencyMaxInput, 100)) // 2s, 50 tokens a second
		if got := m.Measured(id, t0); got.Latency != 0 || got.Throughput != 0 {
			t.Fatalf("after %d samples: %+v, want unmeasured", i+1, got)
		}
	}
	m.Observe(t.Context(), answered(id, t0, 7*time.Second, time.Second, latencyMaxInput+1, speedMinOutput-1)) // neither is sampled
	m.Observe(t.Context(), answered(id, t0, 7*time.Second, time.Second, latencyMaxInput, 100))                // 7s, 100 a second
	got := m.Measured(id, t0.Add(9*time.Second))
	if got.Latency != 3*time.Second || !near(got.Throughput, 60) { // 2 + 0.2·(7−2); 50 + 0.2·(100−50)
		t.Fatalf("measured %+v, want 3s and 60 tokens a second", got)
	}
	if stale := m.Measured(id, t0.Add(8*time.Second+maxAge+time.Nanosecond)); stale.Latency != 0 || stale.Throughput != 0 {
		t.Fatalf("30 minutes after the last sample: %+v, want unmeasured", stale)
	}
	m.Observe(t.Context(), answered(id, t0, 7*time.Second, time.Second, 0, 0)) // a failure-free short answer: latency only
	broke := answered(id, t0, 7*time.Second, time.Second, 0, 1000)
	broke.Done, broke.Failure = false, nil
	broke.Fault = protocol.FaultBroken // a failure, so its speed is no sample
	m.Observe(t.Context(), broke)
	if got := m.Measured(id, t0.Add(9*time.Second)); !near(got.Throughput, 60) {
		t.Fatalf("an answer that did not end normally changed the speed: %+v", got)
	}
}

// TestObserveConcurrently: the gateway's attempts end on many goroutines while the snapshot is built from another;
// the race detector checks the locks, and every outcome counts.
func TestObserveConcurrently(t *testing.T) {
	m := New(nil)
	ids := [2]suppliers.EndpointID{{1}, {2}}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for range 100 {
				m.Observe(t.Context(), Observation{Endpoint: ids[g%2], Failure: failed500, Ended: t0})
				_ = m.Measured(ids[g%2], t0)
			}
		})
	}
	wg.Wait()
	if got := m.Measured(ids[0], t0).Health; got > 1e-9 {
		t.Fatalf("health after 400 failures = %v", got)
	}
}

// BenchmarkObserve: Observe is on every attempt's way out and allocates nothing once the endpoint has been seen, for a
// success that samples both measures and for a failure the table counts.
func BenchmarkObserve(b *testing.B) {
	m := New(nil)
	id := suppliers.EndpointID{3}
	ok := answered(id, t0, time.Second, time.Second, 100, 100)
	bad := Observation{Endpoint: id, Failure: failed500, Ended: t0}
	ok.Source, ok.Credential = id, id
	bad.Source, bad.Credential = id, id
	observe := func() {
		m.Observe(b.Context(), ok)
		m.Observe(b.Context(), bad)
	}
	observe() // the endpoint's entry is made once
	if allocs := testing.AllocsPerRun(100, observe); allocs != 0 {
		b.Fatalf("Observe allocates %v times per call, want 0", allocs)
	}
	b.ReportAllocs()
	for b.Loop() {
		observe()
	}
}
