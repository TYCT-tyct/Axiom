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

// queueWait is the wait limit the tests of the queue use.
const queueWait = 20 * time.Second

func wait(ctx context.Context, q *Queue, key string) *pending {
	epoch, deadline := q.Epoch(), time.Now().Add(queueWait)
	return start(func() (func(), error) { _, err := q.Wait(ctx, key, epoch, deadline); return nil, err })
}

func TestQueueWakesOneWaiterPerSlotInArrivalOrder(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		q := newQueue(10, 10)
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
		q := newQueue(3, 2)
		wait(ctx, q, "a").mustWait(t)
		wait(ctx, q, "a").mustWait(t)
		overKey := wait(ctx, q, "a")
		if !overKey.finished() || !errors.Is(overKey.err, errQueueKeyFull) {
			t.Fatalf("third waiter of one key: finished=%v err=%v, want errQueueKeyFull", overKey.finished(), overKey.err)
		}
		wait(ctx, q, "b").mustWait(t) // another key still fits: the total is 3
		overTotal := wait(ctx, q, "c")
		if !overTotal.finished() || !errors.Is(overTotal.err, errQueueFull) {
			t.Fatalf("beyond the total: finished=%v err=%v, want errQueueFull", overTotal.finished(), overTotal.err)
		}
	})
}

// A waiter that leaves gives its place and its share back, and the wake it would have had goes to the next waiter.
func TestQueueWaiterThatLeavesLeavesNoTrace(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		q := newQueue(2, 1)
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
		q := newQueue(2, 2)
		w := wait(ctx, q, "a")
		time.Sleep(queueWait - time.Nanosecond)
		w.mustWait(t)
		time.Sleep(time.Nanosecond)
		if !w.finished() || !errors.Is(w.err, errQueueWait) {
			t.Fatalf("after the limit: finished=%v err=%v, want errQueueWait", w.finished(), w.err)
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
		q := newQueue(5, 5)
		epoch := q.Epoch()
		q.Wake() // nobody waits: recorded
		late := start(func() (func(), error) { _, err := q.Wait(ctx, "a", epoch, time.Now().Add(queueWait)); return nil, err })
		late.mustGranted(t)

		queued := wait(ctx, q, "a")
		queued.mustWait(t)
		epoch = q.Epoch()
		q.Wake() // goes to queued
		queued.mustGranted(t)
		newcomer := start(func() (func(), error) { _, err := q.Wait(ctx, "a", epoch, time.Now().Add(queueWait)); return nil, err })
		newcomer.mustWait(t)
	})
}

// Waiters that leave while wakes are being handed out must not swallow one: each wake ends as exactly one Wait that
// returns nil, whether it went to a waiter who stayed or to one who was just leaving. (The interleaving is the
// scheduler's, so this is a stress test of that invariant.)
func TestQueueSurvivesCancellationsRacingWakes(t *testing.T) {
	const waiters, wakes = 40, 20
	q := newQueue(waiters, waiters)
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
			if _, err := q.Wait(ctx, "k", epoch, time.Now().Add(time.Hour)); err == nil {
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
		q := newQueue(1, 1)
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
			if _, err := q.Wait(gone, tc.key, tc.epoch, time.Now().Add(queueWait)); !errors.Is(err, context.Canceled) {
				t.Errorf("%s: Wait = %v, want context.Canceled", name, err)
			}
		}
	})
}

// A call whose time is spent is refused 429 whatever else Wait could answer: a release that found nobody waiting makes
// the epoch stale, and that must not send a call past its deadline for another attempt.
func TestQueueRefusesAnExpiredDeadlineWhateverTheEpoch(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		q := newQueue(5, 5)
		stale := q.Epoch()
		q.Wake() // nobody waits: the epoch moves on, so a call with a live deadline is told to retry
		if _, err := q.Wait(ctx, "a", stale, time.Now().Add(queueWait)); err != nil {
			t.Fatalf("with time left and a stale epoch: %v, want a retry", err)
		}
		for name, deadline := range map[string]time.Time{"now": time.Now(), "past": time.Now().Add(-time.Second)} {
			if _, err := q.Wait(ctx, "a", stale, deadline); !errors.Is(err, errQueueWait) {
				t.Errorf("a deadline that is %s, with a stale epoch: %v, want errQueueWait", name, err)
			}
		}
		if q.queued() != 0 {
			t.Errorf("%d still queued", q.queued())
		}
	})
}

// A woken waiter that cannot use the slot passes the wake to the next waiter queued before the slot came back, and to
// nobody who queued after: a slot costs at most one admission for each call that was waiting for it.
func TestQueuePassesAWakeOnOnce(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		q := newQueue(10, 10)
		var woken relay
		wait := func(key string) *pending {
			epoch := q.Epoch()
			return start(func() (func(), error) {
				r, err := q.Wait(ctx, key, epoch, time.Now().Add(queueWait))
				woken = r
				return nil, err
			})
		}
		first := wait("a")
		first.mustWait(t)
		second := wait("b")
		second.mustWait(t)

		q.Wake()
		first.mustGranted(t)
		first = wait("a") // its endpoint is still full: it queues again, behind second
		first.mustWait(t)
		q.Pass(woken)
		second.mustGranted(t)
		second = wait("b") // second cannot use it either
		second.mustWait(t)
		q.Pass(woken)
		first.mustWait(t) // queued after the slot came back: the wake has been to it already
		second.mustWait(t)

		q.Wake() // a new slot starts a new round, oldest first
		first.mustGranted(t)
		second.mustWait(t)
	})
}
