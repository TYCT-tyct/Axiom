package gateway

import (
	"container/list"
	"context"
	"sync"
)

// waitList is a FIFO of parked waiters. The mutex of the structure that owns it guards it; the list takes no lock.
type waitList struct{ l list.List }

// waiter is one parked goroutine. ready closes when the list grants it; elem is nil once it has left the list.
type waiter struct {
	ready chan struct{}
	owner *waitList
	elem  *list.Element
	key   string
}

func (w *waitList) push(key string) *waiter {
	wt := &waiter{ready: make(chan struct{}), owner: w, key: key}
	wt.elem = w.l.PushBack(wt)
	return wt
}

func (w *waitList) len() int { return w.l.Len() }

// countKey is a scan, not a counter: it runs only while a queue is in use, over a list the caps keep short, and
// there is no second count to drift from the list.
func (w *waitList) countKey(key string) int {
	n := 0
	for e := w.l.Front(); e != nil; e = e.Next() {
		if e.Value.(*waiter).key == key {
			n++
		}
	}
	return n
}

// grant wakes the oldest waiter, or reports false when nobody waits.
func (w *waitList) grant() bool {
	front := w.l.Front()
	if front == nil {
		return false
	}
	wt := w.l.Remove(front).(*waiter)
	wt.elem = nil
	close(wt.ready)
	return true
}

// park blocks until wt is granted or ctx ends; mu is the lock that guards wt's list. It reports whether wt holds a
// grant. False means wt left the list and holds nothing. True is also returned when ctx ended in the instant the
// grant arrived: the caller checks ctx and, if it is dead, gives the grant back so the next waiter gets it.
func (wt *waiter) park(ctx context.Context, mu *sync.Mutex) bool {
	select {
	case <-wt.ready:
		return true
	case <-ctx.Done():
		mu.Lock()
		defer mu.Unlock()
		if wt.elem == nil {
			return true
		}
		wt.owner.l.Remove(wt.elem)
		wt.elem = nil
		return false
	}
}
