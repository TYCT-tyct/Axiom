package gateway

import (
	"context"
	"sync"
	"time"
)

// Queue holds the calls that found every candidate endpoint full, outside any transaction. It is first in first
// out, bounded in total and per key (so one key cannot fill it), and each released slot wakes one waiter, who then
// redoes the admission step from the start.
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

// Wait queues key until a slot is released, and returns nil when the caller should retry. It returns ErrRateLimited
// when the queue or the key's share of it is full or deadline has passed, and parent's error when parent ends; in
// every error case the caller leaves no trace in the queue. The deadline is the caller's, one for all the waits of a
// request: a request woken for a slot that another request then takes waits again, and the limit is on the whole.
//
// epoch closes a lost wake-up: a slot released between the caller's look at the load and its place in the queue
// found nobody to wake, so Wait returns at once instead of sleeping until the wait limit with capacity free. Only
// releases that found the queue empty count, so a newcomer never overtakes the waiters.
func (q *Queue) Wait(parent context.Context, key string, epoch uint64, deadline time.Time) error {
	if err := parent.Err(); err != nil {
		return err // before the fast paths: a client that has gone is not told to retry or that the queue is full
	}
	q.mu.Lock()
	if q.unclaimed != epoch {
		q.mu.Unlock()
		return nil
	}
	if q.waiting.len() >= q.max || q.waiting.countKey(key) >= q.perKey {
		q.mu.Unlock()
		return ErrRateLimited
	}
	wt := q.waiting.push(key)
	q.mu.Unlock()

	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	if !wt.park(ctx, &q.mu) {
		return expired(parent)
	}
	if ctx.Err() != nil {
		q.Wake() // granted just as it gave up: the slot is still free, so pass the wake on
		return expired(parent)
	}
	return nil
}

// Wake is called once for every slot released: it wakes the oldest waiter, or records that nobody was waiting.
func (q *Queue) Wake() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.waiting.grant() {
		q.unclaimed++
	}
}
