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
}

// epState is one endpoint's live numbers.
type epState struct {
	inflight    atomic.Int64
	throttledAt atomic.Int64 // unix nanoseconds of the last 429; 0 when never
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
