package routing

import (
	"uuid"

	"axiom/internal/platform/jsonx"
)

// Record is how a request was routed: the candidates left out and why, the route chosen, and each attempt's outcome.
// The gateway appends the attempts; observability stores it with the call, and the user and the operations agent read
// it from there.
type Record struct {
	BreakerFallback []EndpointID
	Excluded        []Excluded
	Route           []EndpointID
	Attempts        []Attempt
}

// Attempt is one try at an endpoint.
type Attempt struct {
	Endpoint EndpointID
	Egress   string // the egress used, by name
	Failed   bool
	Scope    Scope  // what the failure blamed, when it failed
	Action   Action // what was done next, when it failed
}

// recorded, JSON-ready: the identifiers as text, the reasons by name.
type recordJSON struct {
	BreakerFallback []string       `json:"breaker_open_fallback,omitzero"`
	Excluded        []excludedJSON `json:"excluded"`
	Route           []string       `json:"route"`
	Attempts        []attemptJSON  `json:"attempts"`
}

type excludedJSON struct {
	Endpoint string `json:"endpoint,omitzero"`
	Reason   string `json:"reason"`
}

type attemptJSON struct {
	Endpoint string `json:"endpoint"`
	Egress   string `json:"egress,omitzero"`
	Failed   bool   `json:"failed,omitzero"`
	Scope    uint8  `json:"scope,omitzero"`
	Action   uint8  `json:"action,omitzero"`
}

// JSON writes the record in its stored form.
func (r Record) JSON() ([]byte, error) {
	w := recordJSON{Excluded: []excludedJSON{}, Route: []string{}, Attempts: []attemptJSON{}}
	for _, e := range r.Excluded {
		x := excludedJSON{Reason: e.Reason.String()}
		if e.Endpoint != (EndpointID{}) {
			x.Endpoint = uuid.UUID(e.Endpoint).String()
		}
		w.Excluded = append(w.Excluded, x)
	}
	for _, id := range r.Route {
		w.Route = append(w.Route, uuid.UUID(id).String())
	}
	for _, id := range r.BreakerFallback {
		w.BreakerFallback = append(w.BreakerFallback, uuid.UUID(id).String())
	}
	for _, a := range r.Attempts {
		w.Attempts = append(w.Attempts, attemptJSON{uuid.UUID(a.Endpoint).String(), a.Egress, a.Failed, uint8(a.Scope), uint8(a.Action)})
	}
	return jsonx.MarshalCanonical(w)
}
