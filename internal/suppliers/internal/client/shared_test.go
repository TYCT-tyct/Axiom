package client_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/client"
	"axiom/internal/suppliers/internal/store"
)

var platform = pg.NewScope(true, nil, nil)

// node is a limiter on a pool of its own, as one node's is, for a supplier at the address "ip:a" with the account given.
func node(t *testing.T, database pgtest.Database, c *clock, specs []adapter.BucketSpec, account string) *client.SharedLimiter {
	t.Helper()
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return client.NewSharedLimiter(db, platform, specs, client.Scopes{Address: "ip:a", Account: account}, c.now)
}

func newDatabase(t *testing.T) pgtest.Database {
	t.Helper()
	return pgtest.New(t, pg.Module{Name: "suppliers", Migrations: store.Migrations()})
}

// TestNodesCountInTheSameBuckets: nodes that take requests at once from the buckets of one address are let through as
// many times as the limit allows in all, not once each; the rest are told when the window ends.
func TestNodesCountInTheSameBuckets(t *testing.T) {
	t.Parallel()
	database, c := newDatabase(t), newClock()
	nodes := []*client.SharedLimiter{
		node(t, database, c, specs, "account:1"), node(t, database, c, specs, "account:2"),
	}
	var taken atomic.Int32
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			var blocked adapter.Blocked
			switch err := nodes[i%2].Take(t.Context(), []string{"global"}); {
			case err == nil:
				taken.Add(1)
			case !errors.As(err, &blocked) || !blocked.Until.Equal(c.t.Add(180*time.Second)):
				t.Errorf("refused with %v, want Blocked until the window's end", err)
			}
		})
	}
	wg.Wait()
	if taken.Load() != 3 {
		t.Errorf("%d requests were let through 2 nodes sharing a bucket of 3, want 3", taken.Load())
	}
	c.advance(180 * time.Second)
	if err := nodes[1].Take(t.Context(), []string{"global"}); err != nil {
		t.Errorf("after the window: %v", err)
	}
}

// TestASharedRequestTakesEveryBucketOrNone: a request counted in two buckets takes from neither when one has no
// allowance, as Limiter does, so a full bucket does not use up the other.
func TestASharedRequestTakesEveryBucketOrNone(t *testing.T) {
	t.Parallel()
	database, c := newDatabase(t), newClock()
	a, b := node(t, database, c, specs, "account:1"), node(t, database, c, specs, "account:1")
	both := []string{"global", "critical"}
	if err := a.Take(t.Context(), both); err != nil {
		t.Fatal(err)
	}
	if until := blockedUntil(t, b.Take(t.Context(), both)); !until.Equal(c.t.Add(1200 * time.Second)) {
		t.Errorf("blocked until %v, want when the critical bucket reopens", until)
	}
	for i := range 2 {
		if err := b.Take(t.Context(), []string{"global"}); err != nil {
			t.Fatalf("global request %d after the refused one: %v: the refused request took from it", i+2, err)
		}
	}
	blockedUntil(t, a.Take(t.Context(), []string{"global"}))
}

// TestBucketsAreCountedPerWhomTheSupplierCounts: two suppliers at one address share the buckets counted by address and
// have buckets of their own where the supplier counts by account.
func TestBucketsAreCountedPerWhomTheSupplierCounts(t *testing.T) {
	t.Parallel()
	database, c := newDatabase(t), newClock()
	specs := []adapter.BucketSpec{
		{Name: "login", Per: adapter.CountedByIP, Window: time.Minute, Limit: 1},
		{Name: "panel", Per: adapter.CountedByAccount, Window: time.Minute, Limit: 1},
	}
	one, two := node(t, database, c, specs, "account:1"), node(t, database, c, specs, "account:2")
	if err := one.Take(t.Context(), []string{"login", "panel"}); err != nil {
		t.Fatal(err)
	}
	blockedUntil(t, two.Take(t.Context(), []string{"login"})) // the address is shared
	if err := two.Take(t.Context(), []string{"panel"}); err != nil {
		t.Errorf("the other account's bucket was used up by the first: %v", err)
	}
	blockedUntil(t, one.Take(t.Context(), []string{"panel"}))
}

// TestABlockOnOneNodeHoldsOnTheOthers: buckets one node closes are closed for every node until the time given, and
// a shorter block does not shorten a longer one.
func TestABlockOnOneNodeHoldsOnTheOthers(t *testing.T) {
	t.Parallel()
	database, c := newDatabase(t), newClock()
	a, b := node(t, database, c, specs, "account:1"), node(t, database, c, specs, "account:1")
	until := c.t.Add(20 * time.Minute)
	if err := a.Block(t.Context(), []string{"global", "critical"}, until); err != nil {
		t.Fatal(err)
	}
	if err := a.Block(t.Context(), []string{"global"}, c.t.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := blockedUntil(t, b.Take(t.Context(), []string{"global"})); !got.Equal(until) {
		t.Errorf("blocked until %v, want %v", got, until)
	}
	c.advance(20 * time.Minute)
	if err := b.Take(t.Context(), []string{"global", "critical"}); err != nil {
		t.Errorf("after the block: %v", err)
	}
}

func TestASharedLimiterRefusesABucketNobodyDeclared(t *testing.T) {
	t.Parallel()
	database, c := newDatabase(t), newClock()
	err := node(t, database, c, specs, "account:1").Take(t.Context(), []string{"typo"})
	if err == nil || errors.Is(err, adapter.ErrBucketsFull) {
		t.Errorf("an undeclared bucket: %v, want an error that is not a full bucket", err)
	}
}
