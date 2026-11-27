package quality

import (
	"math"
	"time"
)

// Recent is an endpoint's recent record as the public endpoints view shows it, in OpenRouter's shape (《List all
// endpoints for a model》, 2026-11-26: uptime_last_5m, uptime_last_30m, uptime_last_1d, latency_last_30m,
// throughput_last_30m). Uptime is the share of counted attempts that succeeded over the last five minutes, thirty minutes
// and day, by UptimeWindows; an attempt the health table does not count (the caller's fault, a rate limit, our own cut)
// is in none (docs/domains/quality.md「什么算一次失败」). Latency is the time to first token p50/p75/p90/p99 over the last
// thirty minutes. Throughput is OpenRouter's pN of output speed over the same window: the speed N% of answers reached or
// exceeded, so its p90 is the lower. A window resting on fewer than minSamples samples is unknown: its UptimeKnown is
// false, its percentiles 0. Every node counts its own attempts and starts again when it restarts, as the health does.
type Recent struct {
	Uptime      [3]float64 // 0 to 1
	UptimeKnown [3]bool
	Latency     [4]time.Duration
	Throughput  [4]float64 // output tokens per second
}

// UptimeWindows are the spans of Recent.Uptime, in its order.
var UptimeWindows = [3]time.Duration{5 * time.Minute, 30 * time.Minute, 24 * time.Hour}

// Percent is window i's uptime as the published percentage, to a hundredth; false while it is not known.
func (r Recent) Percent(i int) (float64, bool) {
	return math.Round(r.Uptime[i]*10000) / 100, r.UptimeKnown[i]
}

// LatencyMS is the time to first token percentiles in milliseconds, to a tenth; false while they are not measured.
func (r Recent) LatencyMS() ([4]float64, bool) {
	var ms [4]float64
	for i, d := range r.Latency {
		ms[i] = math.Round(float64(d)/float64(time.Millisecond)*10) / 10
	}
	return ms, r.Latency[0] > 0
}

// Speeds is the throughput percentiles to a tenth of a token a second; false while they are not measured.
func (r Recent) Speeds() ([4]float64, bool) {
	var s [4]float64
	for i, v := range r.Throughput {
		s[i] = math.Round(v*10) / 10
	}
	return s, r.Throughput[0] > 0
}

// A window is read from fixed buckets: every bucket that overlaps it counts whole, so a window takes in up to one
// bucket more than its span at its start, and never less. The buckets are sized so that bound is small next to the
// window: minutes for the five and thirty minutes, hours for the day, five minutes for the thirty minutes' percentiles.
const (
	recentSlot   = 5 * time.Minute // a percentile bucket
	recentWindow = 30 * time.Minute
)

// outcomes counts counted attempts by minute, which the five- and thirty-minute windows read, and by hour, which the
// day reads. Each ring holds a window's buckets and the one it partly overlaps; adding never allocates.
type outcomes struct {
	minutes [31]outcomeBucket
	hours   [25]outcomeBucket
}

// outcomeBucket is one minute's or hour's counts; at is its slot (Unix minute or hour), and a bucket of another slot is
// empty.
type outcomeBucket struct {
	at         int64
	ok, failed uint32
}

func (o *outcomes) add(ok bool, at time.Time) {
	b := &o.minutes[slot(at, time.Minute)%int64(len(o.minutes))]
	if b.claim(slot(at, time.Minute)) {
		b.count(ok)
	}
	b = &o.hours[slot(at, time.Hour)%int64(len(o.hours))]
	if b.claim(slot(at, time.Hour)) {
		b.count(ok)
	}
}

func slot(at time.Time, width time.Duration) int64 { return at.UnixNano() / int64(width) }

// claim makes b the bucket of slot s, emptying it when it still holds an older slot. An attempt older than the bucket's
// slot, which a slow attempt ending late can be, is refused rather than erasing the newer counts.
func (b *outcomeBucket) claim(s int64) bool {
	switch {
	case s < b.at:
		return false
	case s > b.at:
		*b = outcomeBucket{at: s}
	}
	return true
}

func (b *outcomeBucket) count(ok bool) {
	if ok {
		b.ok++
	} else {
		b.failed++
	}
}

// share is the succeeded share of the buckets that overlap the span up to now, and false under minSamples attempts.
func share(buckets []outcomeBucket, now time.Time, span, width time.Duration) (float64, bool) {
	from, to := slot(now.Add(-span), width), slot(now, width)
	var ok, all uint64
	for _, b := range buckets {
		if b.at >= from && b.at <= to {
			ok, all = ok+uint64(b.ok), all+uint64(b.ok)+uint64(b.failed)
		}
	}
	if all < minSamples {
		return 0, false
	}
	return float64(ok) / float64(all), true
}

// The histogram's bins are geometric, four an octave from 2^-4 to 2^20: a time to first token in milliseconds and a
// speed in tokens a second both fit, and a value read back from its bin is within 9% of every value in it (2^(1/8)).
const (
	binsPerOctave = 4
	binFloor      = -4
	histogramBins = (20 - binFloor) * binsPerOctave
)

// histogram counts samples in geometric bins by five-minute slot, enough slots for the thirty minutes and the one they
// partly overlap: its size is fixed however many samples come, and adding never allocates.
type histogram struct {
	slots [recentWindow/recentSlot + 1]struct {
		at   int64
		bins [histogramBins]uint32
	}
}

func (h *histogram) add(v float64, at time.Time) {
	s := slot(at, recentSlot)
	b := &h.slots[s%int64(len(h.slots))]
	switch {
	case s < b.at:
		return // as outcomeBucket.claim
	case s > b.at:
		b.at, b.bins = s, [histogramBins]uint32{}
	}
	b.bins[binOf(v)]++
}

func binOf(v float64) int {
	if v <= 0 {
		return 0
	}
	return min(max(int(math.Floor((math.Log2(v)-binFloor)*binsPerOctave)), 0), histogramBins-1)
}

// binValue is the geometric middle of bin i.
func binValue(i int) float64 {
	return math.Exp2(float64(binFloor) + (float64(i)+0.5)/binsPerOctave)
}

// ranks reads the 1-based ranks rank(n) of the window's samples, counted from the smallest, as their bins' values;
// false under minSamples samples.
func (h *histogram) ranks(now time.Time, rank func(n uint64) [4]uint64) ([4]float64, bool) {
	from, to := slot(now.Add(-recentWindow), recentSlot), slot(now, recentSlot)
	var bins [histogramBins]uint64
	var n uint64
	for i := range h.slots {
		if s := &h.slots[i]; s.at >= from && s.at <= to {
			for b, c := range s.bins {
				bins[b] += uint64(c)
				n += uint64(c)
			}
		}
	}
	if n < minSamples {
		return [4]float64{}, false
	}
	var out [4]float64
	for i, r := range rank(n) {
		var seen uint64
		for b, c := range bins {
			if seen += c; seen >= r {
				out[i] = binValue(b)
				break
			}
		}
	}
	return out, true
}

// fromBottom is the nearest rank of the p-th percentile, ceil(p × n / 100).
func fromBottom(n uint64) [4]uint64 {
	var r [4]uint64
	for i, p := range [4]uint64{50, 75, 90, 99} {
		r[i] = max((p*n+99)/100, 1)
	}
	return r
}

// reachedBy is, for p 50, 75, 90 and 99, the rank from the bottom of the speed p% of the samples reach or exceed: the
// ceil(p × n / 100)-th largest.
func reachedBy(n uint64) [4]uint64 {
	r := fromBottom(n)
	for i := range r {
		r[i] = n - r[i] + 1
	}
	return r
}

// recent is the endpoint's Recent at now; the caller holds e.mu.
func (e *State) recent(now time.Time) Recent {
	var r Recent
	r.Uptime[0], r.UptimeKnown[0] = share(e.outcomes.minutes[:], now, UptimeWindows[0], time.Minute)
	r.Uptime[1], r.UptimeKnown[1] = share(e.outcomes.minutes[:], now, UptimeWindows[1], time.Minute)
	r.Uptime[2], r.UptimeKnown[2] = share(e.outcomes.hours[:], now, UptimeWindows[2], time.Hour)
	if v, ok := e.recentLatency.ranks(now, fromBottom); ok {
		for i := range v {
			r.Latency[i] = time.Duration(v[i] * float64(time.Millisecond))
		}
	}
	if v, ok := e.recentSpeed.ranks(now, reachedBy); ok {
		r.Throughput = v
	}
	return r
}
