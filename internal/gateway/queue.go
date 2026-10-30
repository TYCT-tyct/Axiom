package gateway

import (
	"context"
	"sync"
	"time"
)

// Queue holds the calls that found every candidate endpoint full, outside any transaction. It is first in first
// out, bounded in total and per key (so one key cannot fill it), and each released slot wakes one waiter, who then
// redoes the admission step from the start. The waiters wait for different endpoints, so a woken one may find its own
// still full: it passes the wake on (Pass), and a released slot reaches, in turn, every call that was queued when it
// came back, until one is admitted.
type Queue struct {
	mu        sync.Mutex
	max       int
	perKey    int
	waiting   waitList
	unclaimed uint64
}

func newQueue(max, perKey int) *Queue {
	return &Queue{max: max, perKey: perKey}
}

// Epoch counts the slots released while nobody was queued. Read it before looking at the load and pass it to Wait.
func (q *Queue) Epoch() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.unclaimed
}

// relay is the wake a waiter was woken with: what it hands on with Pass when it is not admitted. The zero relay is no
// wake, as the waiter that came back on the epoch has.
type relay uint64

// Wait queues key until a slot is released, and returns nil when the caller should retry, with the relay it was woken
// with. It returns a 429 refusal
// (errQueueFull, errQueueKeyFull or errQueueWait) when the queue or the key's share of it is full or deadline has passed,
// and parent's error when parent ends; in every error case the caller leaves no trace in the queue. The deadline is the caller's, one for all the waits of a
// request: a request woken for a slot that another request then takes waits again, and the limit is on the whole.
//
// epoch closes a lost wake-up: a slot released between the caller's look at the load and its place in the queue
// found nobody to wake, so Wait returns at once instead of sleeping until the wait limit with capacity free. Only
// releases that found the queue empty count, so a newcomer never overtakes the waiters.
func (q *Queue) Wait(parent context.Context, key string, epoch uint64, deadline time.Time) (relay, error) {
	if err := parent.Err(); err != nil {
		return 0, err // before the fast paths: a client that has gone is not told to retry or that the queue is full
	}
	if !time.Now().Before(deadline) {
		return 0, errQueueWait // before the epoch too: a release elsewhere must not let a call whose time is spent retry for ever
	}
	q.mu.Lock()
	if q.unclaimed != epoch {
		q.mu.Unlock()
		return 0, nil
	}
	if q.waiting.len() >= q.max {
		q.mu.Unlock()
		return 0, errQueueFull
	}
	if q.waiting.countKey(key) >= q.perKey {
		q.mu.Unlock()
		return 0, errQueueKeyFull
	}
	wt := q.waiting.push(key)
	q.mu.Unlock()

	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	if !wt.park(ctx, &q.mu) {
		return 0, expired(parent, errQueueWait)
	}
	if ctx.Err() != nil {
		q.Wake() // granted just as it gave up: the slot is still free, so pass the wake on
		return 0, expired(parent, errQueueWait)
	}
	return relay(wt.relay), nil
}

// Wake is called once for every slot released: it wakes the oldest waiter, or records that nobody was waiting.
func (q *Queue) Wake() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.waiting.relay(q.waiting.pushed) {
		q.unclaimed++
	}
}

// Pass hands the wake r on to the oldest waiter queued before r's slot came back, the waker having found no room: the
// slot may be one the next waiter can use. A waiter that queued again since, or came later, is not woken by r, so a
// slot costs at most one admission for each call queued when it was released; when none is left the wake ends, as the
// slot was there for any newcomer's own look at the load.
func (q *Queue) Pass(r relay) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.waiting.relay(uint64(r))
}
