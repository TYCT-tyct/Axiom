package quality

import (
	"testing"
	"time"

	"axiom/internal/suppliers"
)

// TestWindowExpiresAndIsBounded: a sample older than five minutes is not read, fewer than five fresh samples are
// unknown, and the ring keeps only the most recent percentileSamples whatever it is given.
func TestWindowExpiresAndIsBounded(t *testing.T) {
	var w sampleWindow
	now := time.Unix(1_000, 0)
	for range minSamples {
		w.add(1, now.Add(-percentileWindow-time.Second))
	}
	if _, ok := w.percentiles(now); ok {
		t.Fatal("samples older than the window are measured")
	}
	for range minSamples {
		w.add(2, now)
	}
	if got, ok := w.percentiles(now); !ok || got[0] != 2 {
		t.Fatalf("five fresh samples: %v %v, want a p50 of 2", got, ok)
	}

	for i := range 200 {
		w.add(float64(i+1), now)
	}
	if w.n != percentileSamples {
		t.Fatalf("window holds %d samples, want %d", w.n, percentileSamples)
	}
	got, ok := w.percentiles(now)
	if !ok {
		t.Fatal("a full window is not measured")
	}
	// The 200 values 1..200 leave the latest 128: 73..200. Their p50 is the 64th of 128, 136.
	if got[P50] != 136 || got[P99] != 199 {
		t.Fatalf("p50, p99 = %v, %v, want 136, 199", got[P50], got[P99])
	}
}

// TestMeasuredPercentiles: MeasuredFor carries the window's percentiles into routing's copy, and they expire with it.
func TestMeasuredPercentiles(t *testing.T) {
	m := New(nil)
	id := suppliers.EndpointID{1}
	for i := range 5 {
		m.Observe(t.Context(), answered(id, t0, time.Duration(i+1)*time.Second, time.Second, 100, 50))
	}
	got := measured(m, id, t0.Add(10*time.Second))
	for i, want := range [...]time.Duration{3 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second} {
		if got.LatencyPercentiles[i] != want {
			t.Errorf("latency p%d = %v, want %v", [...]int{50, 75, 90, 99}[i], got.LatencyPercentiles[i], want)
		}
	}
	for i, v := range got.ThroughputPercentiles {
		if v != 50 {
			t.Errorf("throughput percentile %d = %v, want 50", i, v)
		}
	}
	stale := measured(m, id, t0.Add(percentileWindow+time.Minute))
	if stale.LatencyPercentiles != [4]time.Duration{} || stale.ThroughputPercentiles != [4]float64{} {
		t.Fatalf("six minutes after the last sample: %+v, want no percentiles", stale)
	}
}
