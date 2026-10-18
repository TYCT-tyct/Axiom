// Package billing reserves, meters, extends and settles what a call costs (README.md). This file quotes a
// reservation: what each route entry needs reserved before the upstream is called, and which entries the account can
// afford.
package billing

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"math/rand/v2"
	"slices"
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
	Compaction                 bool         // server-side compaction or context management is on
	InputItems                 catalog.ItemSet
	ServiceTiers               catalog.ServiceTierSet
}

// ServerTool is a server tool and the most uses the request allows it; 0 means the request set none, so the entry's
// model default applies.
type ServerTool struct {
	Item    catalog.Item
	MaxUses int64
}

// Model is the model data a quote needs, filled by the caller from the catalog snapshot.
type Model struct {
	ID                      catalog.ModelID
	ContextLength           int64
	ImageTokens, PageTokens int64                  // the most input tokens one image or one document page can take
	FilePages               int64                  // the most pages one file can have; 0 when no limit is known
	OutputSpeed             int64                  // the fastest output, in tokens per second
	MaxUses                 map[catalog.Item]int64 // each server tool's default most uses
	Reports                 catalog.ItemSet        // the items the upstream may report (catalog.Reportable)
	// ServiceTiers are the tiers the endpoint is offered at. A request that names no tier may be served at any the
	// endpoint offers, but only at those, so the quote needs the schedule to price only those. Empty means unknown, and
	// the request's own tiers all count.
	ServiceTiers catalog.ServiceTierSet
}

// Quotable is one route entry as billing quotes it: its locked price schedule, its model, the output limit that
// applies to it, whether it can be reserved in segments (routing decides that per entry and request), and the endpoint
// it would call, which the reservation keeps as the route a lost request is recorded with.
type Quotable struct {
	Price       catalog.Schedule
	Model       Model
	MaxOutput   int64
	Segmentable bool
	Endpoint    uuid.UUID
}

// Segments are the site's settings for reserving in segments.
type Segments struct {
	Output    int64         // output tokens per segment
	Overshoot time.Duration // the extension wait plus the delays until a cancelled upstream stops billing
}

// Source is what an amount available to a reservation is limited by.
type Source uint8

// The sources, in the order a refusal names the first that runs out.
const (
	SourceBalance     Source = iota // the account's balance less its pending reservations, above its floor
	SourceSiteBalance               // the same for a second-level site's owner
	SourceWorkspace                 // the workspace budget
	SourceKey                       // the key's limit
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
	fixed     platform.Exact     // every sampling's input at its bound and every server tool at its most uses
	allowance platform.Exact     // the overshoot allowance, held until settlement
	amount    platform.Exact     // fixed, the allowance and the first segment's output, or fixed and the whole output
	output    platform.UnitPrice // the dearest price an output or reasoning token can have
	search    platform.UnitPrice // a web search's dearest price, when the request enables web search
	searches  int64              // the web searches fixed covers
	resample  platform.UnitPrice // the input price of the samplings a search beyond them adds
	rereads   int64              // the input tokens those samplings may read: the context, twice with compaction
	outputCap int64              // the most output tokens the upstream may produce
	segment   int64              // output tokens a segment; 0 when the entry is reserved in full
}

var errOverflow = errors.New("billing: the request's bounds exceed what a reservation can count")

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
		eq, err := quoteEntry(d, e, s)
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

func shortSource(amount platform.Exact, avail []Available) int {
	for i, a := range avail {
		if amount.Cmp(a.Amount) > 0 {
			return i
		}
	}
	return -1
}

// quoteEntry follows docs/domains/billing.md「预留与结算」: the input of every sampling at its bound, every server tool
// at its most uses, and the output, the first segment plus the overshoot allowance or else the whole limit, each at
// the dearest price it can have.
func quoteEntry(d Demand, e Quotable, s Segments) (q quote, err error) {
	window := e.Model.ContextLength
	if window <= 0 {
		return q, errors.New("billing: the model has no context length")
	}
	tiers := d.ServiceTiers
	if e.Model.ServiceTiers != 0 {
		tiers &= e.Model.ServiceTiers
	}
	tiers.Add(catalog.ServiceTierStandard)
	uses, samplings := serverToolUses(d, e.Model)
	for _, t := range d.ServerTools {
		if uses[t.Item] == 0 {
			return q, &UnboundedError{t.Item}
		}
	}
	first, longest := inputBound(d, e.Model, samplings)
	searchable := slices.ContainsFunc(d.ServerTools, func(t ServerTool) bool { return t.Item == catalog.ItemWebSearch })
	rest := mul(samplings-1, window)
	q.outputCap = mul(e.MaxOutput, max(d.Choices, 1))
	output, allowance, overshoot := q.outputCap, int64(0), int64(0) // overshoot in token-nanoseconds
	if e.Segmentable {
		overshoot = mul(int64(s.Overshoot), e.Model.OutputSpeed)
		q.segment, output, allowance = s.Output, min(output, s.Output), ceilDiv(overshoot, int64(time.Second))
	}
	// A saturated count understates the quote, so one that reached math.MaxInt64 refuses the entry. Only the first
	// prompt may saturate: the context length caps it anyway.
	if max(samplings, rest, q.outputCap, add(output, allowance), overshoot) == math.MaxInt64 {
		return q, errOverflow
	}
	in := d.InputItems | e.Model.Reports&(1<<catalog.ItemCacheRead) // a cache hit is the upstream's, not the request's
	in.Add(catalog.ItemInput)
	firstPrice, ok1 := e.Price.Ceiling(in, tiers, first)
	restPrice, ok2 := e.Price.Ceiling(in, tiers, window)
	outPrice, ok3 := e.Price.Ceiling(1<<catalog.ItemOutput|1<<catalog.ItemReasoning, tiers, longest)
	if !ok1 || !ok2 || !ok3 {
		return q, ErrUnpriced
	}
	q.output = outPrice
	q.fixed = sum(firstPrice.Mul(first), restPrice.Mul(rest))
	if err := q.priceServerTools(d, e, tiers, uses, searchable, restPrice); err != nil {
		return q, err
	}
	q.allowance = outPrice.Mul(allowance)
	q.amount = sum(q.fixed, q.allowance, outPrice.Mul(output))
	return q, nil
}

// priceServerTools adds every server tool at its most uses to the fixed part and, when the request enables web
// search, prices what a search beyond them costs the meter: its fee and the samplings it adds, rereading the whole
// context at the input price of the later samplings, twice with compaction.
func (q *quote) priceServerTools(d Demand, e Quotable, tiers catalog.ServiceTierSet, uses [catalog.NumItems]int64,
	searchable bool, resample platform.UnitPrice,
) error {
	window := e.Model.ContextLength
	for it, n := range uses {
		if n == 0 {
			continue
		}
		price, ok := e.Price.Ceiling(1<<catalog.Item(it), tiers, window)
		if !ok {
			return ErrUnpriced
		}
		q.fixed = sum(q.fixed, price.Mul(n))
	}
	if !searchable {
		return nil
	}
	var ok bool
	if q.search, ok = e.Price.Ceiling(1<<catalog.ItemWebSearch, tiers, window); !ok {
		return ErrUnpriced
	}
	q.searches, q.resample, q.rereads = uses[catalog.ItemWebSearch], resample, window
	if d.Compaction {
		q.rereads = mul(window, 2)
	}
	return nil
}

// InputBound returns the most input tokens the first sampling of the request can have, and the most any sampling can
// have, which is what bounds the context tier a charge falls in. Quote prices with them, and routing holds a price limit
// against the same bound, so the two never read a request's length differently.
func InputBound(d Demand, m Model) (first, longest int64) {
	_, samplings := serverToolUses(d, m)
	return inputBound(d, m, samplings)
}

func inputBound(d Demand, m Model, samplings int64) (first, longest int64) {
	window := m.ContextLength
	first = min(window, add(d.TextBytes, add(mul(d.Images, m.ImageTokens), mul(d.Documents, fileTokens(m)))))
	searchable := slices.ContainsFunc(d.ServerTools, func(t ServerTool) bool { return t.Item == catalog.ItemWebSearch })
	if samplings > 1 || searchable {
		return first, window // every later sampling, and one a search beyond the quote adds, rereads the whole context
	}
	return first, first
}

// fileTokens bounds one file: Documents counts files, not pages (protocol does not parse PDFs), so a file takes its
// pages at most at the page maximum, and the whole context when the model has no known page limit.
func fileTokens(m Model) int64 {
	if m.FilePages <= 0 || m.PageTokens <= 0 {
		return m.ContextLength
	}
	return mul(m.FilePages, m.PageTokens)
}

// serverToolUses returns each server tool's most uses, the request's or else the model's default, totalled per item so
// a quote adds at most NumItems products however many tools the request lists; and the samplings: one, plus
// one per use, doubled when compaction may summarise and sample again.
func serverToolUses(d Demand, m Model) (uses [catalog.NumItems]int64, samplings int64) {
	samplings = 1
	for _, t := range d.ServerTools {
		n := t.MaxUses
		if n == 0 {
			n = m.MaxUses[t.Item]
		}
		uses[t.Item], samplings = add(uses[t.Item], n), add(samplings, n)
	}
	if d.Compaction {
		samplings = mul(samplings, 2)
	}
	return uses, samplings
}

// sum adds amounts that cannot overflow: each is a price below 2⁶⁰ times a count below 2⁶³, so below 2¹²³, and a quote
// adds at most NumItems + 3 of them, below 2¹²⁷. A failure is a broken bound, so it panics rather than return an
// error nobody can act on.
func sum(amounts ...platform.Exact) (total platform.Exact) {
	for _, a := range amounts {
		var err error
		if total, err = total.Add(a); err != nil {
			panic(err)
		}
	}
	return total
}

// mul and add saturate at math.MaxInt64 rather than wrap; their operands are counts, never negative.
func mul(a, b int64) int64 {
	hi, lo := bits.Mul64(uint64(a), uint64(b)) //nolint:gosec // non-negative counts
	if hi != 0 || lo > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(lo)
}

func add(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func ceilDiv(a, b int64) int64 {
	if a%b != 0 {
		return a/b + 1
	}
	return a / b
}
