package store_test

import (
	"context"
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

type bucketOp = func(context.Context, pg.Tx, []store.RateBucket) error

func takeBuckets(ctx context.Context, tx pg.Tx, buckets []store.RateBucket) error {
	_, err := store.LockRateBucketsFor(ctx, tx, buckets)
	return err
}

func blockBuckets(ctx context.Context, tx pg.Tx, buckets []store.RateBucket) error {
	return store.BlockRateBuckets(ctx, tx, buckets, time.Now().Add(time.Minute))
}

// someoneWaits blocks until a statement of another connection is waiting for a lock.
func someoneWaits(ctx context.Context, t *testing.T, db *pg.DB) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var n int
		err := db.ReadTx(ctx, platform, func(tx pg.Tx) error {
			return tx.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'").Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
	}
	t.Fatal("no statement waited for a lock")
}

// crossed has holder take the bucket "critical" and, once late — started meanwhile on another connection and naming both
// buckets — is waiting, name both itself, so that the two ask for the buckets in opposite orders.
func crossed(t *testing.T, db *pg.DB, holder, late bucketOp) {
	t.Helper()
	critical := store.RateBucket{Scope: "ip:a", Name: "critical"}
	both := []store.RateBucket{{Scope: "ip:a", Name: "global"}, critical}
	ended := make(chan error, 1)
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		ctx := t.Context()
		if err := holder(ctx, tx, []store.RateBucket{critical}); err != nil {
			return err
		}
		go func() { ended <- db.Tx(ctx, platform, func(tx pg.Tx) error { return late(ctx, tx, both) }) }()
		someoneWaits(ctx, t, db)
		return holder(ctx, tx, both)
	})
	if err != nil {
		t.Errorf("the side that held %q: %v", critical.Name, err)
	}
	if err := <-ended; err != nil {
		t.Errorf("the side that came later: %v", err)
	}
}

// TestBucketsAreAlwaysTakenInKeyOrder: requests that name the same two buckets in opposite orders wait for each other
// instead of deadlocking, whichever of taking and closing they do and whether the buckets exist yet. One side holds
// "critical" and the other, started meanwhile, has to wait for it without holding "global"; if it took "global" first,
// the side holding "critical" would then wait for it and PostgreSQL would break the cycle by failing one of them.
func TestBucketsAreAlwaysTakenInKeyOrder(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		exists       bool
		holder, late bucketOp
	}{
		"take then block, existing": {true, takeBuckets, blockBuckets},
		"block then take, existing": {true, blockBuckets, takeBuckets},
		"take then block, new":      {false, takeBuckets, blockBuckets},
		"block then take, new":      {false, blockBuckets, takeBuckets},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := open(t)
			if c.exists {
				both := []store.RateBucket{{Scope: "ip:a", Name: "global"}, {Scope: "ip:a", Name: "critical"}}
				if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error { return takeBuckets(ctx, tx, both) }); err != nil {
					t.Fatal(err)
				}
			}
			crossed(t, db, c.holder, c.late)
		})
	}
}
