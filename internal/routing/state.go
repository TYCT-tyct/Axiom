package routing

import (
	"sync"
	"sync/atomic"
	"time"
)

// State is the shared state of M1, kept in the process: how many requests each endpoint has in flight, which ones were
// throttled lately, and each supplier's retry budget. It lives longer than any snapshot, which holds pointers into it.
// With more core nodes (M2) the numbers are synchronised in the background; nothing here is a hard limit that other
// nodes would have to see to be right (docs/domains/routing.md「容量感知」).
type state struct {
	mu        sync.RWMutex
	endpoints map[EndpointID]*epState
	suppliers map[SupplierID]*cooldown
}

// epState is one endpoint's live numbers.
type epState struct {
	inflight    atomic.Int64
	throttledAt atomic.Int64 // unix nanoseconds of the last 429; 0 when never
	blocked     cooldown
}

// cooldown is independent of snapshots; a late failure may extend a pause, never shorten another failure's pause.
type cooldown struct{ until atomic.Int64 }

func (c *cooldown) block(until time.Time) {
	for {
		old := c.until.Load()
		if until.UnixNano() <= old || c.until.CompareAndSwap(old, until.UnixNano()) {
			return
		}
	}
}

func (c *cooldown) active(now time.Time) bool { return now.UnixNano() < c.until.Load() }

func (s *state) supplier(id SupplierID) *cooldown {
	s.mu.RLock()
	c := s.suppliers[id]
	s.mu.RUnlock()
	if c != nil {
		return c
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c = s.suppliers[id]; c == nil {
		c = new(cooldown)
		s.suppliers[id] = c
	}
	return c
}

func (s *state) of(id EndpointID) *epState {
	s.mu.RLock()
	e, ok := s.endpoints[id]
	s.mu.RUnlock()
	if ok {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok = s.endpoints[id]; !ok {
		e = new(epState)
		s.endpoints[id] = e
	}
	return e
}

// Begin counts a request in flight at the endpoint; End, when the attempt ends. The gateway calls them around an
// attempt, so Plan's view of fullness is the gateway's own count.
func (r *Router) Begin(id EndpointID) { r.state.of(id).inflight.Add(1) }

// End counts an attempt at the endpoint as over.
func (r *Router) End(id EndpointID) { r.state.of(id).inflight.Add(-1) }

// Throttled records a 429 from the endpoint: its weight drops at once and returns to normal over Config.Recovery.
func (r *Router) Throttled(id EndpointID) { r.state.of(id).throttledAt.Store(r.cfg.Now().UnixNano()) }

// Block excludes the failure's endpoint or supplier until its retry time. With no usable time, Recovery is the
// bounded local probe interval. Credential pauses belong to suppliers, which selects and rests individual keys.
func (r *Router) Block(e Entry, scope Scope, after time.Duration) {
	if after <= 0 {
		after = r.cfg.Recovery
	}
	until := r.cfg.Now().Add(after)
	switch scope {
	case ScopeEndpoint:
		r.state.of(e.Endpoint).blocked.block(until)
	case ScopeSupplier:
		r.state.supplier(e.Supplier).block(until)
	case ScopeRequest, ScopeEgress, ScopeCredential, ScopeGroup: // nothing to pause in routing
	}
}

// Blocked rechecks an already reserved entry: another request may have paused it since Plan ran.
func (r *Router) Blocked(e Entry) bool {
	now := r.cfg.Now()
	return r.state.of(e.Endpoint).blocked.active(now) || r.state.supplier(e.Supplier).active(now)
}

// damping is the factor the endpoint's weight is multiplied by: the floor right after a 429, back to 1 after the
// recovery time, linearly.
func (e *epState) damping(now time.Time, recovery time.Duration, floor float64) float64 {
	at := e.throttledAt.Load()
	if at == 0 {
		return 1
	}
	since := now.Sub(time.Unix(0, at))
	if since >= recovery {
		return 1
	}
	if since < 0 {
		since = 0
	}
	return floor + (1-floor)*float64(since)/float64(recovery)
}
