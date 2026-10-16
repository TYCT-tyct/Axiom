// Package routing decides which endpoint serves a request and which ones come next if it fails: the session key, the
// plan of at most three entries, and whether a failed attempt moves to the next entry. It only reads memory (the
// snapshot, the request summary) and the in-process state of M1 (load, throttling, retry budget, stickiness).
// internal/routing/README.md has the design.
package routing

import (
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// The identifiers routing is given. suppliers and accounts will export their own; until they do these are the
// narrowest types routing can use, and the conversion is one line at the call site.
type (
	// EndpointID identifies a supplier's group × model.
	EndpointID uuid.UUID
	// SupplierID identifies a supplier.
	SupplierID uuid.UUID
	// PresetID identifies a routing preset.
	PresetID uuid.UUID
	// AccountID identifies an account.
	AccountID uuid.UUID
	// SiteID identifies a site.
	SiteID uuid.UUID
)

// Admission is the part of accounts' admission that routing reads: who asks, which preset applies, and which models
// the key may use. Allowed is nil when the key may use every model.
type Admission struct {
	Account AccountID
	Site    SiteID
	Preset  PresetID
	Allowed map[catalog.ModelID]struct{}
}

func (a Admission) allows(m catalog.ModelID) bool {
	_, ok := a.Allowed[m]
	return a.Allowed == nil || ok
}

// Input is one request to plan. Models are the resolved model and, after it, the alternates the request listed or a
// preset allows, in order. NativeOnly is set when the request uses something only its own format can express (an
// Anthropic beta, a reasoning signature). Sticky is the endpoint the session stuck to, zero if none.
type Input struct {
	Admission  Admission
	Req        *protocol.Request
	Models     []catalog.ModelID
	Override   Override
	Sticky     EndpointID
	NativeOnly bool
}

// Plan is the route of a request: at most MaxEntries entries in order, the most attempts to make (a credential or
// egress change within an entry counts), and the time after which no new attempt starts. Record is why candidates
// were left out. The gateway may shorten the plan after reserving, never lengthen it.
type Plan struct {
	Entries     []Entry
	MaxAttempts int
	StartBy     time.Time
	Record      Record
}

// MaxEntries is the longest route.
const MaxEntries = 3

// Entry is one endpoint to try, with what billing needs to quote it: the price version it will be charged under, the
// output limit that applies to it, and whether its reservation can be made in segments.
type Entry struct {
	Endpoint    EndpointID
	Model       catalog.ModelID
	Price       PriceRef
	MaxOutput   int64
	Segmentable bool
}

// PriceRef is a price version of billing: its identifier and the schedule it locks.
type PriceRef struct {
	Version  uuid.UUID
	Schedule catalog.Schedule
}

func (p PriceRef) none() bool { return p.Version == (uuid.UUID{}) }

// Wait is returned when every candidate that passes the filters is full: the gateway rolls back the admission,
// queues outside the transaction, and plans again when woken.
type Wait struct{ RetryAfter time.Duration }

// NoEndpoint is returned when nothing can serve the request. Permanent is set when no waiting would help (the
// capabilities, the context, the format, the preset or the override, a missing price): the gateway answers 404;
// otherwise (breakers open, supplier balance, no egress) 503.
type NoEndpoint struct {
	Reasons   []Excluded
	Permanent bool
}

func (e *NoEndpoint) Error() string {
	kind := "temporarily"
	if e.Permanent {
		kind = "permanently"
	}
	return fmt.Sprintf("no endpoint is available %s (%d candidates left out)", kind, len(e.Reasons))
}

// ErrStale is returned when the snapshot is missing or older than the staleness limit; the gateway answers 503.
var ErrStale = errors.New("the routing snapshot is missing or stale")

// ErrUnknownPreset is a preset the snapshot does not hold: admission named a preset the snapshot was built without.
var ErrUnknownPreset = errors.New("the admission's preset is not in the routing snapshot")

// Reason is why a candidate was left out.
type Reason uint8

// The reasons. Permanent ones do not heal by waiting.
const (
	NoCandidates Reason = iota + 1 // nothing serves the model at all
	NotAllowed                     // the key may not use the model
	Disabled                       // the endpoint is switched off
	BreakerOpen
	SupplierBalance
	MissingCapability
	ContextTooSmall
	FormatNotServed
	ServiceTierNotServed // the request may only be served at tiers the endpoint does not offer
	PresetFilter
	OverrideFilter
	NoPrice
	NoEgress
	RetryBudget // a later entry at a supplier whose retry budget is used up
	Full        // not a refusal: the endpoint would do if it had room
)

var reasonNames = [...]string{
	NoCandidates: "no_candidates", NotAllowed: "model_not_allowed", Disabled: "disabled", BreakerOpen: "breaker_open",
	SupplierBalance: "supplier_balance", MissingCapability: "missing_capability", ContextTooSmall: "context_too_small",
	FormatNotServed: "format_not_served", ServiceTierNotServed: "service_tier_not_served", PresetFilter: "preset_filter", OverrideFilter: "override_filter",
	NoPrice: "no_price", NoEgress: "no_egress", RetryBudget: "retry_budget", Full: "full",
}

func (r Reason) String() string { return reasonNames[r] }

// Permanent reports whether the reason is one that waiting does not cure.
func (r Reason) Permanent() bool {
	switch r {
	case NoCandidates, NotAllowed, Disabled, MissingCapability, ContextTooSmall, FormatNotServed, ServiceTierNotServed,
		PresetFilter, OverrideFilter, NoPrice:
		return true
	case BreakerOpen, SupplierBalance, NoEgress, RetryBudget, Full:
		return false
	}
	return false
}

// Excluded is a candidate left out, and why. Endpoint is zero for NoCandidates and NotAllowed.
type Excluded struct {
	Endpoint EndpointID
	Reason   Reason
}
