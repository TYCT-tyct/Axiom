package gateway

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func wait(ctx context.Context, q *Queue, key string) *pending {
	epoch := q.Epoch()
	return start(func() (func(), error) { return nil, q.Wait(ctx, key, epoch) })
}

func TestQueueWakesOneWaiterPerSlotInArrivalOrder(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		q := newQueue(20*time.Second, 10, 10)
		first := wait(ctx, q, "a")
		first.mustWait(t) // each is parked before the next arrives, which fixes the order
		second := wait(ctx, q, "b")
		second.mustWait(t)
		third := wait(ctx, q, "c")
		third.mustWait(t)

		q.Wake()
		first.mustGranted(t)
		second.mustWait(t)
		third.mustWait(t)
		q.Wake()
		second.mustGranted(t)
		third.mustWait(t)
		q.Wake()
		third.mustGranted(t)
	})
}

func TestQueueCaps(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		q := newQueue(20*time.Second, 3, 2)
		wait(ctx, q, "a").mustWait(t)
		wait(ctx, q, "a").mustWait(t)
		overKey := wait(ctx, q, "a")
		if !overKey.finished() || !errors.Is(overKey.err, ErrRateLimited) {
			t.Fatalf("third waiter of one key: finished=%v err=%v, want ErrRateLimited", overKey.finished(), overKey.err)
		}
		wait(ctx, q, "b").mustWait(t) // another key still fits: the total is 3
		overTotal := wait(ctx, q, "c")
		if !overTotal.finished() || !errors.Is(overTotal.err, ErrRateLimited) {
			t.Fatalf("beyond the total: finished=%v err=%v, want ErrRateLimited", overTotal.finished(), overTotal.err)
		}
	})
}

// A waiter that leaves gives its place and its share back, and the wake it would have had goes to the next waiter.
func TestQueueWaiterThatLeavesLeavesNoTrace(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		q := newQueue(20*time.Second, 2, 1)
		leaveCtx, cancel := context.WithCancel(ctx)
		leaving := wait(leaveCtx, q, "a")
		next := wait(ctx, q, "b")
		leaving.mustWait(t)
		cancel()
		if !leaving.finished() || !errors.Is(leaving.err, context.Canceled) {
			t.Fatalf("cancelled waiter: finished=%v err=%v, want context.Canceled", leaving.finished(), leaving.err)
		}
		again := wait(ctx, q, "a") // key a's share and a place in the total are free again
		again.mustWait(t)
		q.Wake()
		next.mustGranted(t)
		again.mustWait(t)
	})
}

func TestQueueWaitEndsAfterTheLimit(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		q := newQueue(20*time.Second, 2, 2)
		w := wait(ctx, q, "a")
		time.Sleep(20*time.Second - time.Nanosecond)
		w.mustWait(t)
		time.Sleep(time.Nanosecond)
		if !w.finished() || !errors.Is(w.err, ErrRateLimited) {
			t.Fatalf("after the limit: finished=%v err=%v, want ErrRateLimited", w.finished(), w.err)
		}
		if q.waiting.len() != 0 {
			t.Fatal("the timed-out waiter is still queued")
		}
	})
}

// A slot released between the caller's look at the load and its place in the queue must not be lost; one released
// while others wait goes to them and does not let a newcomer overtake.
func TestQueueDoesNotLoseAWakeThatCameEarly(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		q := newQueue(20*time.Second, 5, 5)
		epoch := q.Epoch()
		q.Wake() // nobody waits: recorded
		late := start(func() (func(), error) { return nil, q.Wait(ctx, "a", epoch) })
		late.mustGranted(t)

		queued := wait(ctx, q, "a")
		queued.mustWait(t)
		epoch = q.Epoch()
		q.Wake() // goes to queued
		queued.mustGranted(t)
		newcomer := start(func() (func(), error) { return nil, q.Wait(ctx, "a", epoch) })
		newcomer.mustWait(t)
	})
}

// Waiters that leave while wakes are being handed out must not swallow one: each wake ends as exactly one Wait that
// returns nil, whether it went to a waiter who stayed or to one who was just leaving. (The interleaving is the
// scheduler's, so this is a stress test of that invariant.)
func TestQueueSurvivesCancellationsRacingWakes(t *testing.T) {
	const waiters, wakes = 40, 20
	q := newQueue(time.Hour, waiters, waiters)
	for round := range 100 {
		var woken atomic.Int64
		cancels, wg := parkWaiters(t, q, waiters, &woken)
		go func() {
			for i := 0; i < waiters; i += 2 {
				cancels[i]()
			}
		}()
		for range wakes {
			q.Wake()
		}
		if !eventually(func() bool { return woken.Load() == wakes }) {
			t.Fatalf("round %d: %d of %d wakes ended in a waiter that proceeded: a wake went to one that had left", round, woken.Load(), wakes)
		}
		for _, cancel := range cancels {
			cancel()
		}
		wg.Wait()
	}
}

// parkWaiters starts n waiters of one key, counts those that proceed in woken, and returns when all n are queued. If
// they do not all queue within the watchdog's time, it cancels the waiters and fails with how many queued and how
// many returned.
func parkWaiters(t *testing.T, q *Queue, n int, woken *atomic.Int64) ([]context.CancelFunc, *sync.WaitGroup) {
	t.Helper()
	var wg sync.WaitGroup
	var returned atomic.Int64
	cancels := make([]context.CancelFunc, n)
	for i := range cancels {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		epoch := q.Epoch()
		wg.Go(func() {
			if q.Wait(ctx, "k", epoch) == nil {
				woken.Add(1)
			}
			returned.Add(1)
		})
	}
	if !eventually(func() bool { return q.queued() == n }) {
		queued, back := q.queued(), returned.Load()
		for _, cancel := range cancels {
			cancel()
		}
		wg.Wait()
		t.Fatalf("%d of %d waiters queued and %d returned within the watchdog's time", queued, n, back)
	}
	return cancels, &wg
}

// eventually polls cond for up to 10 seconds. Only a failure waits that long; the outcome never depends on a delay.
func eventually(cond func() bool) bool {
	for deadline := time.Now().Add(10 * time.Second); !cond(); runtime.Gosched() {
		if time.Now().After(deadline) {
			return false
		}
	}
	return true
}

// A client that has already gone gets its own error from every way out of Wait, not a retry (nil) and not a 429.
func TestQueueWaitReportsAnEndedContextBeforeAnythingElse(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		q := newQueue(20*time.Second, 1, 1)
		stale := q.Epoch()
		q.Wake()                      // nobody waits: the epoch moves on, so stale would return nil
		wait(ctx, q, "a").mustWait(t) // fills the queue and the share of key a

		gone, cancel := context.WithCancel(ctx)
		cancel()
		for name, tc := range map[string]struct {
			key   string
			epoch uint64
		}{
			"stale epoch":    {"b", stale},
			"queue full":     {"b", q.Epoch()},
			"key share full": {"a", q.Epoch()},
		} {
			if err := q.Wait(gone, tc.key, tc.epoch); !errors.Is(err, context.Canceled) {
				t.Errorf("%s: Wait = %v, want context.Canceled", name, err)
			}
		}
	})
}
