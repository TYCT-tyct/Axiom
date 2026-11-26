package quality

import (
	"slices"
	"time"
)

// The rolling window the percentile preferences of a route read (OpenRouter《Provider Routing》「Performance Thresholds」,
// 2026-11-01: p50, p75, p90 and p99 over a rolling five-minute window). The sample count is bounded so an endpoint's
// memory is fixed no matter how fast it serves: the window holds the most recent percentileSamples samples that are no
// older than percentileWindow, and a high-traffic endpoint's percentiles are those of its latest sample of that size.
// The samples are the same ones the moving averages take, so a percentile never rests on an attempt the averages
// excluded.
const (
	percentileWindow  = 5 * time.Minute
	percentileSamples = 128
)

// Percentiles are the p50, p75, p90 and p99 of a window, in that order.
const (
	P50 = iota
	P75
	P90
	P99
)

// sampleWindow is a fixed ring of samples. It never allocates: adds overwrite the oldest sample, and a read copies the
// live samples into a local array and sorts them.
type sampleWindow struct {
	values [percentileSamples]float64
	times  [percentileSamples]int64 // UnixNano, so a sample is 16 bytes
	next   int                      // the slot the next sample goes in
	n      int                      // how many slots hold a sample, at most percentileSamples
}

func (w *sampleWindow) add(v float64, at time.Time) {
	w.values[w.next] = v
	w.times[w.next] = at.UnixNano()
	w.next = (w.next + 1) % percentileSamples
	if w.n < percentileSamples {
		w.n++
	}
}

// percentiles returns the four percentiles of the samples newer than the window, and false while fewer than minSamples
// of them are, which is what makes a percentile unknown rather than zero. The caller holds the endpoint's lock.
func (w *sampleWindow) percentiles(now time.Time) ([4]float64, bool) {
	var live [percentileSamples]float64
	cutoff := now.Add(-percentileWindow).UnixNano()
	n := 0
	for i := range w.n {
		if w.times[i] >= cutoff {
			live[n] = w.values[i]
			n++
		}
	}
	if n < minSamples {
		return [4]float64{}, false
	}
	slices.Sort(live[:n])
	var out [4]float64
	for i, p := range [...]int{50, 75, 90, 99} {
		out[i] = live[nearestRank(p, n)]
	}
	return out, true
}

// nearestRank is the zero-based index of the p-th percentile by nearest rank: rank ceil(p × n / 100), counted from 1.
func nearestRank(p, n int) int {
	return max((p*n+99)/100, 1) - 1
}
