package quality

import (
	"math"
	"time"
)

// Recent is an endpoint's recent record as the public endpoints view shows it, in OpenRouter's shape (《List all
// endpoints for a model》, 2026-11-26: uptime_last_5m, uptime_last_30m, uptime_last_1d, latency_last_30m,
// throughput_last_30m). Uptime is the share of counted attempts that succeeded over the last five minutes, thirty minutes
// and day, by UptimeWindows; an attempt the health table does not count (the caller's fault, a rate limit, our own cut)
// is in none (docs/domains/quality.md「什么算一次失败」). Latency is the time to first token p50/p75/p90/p99 and
// Throughput the output speeds that 50/75/90/99% of answers reached or exceeded, so its p90 is the lower, both over the
// last thirty minutes. A window resting on fewer than minSamples samples is unknown: its UptimeKnown is false, its
// percentiles 0. Every node counts its own attempts and starts again when it restarts, as the health does.
type Recent struct {
	Uptime      [3]float64 // 0 to 1
	UptimeKnown [3]bool
	Latency     [4]time.Duration
	Throughput  [4]float64 // output tokens per second
}

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

// UptimeWindows are the spans of Recent.Uptime, in its order.
var UptimeWindows = [3]time.Duration{5 * time.Minute, 30 * time.Minute, 24 * time.Hour}

// recentWindow is the span of Recent's percentiles.
const recentWindow = 30 * time.Minute

// outcomes counts counted attempts in fixed buckets by minute, which the five- and thirty-minute windows read, and by
// hour, which the day reads: the last 24 hour buckets, the current one partly, so the day is 23 to 24 hours long. Its
// size is fixed whatever the traffic, and adding never allocates.
type outcomes struct {
	minutes [30]outcomeBucket
	hours   [24]outcomeBucket
}

// outcomeBucket is one minute's or hour's counts; at is its Unix minute or hour, and a bucket of another one is empty.
type outcomeBucket struct {
	at         int64
	ok, failed uint32
}

func (o *outcomes) add(ok bool, at time.Time) {
	bump(o.minutes[:], at.Unix()/60, ok)
	bump(o.hours[:], at.Unix()/3600, ok)
}

// bump counts one attempt in its slot's bucket, which it first empties when the bucket still holds an older slot. An
// attempt older than the bucket's slot, which a slower attempt ending late can be, is dropped rather than erasing the
// newer counts.
func bump(buckets []outcomeBucket, slot int64, ok bool) {
	b := &buckets[slot%int64(len(buckets))]
	switch {
	case slot < b.at:
		return
	case slot > b.at:
		*b = outcomeBucket{at: slot}
	}
	if ok {
		b.ok++
	} else {
		b.failed++
	}
}

// share is the succeeded share of the buckets of the last span slots up to now, and false under minSamples attempts.
func share(buckets []outcomeBucket, now, span int64) (float64, bool) {
	var ok, all uint64
	for _, b := range buckets {
		if b.at > now-span && b.at <= now {
			ok, all = ok+uint64(b.ok), all+uint64(b.ok)+uint64(b.failed)
		}
	}
	if all < minSamples {
		return 0, false
	}
	return float64(ok) / float64(all), true
}

// recent is the endpoint's Recent at now; the caller holds e.mu.
func (e *State) recent(now time.Time) Recent {
	var r Recent
	minute, hour := now.Unix()/60, now.Unix()/3600
	r.Uptime[0], r.UptimeKnown[0] = share(e.outcomes.minutes[:], minute, 5)
	r.Uptime[1], r.UptimeKnown[1] = share(e.outcomes.minutes[:], minute, 30)
	r.Uptime[2], r.UptimeKnown[2] = share(e.outcomes.hours[:], hour, 24)
	if v, ok := e.latencyWindow.ranks(now, recentWindow, [4]int{50, 75, 90, 99}); ok {
		for i := range v {
			r.Latency[i] = time.Duration(v[i] * float64(time.Second))
		}
	}
	// The speed N% reached or exceeded is the (100-N)th percentile of the speeds.
	if v, ok := e.speedWindow.ranks(now, recentWindow, [4]int{50, 25, 10, 1}); ok {
		r.Throughput = v
	}
	return r
}
