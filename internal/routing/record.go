package routing

import (
	"uuid"

	"axiom/internal/platform/jsonx"
)

// Record is how a request was routed: the candidates left out and why, the route chosen, and each attempt's outcome.
// The gateway appends the attempts; observability stores it with the call, and the user and the operations agent read
// it from there.
type Record struct {
	Excluded []Excluded
	Route    []EndpointID
	Attempts []Attempt
	// Unmeasured: the request asked for the fastest endpoint first and none of its candidates is measured, so the route
	// is in Balanced's price-weighted order and says nothing about speed (docs/domains/routing.md「排序的先后」).
	Unmeasured bool
}

// Attempt is one try at an endpoint.
type Attempt struct {
	Endpoint EndpointID
	Failed   bool
	Scope    Scope  // what the failure blamed, when it failed
	Action   Action // what was done next, when it failed
}

// recorded, JSON-ready: the identifiers as text, the reasons by name.
type recordJSON struct {
	Unmeasured bool           `json:"fastest_unmeasured,omitzero"`
	Excluded   []excludedJSON `json:"excluded"`
	// ExcludedMore counts the candidates left out beyond the ones named above; ExcludedReasons counts every one of
	// them by reason. Together they keep the stored route a complete summary without letting openrouter/auto, which
	// weighs every model the key may call, grow the record past the call log's column limit.
	ExcludedMore    int            `json:"excluded_more,omitzero"`
	ExcludedReasons map[string]int `json:"excluded_reasons,omitzero"`
	Route           []string       `json:"route"`
	Attempts        []attemptJSON  `json:"attempts"`
}

// maxRecordedExclusions bounds the individual candidates the stored route names. The reason counts cover all of them;
// the route and the attempts are never omitted, because they explain what served the call.
const maxRecordedExclusions = 16

type excludedJSON struct {
	Endpoint string `json:"endpoint,omitzero"`
	Reason   string `json:"reason"`
}

type attemptJSON struct {
	Endpoint string `json:"endpoint"`
	Failed   bool   `json:"failed,omitzero"`
	Scope    uint8  `json:"scope,omitzero"`
	Action   uint8  `json:"action,omitzero"`
}

// JSON writes the record in its stored form. Its size is bounded by construction: the attempts are capped at
// platform.MaxAttemptSeq, the route at the entries that may be tried, and the excluded candidates at
// maxRecordedExclusions plus one count per reason. The call log's route column is 8 KiB, so no legal request can
// produce a record the database refuses (internal/observability/migrations/00001_call_log.sql).
func (r Record) JSON() ([]byte, error) {
	w := recordJSON{Unmeasured: r.Unmeasured, Excluded: []excludedJSON{}, Route: []string{}, Attempts: []attemptJSON{}}
	reasons := make(map[string]int, len(reasonNames))
	for i, e := range r.Excluded {
		reason := e.Reason.String()
		reasons[reason]++
		if i >= maxRecordedExclusions {
			continue
		}
		x := excludedJSON{Reason: reason}
		if e.Endpoint != (EndpointID{}) {
			x.Endpoint = uuid.UUID(e.Endpoint).String()
		}
		w.Excluded = append(w.Excluded, x)
	}
	if more := len(r.Excluded) - maxRecordedExclusions; more > 0 {
		w.ExcludedMore = more
	}
	if len(reasons) > 0 {
		w.ExcludedReasons = reasons
	}
	for _, id := range r.Route {
		w.Route = append(w.Route, uuid.UUID(id).String())
	}
	for _, a := range r.Attempts {
		w.Attempts = append(w.Attempts, attemptJSON{uuid.UUID(a.Endpoint).String(), a.Failed, uint8(a.Scope), uint8(a.Action)})
	}
	return jsonx.MarshalCanonical(w)
}
