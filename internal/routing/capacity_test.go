package routing_test

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/routing"
)

// credentialCapacity is a test's live credential view: full while free is false.
type credentialCapacity struct{ free atomic.Bool }

func (c *credentialCapacity) Full() bool { return !c.free.Load() }

// TestFullCredentialsWait: an endpoint whose credentials are all taken is left out of the route like any other full
// one, and when every candidate is full the answer is a wait, so the gateway queues instead of refusing.
func TestFullCredentialsWait(t *testing.T) {
	t.Parallel()
	full := func(v *credentialCapacity) func(*routing.Endpoint) {
		v.free.Store(true)
		return func(e *routing.Endpoint) { e.Capacity = v }
	}
	var a, b, c credentialCapacity
	h := newHarness(t, routing.Config{RetryAfter: 3 * time.Second}, openPreset(),
		endpoint(t, 1, full(&a)), endpoint(t, 2, full(&b)), endpoint(t, 3, full(&c)))
	if got := endpoints(h.plan(input())); len(got) != 3 {
		t.Fatalf("route %v, want every endpoint while all have room", got)
	}
	a.free.Store(false)
	b.free.Store(false)
	c.free.Store(false)
	_, wait, err := h.r.Plan(input())
	if err != nil || wait == nil || wait.RetryAfter != 3*time.Second {
		t.Fatalf("every credential taken: wait %v, error %v", wait, err)
	}
	c.free.Store(true)
	p := h.plan(input())
	if got := endpoints(p); !slices.Equal(got, []routing.EndpointID{ep(3)}) {
		t.Errorf("route %v, want only the endpoint whose credentials have room", got)
	}
	taken := 0
	for _, x := range p.Record.Excluded {
		if x.Reason == routing.Full {
			taken++
		}
	}
	if taken != 2 {
		t.Errorf("%d endpoints are left out for capacity, want 2: %+v", taken, p.Record.Excluded)
	}
}
