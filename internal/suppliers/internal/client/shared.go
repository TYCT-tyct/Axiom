package client

import (
	"context"
	"fmt"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
)

// Scopes name whom a supplier counts requests for. Suppliers that share a key share its counts: the address is the
// origin and the exit the node reaches it from, so every supplier at one host shares the buckets counted by address,
// and the account is the supplier's own.
type Scopes struct{ Address, Account string }

// SharedLimiter counts requests in the database, so that nodes that reach one supplier from one address count in the
// same buckets. A request takes one transaction: the buckets it names are locked in key order, checked, and counted
// together, which is also what makes it all or none. The count of a bucket is fixed-window like Limiter's, and the
// window starts at the clock of the node that took its first request.
type SharedLimiter struct {
	db     *pg.DB
	scope  pg.Scope
	specs  map[string]adapter.BucketSpec
	scopes Scopes
	now    func() time.Time
}

var _ Buckets = (*SharedLimiter)(nil)

// NewSharedLimiter makes the limiter of one supplier's declared buckets. scope is the platform scope, which only cmd/
// builds.
func NewSharedLimiter(db *pg.DB, scope pg.Scope, specs []adapter.BucketSpec, scopes Scopes, now func() time.Time) *SharedLimiter {
	l := &SharedLimiter{db: db, scope: scope, specs: make(map[string]adapter.BucketSpec, len(specs)), scopes: scopes, now: now}
	for _, s := range specs {
		l.specs[s.Name] = s
	}
	return l
}

// key is the bucket a name stands for.
func (l *SharedLimiter) key(name string) (store.RateBucket, bool) {
	spec, ok := l.specs[name]
	if !ok {
		return store.RateBucket{}, false
	}
	scope := l.scopes.Address
	if spec.Per == adapter.CountedByAccount {
		scope = l.scopes.Account
	}
	return store.RateBucket{Scope: scope, Name: name}, true
}

// Take implements Buckets. A request that names a bucket its adapter did not declare is a mistake of the adapter's and
// is refused before the database is asked.
func (l *SharedLimiter) Take(ctx context.Context, names []string) error {
	names = dedupe(names)
	keys := make([]store.RateBucket, len(names))
	for i, name := range names {
		k, ok := l.key(name)
		if !ok {
			return fmt.Errorf("client: the request names the bucket %q, which its adapter did not declare", name)
		}
		keys[i] = k
	}
	var blocked error
	err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		rows, err := store.LockRateBucketsFor(ctx, tx, keys)
		if err != nil {
			return err
		}
		buckets, now := l.buckets(rows), l.now()
		if until := reopening(buckets, now); !until.IsZero() {
			blocked = adapter.Blocked{Until: until}
			return nil
		}
		for i, r := range rows {
			buckets[i].take(now)
			err := store.New().SetRateBucket(ctx, tx, store.SetRateBucketParams{
				Scope: r.Scope, Name: r.Name, WindowStart: &buckets[i].windowStart, Used: int32(buckets[i].used), //nolint:gosec // a count under the bucket's limit
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("client: count the request in the shared buckets: %w", err)
	}
	return blocked
}

// buckets are the rows as the buckets of Limiter are, so that both count by the same rules.
func (l *SharedLimiter) buckets(rows []store.RateBucketRow) []bucket {
	out := make([]bucket, len(rows))
	for i, r := range rows {
		out[i] = bucket{spec: l.specs[r.Name], windowStart: r.WindowStart, used: r.Used, blockedUntil: r.BlockedUntil}
	}
	return out
}

// reopening is when the last of the buckets that refuse a request reopens, or the zero time if none does.
func reopening(buckets []bucket, now time.Time) (until time.Time) {
	for _, b := range buckets {
		if t := b.reopens(now); t.After(until) {
			until = t
		}
	}
	return until
}

// Block implements Buckets.
func (l *SharedLimiter) Block(ctx context.Context, names []string, until time.Time) error {
	var keys []store.RateBucket
	for _, name := range dedupe(names) {
		if k, ok := l.key(name); ok {
			keys = append(keys, k)
		}
	}
	err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error { return store.BlockRateBuckets(ctx, tx, keys, until) })
	if err != nil {
		return fmt.Errorf("client: close the shared buckets: %w", err)
	}
	return nil
}
