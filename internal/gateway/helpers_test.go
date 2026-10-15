package gateway

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

// pending is a blocking call running in its own goroutine inside a synctest bubble.
type pending struct {
	done    chan struct{}
	release func()
	err     error
}

func start(f func() (func(), error)) *pending {
	p := &pending{done: make(chan struct{})}
	go func() {
		p.release, p.err = f()
		close(p.done)
	}()
	return p
}

// finished waits until every goroutine of the bubble is blocked, so a call that can still make progress has made it,
// and then reports whether p has returned.
func (p *pending) finished() bool {
	synctest.Wait()
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func countFinished(ps []*pending) (n int) {
	for _, p := range ps {
		if p.finished() {
			n++
		}
	}
	return n
}

func countRefused(ps []*pending) (n int) {
	for _, p := range ps {
		if p.finished() && errors.Is(p.err, ErrRateLimited) {
			n++
		}
	}
	return n
}

func mustAcquire(t *testing.T, f func() (func(), error)) func() {
	t.Helper()
	p := start(f)
	if !p.finished() || p.err != nil {
		t.Fatalf("expected an immediate grant, got finished=%v err=%v", p.finished(), p.err)
	}
	return p.release
}

func (p *pending) mustGranted(t *testing.T) {
	t.Helper()
	if !p.finished() || p.err != nil {
		t.Fatalf("expected a grant, got finished=%v err=%v", p.finished(), p.err)
	}
}

func (p *pending) mustWait(t *testing.T) {
	t.Helper()
	if p.finished() {
		t.Fatalf("expected to keep waiting, returned err=%v", p.err)
	}
}

// run is synctest.Test with a context that ends with the test body, so waiters the body leaves behind do not
// deadlock the bubble.
func run(t *testing.T, body func(t *testing.T, ctx context.Context)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		body(t, ctx)
	})
}

func (q *Queue) queued() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.waiting.len()
}
