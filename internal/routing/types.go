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

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
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
	Tier    accounts.TierID
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
// Anthropic beta, a reasoning signature). NoSegments is set when the gateway reads the answer whole instead of as a
// stream, as for a call the upstream answers whole: its reservation is made in full (gateway README.md「Relay 的所有权」)
// and the endpoint need not stream one. Sticky is the endpoint the session stuck to, zero if none. FileRef is set when
// the request refers to a file by an id an upstream issued: such a request gets one attempt, to one endpoint, because
// the id names a resource of that endpoint's supplier account and nowhere else (protocol README.md「跨格式转换」).
type Input struct {
	Admission  Admission
	Req        *protocol.Request
	Models     []catalog.ModelID
	Auto       bool // rank every model in the snapshot: the request named openrouter/auto
	Free       bool // rank only the free models in the snapshot: the request named openrouter/free
	Override   Override
	Sticky     EndpointID
	NativeOnly bool
	NoSegments bool
	FileRef    bool
}

// Plan is the route of a request: at most MaxEntries entries in order, the most attempts to make (a credential or
// egress change within an entry counts), and the time after which no new attempt starts. Record is why candidates
// were left out. The gateway may shorten the plan after reserving, never lengthen it.
type Plan struct {
	Entries     []Entry
	Segments    billing.Segments // the segment settings this plan used, frozen for the reservation that follows it
	MaxAttempts int
	StartBy     time.Time
	StickyTTL   time.Duration // how long the session sticks to the endpoint that serves it; the preset's, 0 for the router's default
	Record      Record
}

// MaxEntries is the longest route.
const MaxEntries = 3

// Entry is one endpoint to try, with what billing needs to quote it: the price version it will be charged under, the
// output limit that applies to it, whether its reservation can be made in segments, and the model's limits at this
// endpoint, the same ones the route was filtered by; and what the attempt needs to make the call: the supplier, whose
// retry budget it spends, and how the endpoint is called for this request (Target). The output limit is one value for
// both: a request that sets no cap is sent with Target.MaxOutput, which is MaxOutput, so what is quoted is what the
// upstream may produce (I1).
type Entry struct {
	BreakerFallback bool
	Exchange        platform.ExchangeRate
	Endpoint        EndpointID
	Supplier        SupplierID
	Target          protocol.Target
	Model           catalog.ModelID
	ModelSlug       string
	Price           PriceRef
	Limits          billing.Model
	MaxOutput       int64
	Segmentable     bool
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
	NotVerified // the request wants only endpoints verified as the model they claim (:exacto)
	NoPrice
	NoEgress
	RetryBudget    // a later entry at a supplier whose retry budget is used up
	Full           // not a refusal: the endpoint would do if it had room
	UnknownItem    // billing met a usage field on the model that it cannot price, until an admin prices it
	CoolingDown    // the endpoint or its supplier is paused until its retry time, or this request already skipped the supplier after a supplier-scope failure
	UnquotableCost // native cost cannot be bounded due to model data or count overflow
)

var reasonNames = [...]string{
	NoCandidates: "no_candidates", NotAllowed: "model_not_allowed", Disabled: "disabled", BreakerOpen: "breaker_open",
	SupplierBalance: "supplier_balance", MissingCapability: "missing_capability", ContextTooSmall: "context_too_small",
	FormatNotServed: "format_not_served", ServiceTierNotServed: "service_tier_not_served", PresetFilter: "preset_filter", OverrideFilter: "override_filter",
	NotVerified: "not_verified",
	NoPrice:     "no_price", NoEgress: "no_egress", RetryBudget: "retry_budget", Full: "full",
	UnknownItem: "unknown_item", CoolingDown: "cooling_down", UnquotableCost: "unquotable_cost",
}

func (r Reason) String() string { return reasonNames[r] }

// Permanent reports whether the reason is one that waiting does not cure.
func (r Reason) Permanent() bool {
	switch r {
	case NoCandidates, NotAllowed, Disabled, MissingCapability, ContextTooSmall, FormatNotServed, ServiceTierNotServed,
		PresetFilter, OverrideFilter, NoPrice, UnknownItem, UnquotableCost:
		return true
	case BreakerOpen, SupplierBalance, NoEgress, NotVerified, RetryBudget, Full, CoolingDown:
		return false
	}
	return false
}

// Excluded is a candidate left out, and why. Endpoint is zero for NoCandidates and NotAllowed.
type Excluded struct {
	Endpoint EndpointID
	Reason   Reason
}
