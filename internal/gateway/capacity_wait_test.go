package gateway

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/routing"
)

// credentialCapacity is a live credential view a test switches: full while the flag is set.
type credentialCapacity struct{ full atomic.Bool }

func (c *credentialCapacity) Full() bool { return c.full.Load() }

// TestQueueWaitsForCredentialCapacity: when the credentials of every candidate are taken, the call waits in the queue
// with nothing reserved; it is admitted once capacity returns, and its reservation is written only then.
func TestQueueWaitsForCredentialCapacity(t *testing.T) {
	t.Parallel()
	capacity := new(credentialCapacity)
	r := newRig(t, func(o *rigOptions) {
		o.config = func(c *Config) {
			c.QueueWait, c.AccountGateWait = 30*time.Second, 30*time.Second
		}
		o.endpoints = func(name string, e *routing.Endpoint) {
			if name == "beta-1" {
				e.Capacity = capacity
			}
		}
	})
	a := r.newAccount(1e9)
	held := r.holdCalls()
	var wg sync.WaitGroup
	answers := make([]answer, 2)
	wg.Go(func() { answers[0].resp, answers[0].body = r.message(a, "acme/beta") })
	r.asked(1) // the first call is admitted and held in Run
	capacity.full.Store(true)
	wg.Go(func() { answers[1].resp, answers[1].body = r.message(a, "acme/beta") })
	waitFor(t, "the call to queue", func() bool { return r.limits.Queue.queued() == 1 })
	if calls := r.calls(); len(calls) != 1 {
		t.Errorf("%d calls admitted while the only candidate's credentials are taken", len(calls))
	}
	if pending := r.count(pendingCount); pending != 1 {
		t.Errorf("%d reservations pending while a call waits, want only the held call's", pending)
	}
	capacity.full.Store(false)
	held()                // the held call ends and gives its reservation back
	r.limits.Queue.Wake() // the rig's Run holds no credential slot; this test stands in for the release it means
	wg.Wait()
	for i, an := range answers {
		if an.resp.StatusCode != http.StatusOK {
			t.Errorf("request %d: %d %s", i, an.resp.StatusCode, an.body)
		}
	}
	if queued := r.limits.Queue.queued(); queued != 0 {
		t.Errorf("%d calls still queued", queued)
	}
	r.consistent()
	r.idle(1 << 30)
}

// TestAReleasedCredentialWakesAQueuedCall: giving a credential slot back wakes the oldest queued call, so a request
// that waited for capacity is planned again instead of sleeping until its deadline.
func TestAReleasedCredentialWakesAQueuedCall(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 1})
		epoch := h.exec.Limits.Queue.Epoch()
		woke := make(chan error, 1)
		go func() { woke <- h.exec.Limits.Queue.Wait(ctx, "key", epoch, time.Now().Add(30*time.Second)) }()
		time.Sleep(time.Millisecond) // the bubble's clock runs once the waiter is blocked
		if queued := h.exec.Limits.Queue.queued(); queued != 1 {
			t.Fatalf("%d waiters queued, want the one this test parked", queued)
		}
		h.run()
		select {
		case err := <-woke:
			if err != nil {
				t.Fatalf("the waiter left the queue with %v, want the released slot's wake", err)
			}
		case <-time.After(time.Second):
			t.Fatal("the released credential slot did not wake the queue")
		}
	})
}
