package gateway

import (
	"sync"
	"time"
)

// RateCounter is the precheck's rate count (docs/domains/routing.md「容量感知」): it counts and never judges, and
// nothing is released. The estimate over the last window is this window's count plus the share of the previous
// window's that the sliding window still covers. Two maps rotate with the window, so the memory held is bounded by
// the keys of two windows however many cold keys pass.
type RateCounter struct {
	mu        sync.Mutex
	window    time.Duration
	start     time.Time // when cur began
	cur, prev map[string]int
}

// NewRateCounter counts over a sliding window of the given length.
func NewRateCounter(window time.Duration) *RateCounter {
	return &RateCounter{window: window, start: time.Now(), cur: map[string]int{}}
}

// Count adds one for key and returns the estimated count over the last window, this call included.
func (r *RateCounter) Count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if elapsed := now.Sub(r.start); elapsed >= r.window {
		r.prev = r.cur
		if elapsed >= 2*r.window {
			r.prev = nil // nothing in the previous window is in the sliding one
		}
		r.cur = map[string]int{}
		r.start = r.start.Add(elapsed / r.window * r.window)
	}
	elapsed := now.Sub(r.start)
	r.cur[key]++
	return r.cur[key] + int(float64(r.prev[key])*(1-float64(elapsed)/float64(r.window)))
}
