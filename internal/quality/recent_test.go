package quality

import (
	"context"
	"math"
	"testing"
	"time"

	"axiom/internal/suppliers"
)

// outcomesAt observes n attempts at id ending at at: successes that output 100 tokens, or 5xx failures.
func outcomesAt(m *Monitor, id suppliers.EndpointID, at time.Time, n int, ok bool) {
	for range n {
		o := Observation{Endpoint: id, Failure: failed500, Ended: at}
		if ok {
			o = answered(id, at.Add(-2*time.Second), time.Second, time.Second, 10, 100)
		}
		m.Observe(context.Background(), o)
	}
}

// TestRecentUptime: uptime counts the counted attempts of each window, a rate limit being the caller's; a window
// includes the whole bucket its start falls in, so an attempt inside the span is never dropped; a window under five
// attempts is unknown; and an attempt that ends after its bucket was reused does not erase the newer counts.
func TestRecentUptime(t *testing.T) {
	at := func(clock string) time.Time {
		v, err := time.Parse(time.TimeOnly, clock)
		if err != nil {
			t.Fatal(err)
		}
		return t0.Truncate(24 * time.Hour).Add(v.Sub(v.Truncate(24 * time.Hour)))
	}
	for _, tc := range []struct {
		name  string
		seed  func(m *Monitor, id suppliers.EndpointID)
		now   string
		known [3]bool
		share [3]float64
	}{
		{"the start's bucket counts", func(m *Monitor, id suppliers.EndpointID) {
			outcomesAt(m, id, at("12:00:50"), 5, false)
			outcomesAt(m, id, at("12:05:05"), 5, true)
		}, "12:05:10", [3]bool{true, true, true}, [3]float64{0.5, 0.5, 0.5}},
		{"the day reaches back 24 hours", func(m *Monitor, id suppliers.EndpointID) {
			outcomesAt(m, id, at("11:40:00").Add(-24*time.Hour), 5, false)
			outcomesAt(m, id, at("10:50:00"), 5, true)
		}, "11:30:00", [3]bool{false, false, true}, [3]float64{0, 0, 0.5}},
		{"a rate limit is not counted", func(m *Monitor, id suppliers.EndpointID) {
			outcomesAt(m, id, at("12:00:00"), 5, true)
			m.Observe(context.Background(), Observation{
				Endpoint: id, Ended: at("12:00:00"),
				Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.RateLimited{}},
			})
		}, "12:00:30", [3]bool{true, true, true}, [3]float64{1, 1, 1}},
		{"a late end keeps the newer minute", func(m *Monitor, id suppliers.EndpointID) {
			outcomesAt(m, id, at("12:31:00"), 5, true)
			outcomesAt(m, id, at("12:00:00"), 1, false) // the same minute bucket, 31 minutes older
		}, "12:31:30", [3]bool{true, true, true}, [3]float64{1, 1, 5.0 / 6}},
		{
			"too few", func(m *Monitor, id suppliers.EndpointID) { outcomesAt(m, id, at("12:00:00"), 4, true) },
			"12:00:30",
			[3]bool{},
			[3]float64{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, id := New(nil), suppliers.EndpointID{7}
			tc.seed(m, id)
			r := measured(m, id, at(tc.now)).Recent
			for i := range 3 {
				if r.UptimeKnown[i] != tc.known[i] || !near(r.Uptime[i], tc.share[i]) {
					t.Errorf("uptime %v: %v %v, want %v %v", UptimeWindows[i], r.Uptime[i], r.UptimeKnown[i], tc.share[i], tc.known[i])
				}
			}
		})
	}
}

// within reports whether got is v read back from its histogram bin, which is at most 2^(1/8) away.
func within(got, v float64) bool { return math.Abs(math.Log2(got/v)) <= 1.0/8 }

// TestRecentPercentiles: the thirty-minute percentiles read every sample of the window however many came, and the
// throughput's pN is the speed N% of answers reached or exceeded.
func TestRecentPercentiles(t *testing.T) {
	m, id := New(nil), suppliers.EndpointID{8}
	start := t0
	// 256 answers with a first token after 1 s, then 128 after 100 ms: the window's median is 1 s.
	for i := range 384 {
		ttft := time.Second
		if i >= 256 {
			ttft = 100 * time.Millisecond
		}
		m.Observe(context.Background(), answered(id, start.Add(time.Duration(i)*time.Second), ttft, time.Second, 10, 1))
	}
	// Ten answers at 10, 20, …, 100 tokens a second: 90% reached 20, 50% reached 60.
	for i := range 10 {
		m.Observe(context.Background(), answered(id, start.Add(400*time.Second), time.Second, 10*time.Second/time.Duration(i+1), 10, 100))
	}
	r := measured(m, id, start.Add(10*time.Minute)).Recent
	if !within(r.Latency[0].Seconds(), 1) {
		t.Errorf("latency p50 %v, want 1s", r.Latency[0])
	}
	if !within(r.Throughput[2], 20) || !within(r.Throughput[0], 60) || r.Throughput[2] >= r.Throughput[0] {
		t.Errorf("throughput %v, want p50 60 and p90 20", r.Throughput)
	}
	if stale := measured(m, id, start.Add(45*time.Minute)).Recent; stale.Latency != [4]time.Duration{} || stale.Throughput != [4]float64{} {
		t.Errorf("forty-five minutes on: %+v", stale)
	}
}
