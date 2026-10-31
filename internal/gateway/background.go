package gateway

import (
	"context"
	"sync"
	"time"
)

// Outstanding is what must end before a node goes away, on one counter: every request from the moment it passes the
// node's gate until its handler returns, the calls an Executor runs, and the settlements that outlive the request they
// belong to (a lost admission's, SettleOrphan, and a call's whose settlement was not written in time). The Handler and
// the Executor of a node share one, so that zero means done.
//
// The invariant that makes it so: work handed to the background registers (Go) before the count of the request that
// hands it over is released. Go counts in the caller's goroutine, and both hand-offs happen inside the request's
// ServeHTTP, so the count never touches zero between a request and the settlement it leaves behind.
//
// It counts with a lock rather than a sync.WaitGroup: a request may begin while a drain is waiting, when nothing was
// running, which a WaitGroup does not allow. Each settlement holds its call's slots until it is done, so how many
// there can be is bounded by the node's limit.
type Outstanding struct {
	mu      sync.Mutex
	running int
	idle    chan struct{} // closed when running drops to 0; a new one when it rises from it

	stopped  chan struct{} // closed when the retries in the background must give up
	stopOnce sync.Once
	shutdown context.Context    //nolint:containedctx // the node's, which every attempt's context.AfterFunc watches; done at abort
	abort    context.CancelFunc // ends shutdown
	end      context.Context    //nolint:containedctx // the node's final deadline, which every settlement's context watches
	finish   context.CancelFunc // ends end
}

// NewOutstanding makes the counter a node's Handler and Executor share.
func NewOutstanding() *Outstanding {
	idle := make(chan struct{})
	close(idle)
	f := &Outstanding{idle: idle, stopped: make(chan struct{})}
	f.shutdown, f.abort = context.WithCancel(context.Background())
	f.end, f.finish = context.WithCancel(context.Background())
	return f
}

// add counts one more thing running; the returned function ends it.
func (f *Outstanding) add() (done func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running == 0 {
		f.idle = make(chan struct{})
	}
	f.running++
	return sync.OnceFunc(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.running--; f.running == 0 {
			close(f.idle)
		}
	})
}

// Go runs g in the background; a drain waits for it. It counts g before it returns, which is the hand-off rule above.
func (f *Outstanding) Go(g func()) {
	done := f.add()
	go func() {
		defer done()
		g()
	}()
}

func (f *Outstanding) isStopped() bool {
	select {
	case <-f.stopped:
		return true
	default:
		return false
	}
}

// Drain waits for everything in flight to end, on three clocks. New streams are already refused in front of it
// (httpserver.Health.Gate). At ctx's deadline less grace it cancels every attempt (each attempt's context hangs off
// shutdown, cause errShutdown, whether or not its endpoint passes cancellation on), so the calls end and settle. It
// returns when the count is zero or the deadline arrives, and only then stops the background retries and ends the
// settlements' contexts: what has not settled is the cleanup's (README.md「故障矩阵」). The grace comes out of the
// deadline rather than being added after it. A ctx without a deadline waits until it ends, then gives grace more.
// After Drain the executor starts no attempt (408) and writes no settlement.
func (f *Outstanding) Drain(ctx context.Context, grace time.Duration) {
	defer f.finish()
	defer f.stop()
	defer f.abort()
	deadline, ok := ctx.Deadline()
	if !ok {
		if f.wait(ctx.Done()) {
			return
		}
		deadline = time.Now().Add(grace)
	} else if f.waitUntil(deadline.Add(-grace)) {
		return
	}
	f.abort()
	f.waitUntil(deadline)
}

func (f *Outstanding) stop() { f.stopOnce.Do(func() { close(f.stopped) }) }

// wait reports whether nothing was running any more before done was closed.
func (f *Outstanding) wait(done <-chan struct{}) bool {
	for {
		f.mu.Lock()
		idle, running := f.idle, f.running
		f.mu.Unlock()
		if running == 0 {
			return true
		}
		select {
		case <-idle: // something may have started since: look again
		case <-done:
			return false
		}
	}
}

// waitUntil is wait with an absolute deadline; one already past only looks.
func (f *Outstanding) waitUntil(deadline time.Time) bool {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	return f.wait(ctx.Done())
}

// settlementContext is SettleContext for one try of a settlement, also ended at the node's final deadline: a write
// still in progress then must not hold the process past the deadline the drain waited for. One taken after that
// deadline is ended before it returns: AfterFunc on an ended context cancels in its own goroutine, so the try would
// otherwise race it to the account gate and sometimes write after the drain.
func (f *Outstanding) settlementContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := SettleContext(ctx, timeout)
	if f.end.Err() != nil {
		cancel()
		return ctx, cancel
	}
	stop := context.AfterFunc(f.end, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// retry calls try until it succeeds, it fails in a way final says no retry can mend, until has passed, or the
// drain stops the retries; the pause between tries grows from 50ms to 2s. It tries at least once and returns the last
// error.
func (f *Outstanding) retry(until time.Time, final func(error) bool, try func() error) error {
	for pause := 50 * time.Millisecond; ; pause = min(2*pause, 2*time.Second) {
		err := try()
		if err == nil || final(err) || !time.Now().Before(until) || f.isStopped() {
			return err
		}
		select {
		case <-f.stopped:
		case <-time.After(min(pause, time.Until(until))):
		}
	}
}
