package routing

import (
	"sync"
	"time"
)

// budget is each supplier's retry budget: the retries it receives in a window may not exceed a share of the requests
// it received, plus a few that are always allowed so a quiet supplier can still be retried onto. When it is used up,
// retries stop going to that supplier and still go to others, so one supplier's trouble does not become a storm of
// retries on it (sub2api issue #7895 is that storm). The window is a ring of buckets, so old traffic ages out.
type budget struct {
	mu        sync.Mutex
	ratio     float64
	floor     int64
	bucket    time.Duration
	suppliers map[SupplierID]*ring
}

const buckets = 10

type ring struct {
	start    int64 // the index of the bucket that was current at the last write
	requests [buckets]int64
	retries  [buckets]int64
}

func newBudget(ratio float64, floor int64, window time.Duration) *budget {
	return &budget{ratio: ratio, floor: floor, bucket: window / buckets, suppliers: map[SupplierID]*ring{}}
}

// advance moves the ring to the bucket of now, clearing the buckets it passes.
func (r *ring) advance(now int64) {
	if now-r.start >= buckets {
		*r = ring{start: now}
		return
	}
	for r.start < now {
		r.start++
		i := r.start % buckets
		r.requests[i], r.retries[i] = 0, 0
	}
}

func (b *budget) ring(id SupplierID, now time.Time) (*ring, int64) {
	r, ok := b.suppliers[id]
	idx := now.UnixNano() / int64(b.bucket)
	if !ok {
		r = &ring{start: idx}
		b.suppliers[id] = r
	}
	r.advance(idx)
	return r, idx
}

// request counts a first attempt sent to the supplier.
func (b *budget) request(id SupplierID, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, idx := b.ring(id, now)
	r.requests[idx%buckets]++
}

// fits reports whether one more retry fits the supplier's window.
func (b *budget) fits(r *ring) bool {
	var requests, retries int64
	for i := range buckets {
		requests += r.requests[i]
		retries += r.retries[i]
	}
	return float64(retries+1) <= b.ratio*float64(requests)+float64(b.floor)
}

// allow reports whether a retry fits, using none of the budget.
func (b *budget) allow(id SupplierID, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, _ := b.ring(id, now)
	return b.fits(r)
}

// use takes one retry of the budget if it fits.
func (b *budget) use(id SupplierID, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, idx := b.ring(id, now)
	if !b.fits(r) {
		return false
	}
	r.retries[idx%buckets]++
	return true
}

// Sent counts a first attempt to the supplier, which earns its retry budget.
func (r *Router) Sent(id SupplierID) { r.budget.request(id, r.cfg.Now()) }

// TakeRetry uses one retry of the supplier's budget and reports whether it fit; the gateway calls it when it is about
// to send a retry, so concurrent retries cannot all pass a check that none of them has paid for. Plan only looks.
func (r *Router) TakeRetry(id SupplierID) bool { return r.budget.use(id, r.cfg.Now()) }
