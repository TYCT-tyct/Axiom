// Package client sends an adapter's requests: through platform/egress, to the origin a supplier was connected with,
// and only once every rate bucket the request names has allowance (internal/suppliers/README.md「适配器」).
package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// Buckets is the supplier's rate limits as the client asks of them. Limiter counts in one process; SharedLimiter, in the
// database, for nodes that reach the supplier from one address. A name that repeats among the buckets of one call is
// that one bucket: the request counts once in it, and the statements that make and close the shared buckets cannot name
// one key twice.
type Buckets interface {
	// Take takes one request from each bucket in names, or from none. It returns Blocked, with the time the last of the
	// refusing buckets reopens, when it takes nothing.
	Take(ctx context.Context, names []string) error
	// Block closes the buckets in names until then, as the supplier's own refusal says to.
	Block(ctx context.Context, names []string, until time.Time) error
}

// dedupe leaves each bucket name once, in the order given: a request takes one allowance from each bucket it names, not
// one per mention, and ON CONFLICT DO UPDATE cannot affect a row a second time, so the statements that make and close
// the shared buckets may not name one key twice.
func dedupe(names []string) []string {
	seen := make(map[string]bool, len(names))
	once := make([]string, 0, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		once = append(once, name)
	}
	return once
}

// Limiter counts requests against a supplier's rate limits, in this process: for the tests of the adapters, and for a
// node that is alone at its address.
type Limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*bucket
}

type bucket struct {
	spec         adapter.BucketSpec
	windowStart  time.Time
	used         int
	blockedUntil time.Time
}

// NewLimiter makes a limiter of the declared buckets. now is the clock, so tests move time.
func NewLimiter(specs []adapter.BucketSpec, now func() time.Time) *Limiter {
	l := &Limiter{now: now, buckets: make(map[string]*bucket, len(specs))}
	for _, s := range specs {
		l.buckets[s.Name] = &bucket{spec: s}
	}
	return l
}

// Take takes one request from each bucket in names, or from none: a request that one bucket refuses leaves the others
// as they were. It returns Blocked, with the time the last of the refusing buckets reopens, when it takes nothing.
func (l *Limiter) Take(_ context.Context, names []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	names = dedupe(names)
	now := l.now()
	var until time.Time
	for _, name := range names {
		b, ok := l.buckets[name]
		if !ok {
			return fmt.Errorf("client: the request names the bucket %q, which its adapter did not declare", name)
		}
		if t := b.reopens(now); t.After(until) {
			until = t
		}
	}
	if !until.IsZero() {
		return adapter.Blocked{Until: until}
	}
	for _, name := range names {
		l.buckets[name].take(now)
	}
	return nil
}

// take counts a request at now, in a new window if the last has ended.
func (b *bucket) take(now time.Time) {
	if b.windowStart.IsZero() || !now.Before(b.windowStart.Add(b.spec.Window)) {
		b.windowStart, b.used = now, 0
	}
	b.used++
}

// reopens is when the bucket next admits a request, or the zero time if it admits one now.
func (b *bucket) reopens(now time.Time) time.Time {
	if now.Before(b.blockedUntil) {
		return b.blockedUntil
	}
	windowEnd := b.windowStart.Add(b.spec.Window)
	if !b.windowStart.IsZero() && now.Before(windowEnd) && b.used >= b.spec.Limit {
		return windowEnd
	}
	return time.Time{}
}

// Block closes the buckets in names until then, as the supplier's own refusal says to. A refusal that does not say
// which of the buckets a request counted in is applied to all of them.
func (l *Limiter) Block(_ context.Context, names []string, until time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, name := range names {
		if b, ok := l.buckets[name]; ok && until.After(b.blockedUntil) {
			b.blockedUntil = until
		}
	}
	return nil
}
