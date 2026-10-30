package gateway

import (
	"context"
	"sync"
	"time"
)

// background is what must end before a node goes away: the calls an Executor runs, and the settlements that outlive
// the request they belong to, a lost admission's (SettleOrphan) and a call's whose settlement was not written in time.
// Each holds its call's slots until it is done, so how many there can be is bounded by the node's limit. stop makes
// the settlements give up, leaving what they had to the cleanup.
//
// It counts with a lock rather than a sync.WaitGroup: a call may begin while a drain is waiting, when nothing was
// running, which a WaitGroup does not allow.
type background struct {
	mu      sync.Mutex
	running int
	idle    chan struct{} // closed when running drops to 0; a new one when it rises from it

	stopped  chan struct{}
	stopOnce sync.Once
}

func newBackground() *background {
	idle := make(chan struct{})
	close(idle)
	return &background{idle: idle, stopped: make(chan struct{})}
}

// add counts one more thing running; the returned function ends it.
func (b *background) add() (done func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running == 0 {
		b.idle = make(chan struct{})
	}
	b.running++
	return sync.OnceFunc(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.running--; b.running == 0 {
			close(b.idle)
		}
	})
}

// Go runs f in the background; a drain waits for it.
func (b *background) Go(f func()) {
	done := b.add()
	go func() {
		defer done()
		f()
	}()
}

// pending reports whether anything is running.
func (b *background) pending() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running > 0
}

// idleChan is the channel closed when the background becomes idle; a caller that saw pending true can wait on it and
// look again, as a task that started in between makes a new one.
func (b *background) idleChan() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.idle
}

func (b *background) isStopped() bool {
	select {
	case <-b.stopped:
		return true
	default:
		return false
	}
}

func (b *background) stop() { b.stopOnce.Do(func() { close(b.stopped) }) }

// wait reports whether nothing was running any more before ctx ended.
func (b *background) wait(ctx context.Context) bool {
	for {
		b.mu.Lock()
		idle, running := b.idle, b.running
		b.mu.Unlock()
		if running == 0 {
			return true
		}
		select {
		case <-idle: // something may have started since: look again
		case <-ctx.Done():
			return false
		}
	}
}

// waitUntil is wait with an absolute deadline, for a drain that has one: it reports whether nothing is running before
// the deadline passed.
func (b *background) waitUntil(deadline time.Time) bool {
	for {
		b.mu.Lock()
		idle, running := b.idle, b.running
		b.mu.Unlock()
		if running == 0 {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		timer := time.NewTimer(remaining)
		select {
		case <-idle: // something may have started since: look again
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			return false
		}
	}
}

// drain waits until ctx ends; then it stops what is left and waits for it to give up.
func (b *background) drain(ctx context.Context) {
	if !b.wait(ctx) {
		b.stop()
		b.wait(context.WithoutCancel(ctx))
	}
}

// drainUntil waits for the background until abort, stops what is left and waits for it until deadline. The deadline
// is the node's, not a new grace added after abort, so a slow settlement cannot push the node past it.
func (b *background) drainUntil(abort, deadline time.Time) {
	b.waitUntil(abort)
	b.stop()
	b.waitUntil(deadline)
}

// retry calls try until it succeeds, it fails in a way final says no retry can mend, until has passed, or the
// background is stopped; the pause between tries grows from 50ms to 2s. It tries at least once and returns the last
// error.
func (b *background) retry(until time.Time, final func(error) bool, try func() error) error {
	for pause := 50 * time.Millisecond; ; pause = min(2*pause, 2*time.Second) {
		err := try()
		if err == nil || final(err) || !time.Now().Before(until) || b.isStopped() {
			return err
		}
		select {
		case <-b.stopped:
		case <-time.After(min(pause, time.Until(until))):
		}
	}
}
