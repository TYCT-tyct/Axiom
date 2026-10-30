package quality

import (
	"log/slog"
	"math"
	"sync"
	"testing"
	"time"
	"uuid"

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
	if got := m.Measured(id, t0); got != (Measures{Health: 1, Quality: 1}) {
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

// TestMeasuredVerification: routing sees a pass only while it is recent and uncontradicted, and still learns when the
// last one was after it ages out, so a never-checked endpoint differs from one that passed before.
func TestMeasuredVerification(t *testing.T) {
	m := New(nil)
	id := suppliers.EndpointID{7}
	at := t0.Add(time.Hour)
	e := m.Live(id, 1)
	e.mu.Lock()
	e.verify = verification{passed: true, checked: at}
	e.mu.Unlock()
	if got := m.Measured(id, at); !got.Verified || got.VerifyConfidence != 1 || !got.VerifiedAt.Equal(at) {
		t.Fatalf("a fresh pass: %+v", got)
	}
	if got := m.Measured(id, at.Add(verifyMaxAge+time.Second)); got.Verified || got.VerifyConfidence != 0 || !got.VerifiedAt.Equal(at) {
		t.Fatalf("an expired pass: %+v", got)
	}
	e.mu.Lock()
	e.verify.passed, e.verify.failures = false, 1
	e.mu.Unlock()
	if got := m.Measured(id, at); got.Verified || !got.VerifiedAt.Equal(at) {
		t.Fatalf("a contradicted pass: %+v", got)
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

// TestQualityScore: the score is reliability (1 minus the error moving average) times the mean of the measured
// performance factors, each normalized to the interactive references—a second to the first token and 50 tokens a
// second. An unmeasured factor is left out, never counted as a bad one; nothing counted gives the neutral score 1 and
// the confidence 0, which keeps an unknown endpoint apart from a measured bad one. The clock is the test's.
func TestQualityScore(t *testing.T) {
	fast := func(m *Monitor, id suppliers.EndpointID) {
		for range scoreSamples {
			m.Observe(t.Context(), answered(id, t0, 500*time.Millisecond, time.Second, 100, 100)) // 0.5s, 100 tokens a second
		}
	}
	slow := func(m *Monitor, id suppliers.EndpointID) {
		for range scoreSamples {
			m.Observe(t.Context(), answered(id, t0, 10*time.Second, 10*time.Second, 100, 100)) // 10s, 10 tokens a second
		}
	}
	noPerformance := func(m *Monitor, id suppliers.EndpointID) {
		// A long input is not timed, and an answer under 50 tokens has no speed sample.
		for range scoreSamples {
			m.Observe(t.Context(), answered(id, t0, 5*time.Second, time.Second, latencyMaxInput+1, speedMinOutput-1))
		}
	}
	oneFailure := func(m *Monitor, id suppliers.EndpointID) {
		m.Observe(t.Context(), Observation{Endpoint: id, Failure: failed500, Ended: t0})
	}
	fastEnd := t0.Add(1500 * time.Millisecond) // fast's last sample is dated at the answer's end
	slowEnd := t0.Add(20 * time.Second)
	noPerformanceEnd := t0.Add(6 * time.Second)
	for _, c := range []struct {
		name                string
		observe             func(*Monitor, suppliers.EndpointID)
		now                 time.Time
		quality, confidence float64
	}{
		{"unknown", func(*Monitor, suppliers.EndpointID) {}, t0, 1, 0},
		{"fast and reliable", fast, fastEnd, 1, 1},
		{"slow", slow, slowEnd, 0.15, 1}, // (0.1 + 0.2) / 2
		{"one failure", oneFailure, t0, 0.8, 0.1},
		{"no performance sample", noPerformance, noPerformanceEnd, 1, 1},
		{"confidence half at half the age", fast, fastEnd.Add(scoreMaxAge / 2), 1, 0.5},
		{"confidence gone after the age", fast, fastEnd.Add(scoreMaxAge), 1, 0},
	} {
		m := New(nil)
		id := suppliers.EndpointID{1}
		c.observe(m, id)
		got := m.Measured(id, c.now)
		if !near(got.Quality, c.quality) || !near(got.Confidence, c.confidence) {
			t.Errorf("%s: quality %.3f confidence %.3f, want %.3f and %.3f",
				c.name, got.Quality, got.Confidence, c.quality, c.confidence)
		}
	}
}

// TestQualityWithoutCountedOutcomesIsNeutral: an uncounted failure can still leave independent latency samples—a
// stream that answers slowly and then reports rate limiting is one—but without a counted outcome the endpoint stays
// unknown: quality 1 and confidence 0, never a low score derived from performance samples alone.
func TestQualityWithoutCountedOutcomesIsNeutral(t *testing.T) {
	m := New(nil)
	id := suppliers.EndpointID{1}
	for range minSamples {
		o := answered(id, t0, 10*time.Second, time.Second, 100, 100)
		o.Done, o.Fault = false, protocol.FaultRateLimited
		m.Observe(t.Context(), o)
	}
	got := m.Measured(id, t0.Add(11*time.Second))
	if got.Latency != 10*time.Second {
		t.Fatalf("latency = %v, want the independent samples to stay measured", got.Latency)
	}
	if got.Quality != 1 || got.Confidence != 0 {
		t.Fatalf("no counted outcomes: quality %v confidence %v, want 1 and 0", got.Quality, got.Confidence)
	}
}

// TestProbeResultsScoreTheEndpoint: a probe is a real generation, so its result counts as quality evidence—a pass and
// an endpoint-scope failure both move the score—but it never enters the breaker window, which needs independent sources.
func TestProbeResultsScoreTheEndpoint(t *testing.T) {
	for _, c := range []struct {
		name   string
		result suppliers.TestResult
		health float64
	}{
		{"passed", suppliers.TestResult{Streamed: true}, 1},
		{"failed", suppliers.TestResult{Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{Status: 500}}}, 0.8},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := New(nil)
			now := t0
			l := &Loop{monitor: m, now: func() time.Time { return now }, logger: slog.Default()}
			id := suppliers.EndpointID{9}
			e := m.of(id)
			claim := healthRecord{ID: id, Opened: now.Add(-time.Minute).UnixNano(), Owner: uuid.NewV7(), Lease: now.Add(time.Minute).UnixNano()}
			e.mu.Lock()
			e.record = claim
			e.mu.Unlock()
			l.finish(t.Context(), probeEnd{claim: claim, result: c.result})
			got := m.Measured(id, now)
			if !near(got.Health, c.health) || !near(got.Quality, c.health) || !near(got.Confidence, 0.1) {
				t.Errorf("probe %s: %+v, want health and quality %v and confidence 0.1", c.name, got, c.health)
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.window.n != 0 {
				t.Errorf("probe %s entered the breaker window: %d outcomes", c.name, e.window.n)
			}
		})
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
