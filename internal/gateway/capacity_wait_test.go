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
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 1})
		epoch := h.exec.Limits.Queue.Epoch()
		woke := make(chan error, 1)
		go func() {
			_, err := h.exec.Limits.Queue.Wait(ctx, "key", epoch, time.Now().Add(30*time.Second))
			woke <- err
		}()
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

// TestAWakeReachesTheCallThatCanUseIt: the queue is one line for every endpoint, so the oldest waiter may be waiting
// for another endpoint than the one whose slot came back. It passes the wake on, and the call that can use the slot is
// admitted at once rather than at its deadline; the first keeps waiting.
func TestAWakeReachesTheCallThatCanUseIt(t *testing.T) {
	t.Parallel()
	alpha, beta := new(credentialCapacity), new(credentialCapacity)
	alpha.full.Store(true)
	beta.full.Store(true)
	r := newRig(t, func(o *rigOptions) {
		o.config = func(c *Config) {
			c.QueueWait, c.AccountGateWait = 30*time.Second, 30*time.Second
		}
		o.endpoints = func(name string, e *routing.Endpoint) {
			if name == "beta-1" {
				e.Capacity = beta
			} else {
				e.Capacity = alpha
			}
		}
	})
	a := r.newAccount(1e9)
	var wg sync.WaitGroup
	answers := make([]answer, 2)
	wg.Go(func() { answers[0].resp, answers[0].body = r.message(a, "acme/beta") })
	waitFor(t, "the beta call to queue", func() bool { return r.limits.Queue.queued() == 1 })
	wg.Go(func() { answers[1].resp, answers[1].body = r.message(a, "acme/alpha") })
	waitFor(t, "the alpha call to queue behind it", func() bool { return r.limits.Queue.queued() == 2 })

	alpha.full.Store(false)
	start := time.Now()
	r.limits.Queue.Wake() // an alpha slot is given back; the rig's Run holds none, so the test makes the release
	waitFor(t, "the alpha call to be admitted", func() bool { return len(r.calls()) == 1 })
	if waited := time.Since(start); waited >= 30*time.Second {
		t.Errorf("the alpha call was admitted after %v, at its deadline and not on the wake", waited)
	}
	if queued := r.limits.Queue.queued(); queued != 1 {
		t.Errorf("%d calls queued after the alpha call left, want the beta call", queued)
	}

	beta.full.Store(false)
	r.limits.Queue.Wake()
	wg.Wait()
	for i, an := range answers {
		if an.resp.StatusCode != http.StatusOK {
			t.Errorf("request %d: %d %s", i, an.resp.StatusCode, an.body)
		}
	}
	r.consistent()
	r.idle(1 << 30)
}
