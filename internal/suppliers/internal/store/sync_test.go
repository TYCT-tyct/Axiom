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

// someoneWaits blocks until a statement of the connection pid is waiting for a lock. Tests run at the same time on one
// server, so a wait of another test's connection does not count: only this one means the statement of the side that was
// to come later is really waiting.
func someoneWaits(ctx context.Context, t *testing.T, db *pg.DB, pid int32) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var n int
		err := db.ReadTx(ctx, platform, func(tx pg.Tx) error {
			return tx.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE pid = $1 AND wait_event_type = 'Lock'", pid).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
	}
	t.Fatal("no statement of this connection waited for a lock")
}

// waitsOnAnotherConnection runs op in a transaction of its own — on a connection of its own — and returns when a statement
// of that connection is waiting for a lock of the transaction this is called in; the channel it returns carries how op
// ended. The connection reports its pid before op, which is what tells this test's wait from the waits of the other tests
// running at the same time on the same server.
func waitsOnAnotherConnection(ctx context.Context, t *testing.T, db *pg.DB, buckets []store.RateBucket, op bucketOp) <-chan error {
	t.Helper()
	pids, ended := make(chan int32, 1), make(chan error, 1)
	go func() {
		ended <- db.Tx(ctx, platform, func(tx pg.Tx) error {
			var pid int32
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				return err
			}
			pids <- pid
			return op(ctx, tx, buckets)
		})
	}()
	var pid int32
	select {
	case pid = <-pids:
	case err := <-ended: // the transaction ended before op: no statement of it waits
		t.Fatalf("the statement that was to wait ended instead: %v", err)
	}
	someoneWaits(ctx, t, db, pid)
	return ended
}

// crossed has holder take the bucket "critical" and, once late — started meanwhile on another connection and naming both
// buckets — is waiting, name both itself, so that the two ask for the buckets in opposite orders.
func crossed(t *testing.T, db *pg.DB, holder, late bucketOp) {
	t.Helper()
	critical := store.RateBucket{Scope: "ip:a", Name: "critical"}
	both := []store.RateBucket{{Scope: "ip:a", Name: "global"}, critical}
	var ended <-chan error
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		ctx := t.Context()
		if err := holder(ctx, tx, []store.RateBucket{critical}); err != nil {
			return err
		}
		ended = waitsOnAnotherConnection(ctx, t, db, both, late)
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

// TestMakingBucketsLocksTheExistingOnesToo: a request that makes the buckets it names holds all of them, those that
// already existed as well as those it made, before it goes on to lock them. Otherwise it could hold the new "global"
// while waiting for the old "critical" that a closing request holds, and that request would wait for "global": a
// deadlock between the statements of two requests, which sorting each statement does not prevent.
func TestMakingBucketsLocksTheExistingOnesToo(t *testing.T) {
	t.Parallel()
	db := open(t)
	critical := store.RateBucket{Scope: "ip:a", Name: "critical"}
	both := []store.RateBucket{{Scope: "ip:a", Name: "global"}, critical}
	if err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error { return takeBuckets(ctx, tx, []store.RateBucket{critical}) }); err != nil {
		t.Fatal(err)
	}
	var ended <-chan error
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		ctx := t.Context()
		if err := store.EnsureRateBuckets(ctx, tx, both); err != nil {
			return err
		}
		ended = waitsOnAnotherConnection(ctx, t, db, both, blockBuckets)
		return takeBuckets(ctx, tx, both)
	})
	if err != nil {
		t.Errorf("the request that made the buckets: %v", err)
	}
	if err := <-ended; err != nil {
		t.Errorf("the request that closed them: %v", err)
	}
}
