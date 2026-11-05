package routing

import (
	"uuid"

	"axiom/internal/platform/jsonx"
)

// Record is how a request was routed: the candidates left out and why, the route chosen, and each attempt's outcome.
// The gateway appends the attempts; observability stores it with the call, and the user and the operations agent read
// it from there.
type Record struct {
	// Preset is the preset the plan routed by, at the version the snapshot compiled, and Overrides what the request's
	// provider object changed on top of it, by member, never by value (docs/domains/routing.md「滚动发布、失败与性能」).
	// Plan fills them; a trace can then say which rules a call was held to, and the version table holds them.
	Preset        PresetID
	PresetVersion int64
	Overrides     Overrides
	Excluded      []Excluded
	Route         []EndpointID
	Attempts      []Attempt
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
	Preset     presetJSON     `json:"preset,omitzero"`
	Overrides  []string       `json:"overrides,omitzero"`
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

// presetJSON is zero, and left out, only for the preset code builds when a router has no preset source (tests): every
// stored preset has a version from 1.
type presetJSON struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

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
	if r.Preset != (PresetID{}) || r.PresetVersion != 0 {
		w.Preset = presetJSON{uuid.UUID(r.Preset).String(), r.PresetVersion}
	}
	w.Overrides = r.Overrides.names()
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

// Overrides is a set of the provider members a request wrote that change what its key's preset allows or how it orders
// the route. Plan records it without allocating; JSON names the members.
type Overrides uint16

// overrideNames names each bit of Overrides, in bit order, by the request member that sets it: data_collection stands
// for zdr too, which asks for the same policies, and verified_only is what the :exacto variant asks.
var overrideNames = [...]string{
	"only", "ignore", "order", "allow_fallbacks", "sort", "sort.partition", "verified_only", "classes", "data_collection",
	"max_price", "preferred_min_throughput", "preferred_max_latency", "quantizations", "require_parameters",
}

// overrides is the members o sets. Each condition is the one Plan reads the member by, so a member written with no
// effect (an empty list, allow_fallbacks true) is not recorded.
func (o *Override) overrides() Overrides {
	var s Overrides
	for i, set := range [len(overrideNames)]bool{
		len(o.OnlyNames) > 0, len(o.ExcludeNames) > 0, len(o.OrderNames) > 0, o.NoFallbacks, o.Sort != nil, o.SortGlobal,
		o.VerifiedOnly, !o.Classes.Empty(), o.Policies != 0, o.MaxPrice.Set != 0,
		o.MinThroughput > 0 || o.MinThroughputPercentiles.Set != 0, o.MaxLatency > 0 || o.MaxLatencyPercentiles.Set != 0,
		len(o.Quantizations) > 0, o.RequireParameters,
	} {
		if set {
			s |= 1 << i
		}
	}
	return s
}

func (s Overrides) names() []string {
	var out []string
	for i, name := range overrideNames {
		if s&(1<<i) != 0 {
			out = append(out, name)
		}
	}
	return out
}
