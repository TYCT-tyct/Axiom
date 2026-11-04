// Package billing reserves, meters, extends and settles what a call costs (README.md). This file quotes a
// reservation: what each route entry needs reserved before the upstream is called, and which entries the account can
// afford.
package billing

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// Demand is what the request itself says; what depends on the model is worked out per entry when quoting.
type Demand struct {
	TextBytes, Images, Choices int64
	Documents                  int64        // files, whatever their pages: protocol does not parse them
	ServerTools                []ServerTool // the server tools the request enables, such as web search
	// Direct is a call of a format that answers one object rather than generating output: embeddings and rerank. It
	// has no output tokens to reserve, and its server-tool charges are flat per use, not a sampling of the model. Its
	// batch is reserved, estimated and banded at its total, for every price alike: the upstream reports one usage for
	// it, which the settlement bands as one prompt. The context holds each input instead, which routing checks.
	Direct     bool
	Compaction bool // server-side compaction or context management is on
	// InputBytes is the longest single input of a batch, and 0 for a request that is not one: legacy completions'
	// prompt array is charged, estimated and banded at the sum of its inputs, but each prompt is held to the context
	// on its own (docs/domains/gateway.md「决策与理由」).
	InputBytes   int64
	InputItems   catalog.ItemSet
	ServiceTiers catalog.ServiceTierSet
	// TextTokens is the request's text counted in the tokens of the model family's own tokenizer, when the caller
	// counted it (NewTextCount); 0 keeps the bytes-per-token rule of the estimate. It is an estimate, not a bound:
	// InputBound goes by TextBytes alone.
	TextTokens int64
	// Counts are the request's quantities of the items billed per piece, per second or per character, in each item's
	// own unit: the images to generate, the seconds of audio, the characters to read aloud. A count of a token or
	// per-use item here is ignored, because those are bounded by TextBytes, the output limit and ServerTools.
	Counts map[catalog.Item]int64
	// Requests is how many generation requests this call is: 1 for an ordinary call, and 0 only where there is none to
	// charge, as a probe's cost estimate. It is billed at the per-request item when the schedule prices one.
	Requests int64
}

// ServerTool is a server tool and the most uses the request allows it; 0 means the request set none, so the entry's
// model default applies. Flat says the tool does not sample the model again: it runs once outside the completion, as
// the gateway's web plugin does, so only its per-use fee is reserved, not a second sampling rereading the context.
type ServerTool struct {
	Item    catalog.Item
	MaxUses int64
	Flat    bool
	// Repeats says each use samples the model again as its own upstream call, with its own output limit, as the
	// gateway's web search loop does. A provider's own server tool keeps one output limit for the whole reply, so it
	// leaves this false; a flat tool samples nothing.
	Repeats bool
}

// Model is the model data a quote needs, filled by the caller from the catalog snapshot.
type Model struct {
	ID                      catalog.ModelID
	ContextLength           int64
	ImageTokens, PageTokens int64                  // the most input tokens one image or one document page can take; 0 when not known
	FilePages               int64                  // the most pages one file can have; 0 when no limit is known
	OutputSpeed             int64                  // the fastest output, in tokens per second
	MaxUses                 map[catalog.Item]int64 // each server tool's default most uses
	Reports                 catalog.ItemSet        // the items the upstream may report (catalog.Reportable)
	// OpenAIUsage is set when the entry is called in Chat or Responses, whose usage includes cached tokens in the input
	// total and whose cache reads and writes may overlap and be billed together; a Messages call reports them apart.
	OpenAIUsage bool
	// ServiceTiers are the tiers the endpoint is offered at. A request that names no tier may be served at any the
	// endpoint offers, but only at those, so the quote needs the schedule to price only those. Empty means unknown, and
	// the request's own tiers all count.
	ServiceTiers catalog.ServiceTierSet
}

// Quotable is one route entry as billing quotes it: its locked price schedule, its model, the output limit that
// applies to it, whether it can be reserved in segments (routing decides that per entry and request), and the endpoint
// it would call, which the reservation keeps as the route a lost request is recorded with.
type Quotable struct {
	Version     uuid.UUID
	Exchange    platform.ExchangeRate
	Price       catalog.Schedule
	Model       Model
	MaxOutput   int64
	Segmentable bool
	Endpoint    uuid.UUID
}

// Segments are the settings for reserving in segments, one set per node.
type Segments struct {
	Output    int64         // output tokens per segment
	Overshoot time.Duration // the extension wait plus the delays until a cancelled upstream stops billing
}

// Allowance is the most output tokens a segment's overshoot may add at the given fastest output speed: the time it
// covers, times the speed, rounded up. Its second result is the product before rounding, which a quote checks for
// saturation: a saturated allowance would understate a reservation, and routing treats one as leaving no room to
// segment an entry.
func (s Segments) Allowance(speed int64) (tokens, nanos int64) {
	nanos = mul(int64(s.Overshoot), speed)
	return ceilDiv(nanos, int64(time.Second)), nanos
}

// Source is what an amount available to a reservation is limited by.
type Source uint8

// The sources, in the order a refusal names the first that runs out.
const (
	SourceBalance      Source = iota // the account's balance less its pending reservations, above its floor
	SourceSiteBalance                // the same for a second-level site's owner
	SourceWorkspace                  // the workspace budget
	SourceKey                        // the key's limit
	SourceSubscription               // the subscription window, when the preference does not allow the wallet
)

// Available is the amount one source still allows.
type Available struct {
	Source Source
	Amount platform.Exact
}

// Insufficient is the refusal when the preferred entry's quote, or an extension, exceeds what a source allows.
// Transient is a limit that resets, so the same call may pass later; a short balance waits for a top-up.
type Insufficient struct {
	Source    Source
	Transient bool
}

func (e *Insufficient) Error() string {
	return fmt.Sprintf("billing: source %d cannot cover the reservation", e.Source)
}

// Reservation is the outcome of a quote: the amount to reserve and the entries kept, as indexes into the route.
type Reservation struct {
	Amount  platform.Exact
	Kept    []int
	quotes  []quote // by route index; a dropped entry's is never bound
	trigger int64   // the per mille of a segment at which to extend, drawn once per request from 400 to 600
}

// quote is one entry's reservation in parts, so that a meter builds every later target by addition.
type quote struct {
	tiers     catalog.ServiceTierSet // the model tiers admitted with this live hold; 0 when unknown
	fixed     platform.Exact         // every sampling's input at its bound and every server tool at its most uses
	allowance platform.Exact         // the overshoot allowance, held until settlement
	amount    platform.Exact         // fixed, the allowance and the first segment's output, or fixed and the whole output
	output    platform.UnitPrice     // the dearest price an output or reasoning token can have
	search    platform.UnitPrice     // a web search's dearest price, when the request enables web search
	searches  int64                  // the web searches fixed covers
	resample  inputPrice             // the input price of the samplings a search beyond them adds
	rereads   int64                  // the input tokens those samplings may read: the context, twice with compaction
	outputCap int64                  // the most output tokens the upstream may produce
	segment   int64                  // output tokens a segment; 0 when the entry is reserved in full
}

var errOverflow = errors.New("billing: the request's bounds exceed what a reservation can count")

// errNoSegments is a segmentable entry quoted with no segment size or no overshoot allowance configured: a segment of
// 0 tokens reserves no output at all and the meter reads it as a whole reservation, so the output would have no bound.
// Routing makes no entry segmentable while it has no segment size (routing.Config.FirstSegment); this keeps a
// configuration that gives one to routing and none to billing from quoting an entry short.
var errNoSegments = errors.New("billing: an entry is segmentable but the segment size or the overshoot is not configured")

// ErrUnpriced is a price schedule that leaves something unpriced: what Quote refuses a request for when the locked
// schedule does not price an item the request may use, and what Sell returns for a rule M1 cannot turn into a sale
// price. A model without one has no sale price at all rather than its reference price (docs/domains/billing.md「定价」).
var ErrUnpriced = errors.New("billing: the entry's schedule does not price what the request may use")

// ErrUnbounded is a request that enables a server tool without a limit on its uses, for a model that has no default
// maximum: its cost has no upper bound, so no reservation can cover it, and the request is refused before any upstream
// call (docs/domains/billing.md「预留与结算」). A client states the limit (max_uses, max_tool_calls) and is served.
var ErrUnbounded = errors.New("billing: a server tool is enabled with no limit on its uses")

// UnboundedError is an ErrUnbounded that names the tool, so the gateway can tell the client what to do about it.
type UnboundedError struct{ Item catalog.Item }

func (e *UnboundedError) Error() string { return ErrUnbounded.Error() + ": " + e.Item.String() }

func (*UnboundedError) Unwrap() error { return ErrUnbounded }

// Quote quotes every entry, drops those whose quote exceeds any available amount, and reserves the highest quote
// kept, so a fallback never meets an entry dearer than what was reserved. A dropped preferred entry refuses the
// request with *Insufficient, naming the first source in avail that falls short.
func Quote(d Demand, q []Quotable, s Segments, avail []Available) (Reservation, error) {
	r := Reservation{quotes: make([]quote, len(q)), trigger: 400 + rand.Int64N(201)} //nolint:gosec // G404: spreads extensions over time, not a secret
	for i, e := range q {
		eq, err := quoteEntry(d, e, s, false)
		if err != nil {
			return Reservation{}, fmt.Errorf("entry %d: %w", i, err)
		}
		r.quotes[i] = eq
		amount := eq.amount
		if short := shortSource(amount, avail); short >= 0 {
			if i == 0 {
				return Reservation{}, &Insufficient{Source: avail[short].Source}
			}
			continue
		}
		r.Kept = append(r.Kept, i)
		if amount.Cmp(r.Amount) > 0 {
			r.Amount = amount
		}
	}
	return r, nil
}

// ReservedCost returns a single entry's full-generation bound in its schedule's units. Routing uses a supplier's
// native cost schedule here, so affordability follows Quote's bounds without allocating a reservation or drawing a
// segment trigger. Supplier balances cannot reserve extensions, so this always covers the complete output. Missing
// native prices are estimated at the highest known token rate; this does not alter user admission or settlement.
func ReservedCost(d Demand, e Quotable) (platform.Exact, error) {
	e.Segmentable = false
	q, err := quoteEntry(d, e, Segments{}, true)
	return q.amount, err
}

func shortSource(amount platform.Exact, avail []Available) int {
	for i, a := range avail {
		if amount.Cmp(a.Amount) > 0 {
			return i
		}
	}
	return -1
}
