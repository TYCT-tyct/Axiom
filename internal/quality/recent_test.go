package quality

import (
	"testing"
	"time"

	"axiom/internal/suppliers"
)

// TestRecentWindows: uptime counts the counted attempts of each window and nothing else (a rate limit is the caller's),
// a window under five of them is unknown, an attempt that ends after its minute's bucket was reused does not erase the
// newer counts, and the thirty-minute speeds are those 50% and 90% of answers reached or exceeded.
func TestRecentWindows(t *testing.T) {
	m := New(nil)
	id := suppliers.EndpointID{7}
	now := t0.Add(30 * time.Second)
	observe := func(at time.Time, n int, ok bool) {
		for i := range n {
			o := Observation{Endpoint: id, Failure: failed500, Ended: at}
			if ok {
				o = answered(id, at.Add(-time.Duration(i+2)*time.Second), time.Second, time.Duration(i+1)*time.Second, 10, 100)
			}
			m.Observe(t.Context(), o)
		}
	}
	observe(now.Add(-3*time.Hour), 10, false)
	observe(now.Add(-20*time.Minute), 5, true)
	observe(now.Add(-20*time.Minute), 5, false)
	observe(now, 5, true)
	observe(now.Add(-30*time.Minute), 1, false) // its minute's bucket now holds the current minute
	m.Observe(t.Context(), Observation{Endpoint: id, Ended: now,
		Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.RateLimited{}}})

	for _, tc := range []struct {
		name  string
		at    time.Time
		known [3]bool
		share [3]float64
		speed bool
	}{
		{"now", now, [3]bool{true, true, true}, [3]float64{1, 10.0 / 15, 10.0 / 26}, true},
		{"an hour on", now.Add(time.Hour), [3]bool{false, false, true}, [3]float64{0, 0, 10.0 / 26}, false},
		{"a day on", now.Add(25 * time.Hour), [3]bool{}, [3]float64{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := measured(m, id, tc.at).Recent
			for i := range 3 {
				if r.UptimeKnown[i] != tc.known[i] || !near(r.Uptime[i], tc.share[i]) {
					t.Errorf("uptime %v: %v %v, want %v %v", UptimeWindows[i], r.Uptime[i], r.UptimeKnown[i], tc.share[i], tc.known[i])
				}
			}
			assertRecentSpeeds(t, r, tc.speed)
		})
	}
}

// assertRecentSpeeds: ten answers of 100 tokens over 1 to 5 s, twice, have the median speed 100/3 and the speed 90%
// reached 20, with a first token after 1 s; without recent answers there are none.
func assertRecentSpeeds(t *testing.T, r Recent, measured bool) {
	t.Helper()
	if !measured {
		if r.Latency != [4]time.Duration{} || r.Throughput != [4]float64{} {
			t.Errorf("stale percentiles: %+v", r)
		}
		return
	}
	if !near(r.Throughput[0], 100.0/3) || r.Throughput[2] != 20 || r.Latency[0] != time.Second {
		t.Errorf("thirty minutes: %+v", r)
	}
}
