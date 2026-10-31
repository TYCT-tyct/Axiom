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
	// Direct is a call of a format that answers one object rather than generating output: embeddings and rerank. It
	// has no output tokens to reserve, and its server-tool charges are flat per use, not a sampling of the model. Its
	// batch is reserved, estimated and banded at its total, for every price alike: the upstream reports one usage for
	// it, which the settlement bands as one prompt. The context holds each input instead, which routing checks.
	Direct       bool
	Compaction   bool // server-side compaction or context management is on
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

// quoteEntry follows docs/domains/billing.md「预留与结算」: the input of every sampling at its bound, every server tool
// at its most uses, every piece, second and character the request states at its count, and the output, the first
// segment plus the overshoot allowance or else the whole limit, each at the dearest price it can have.
func quoteEntry(d Demand, e Quotable, s Segments, estimateCost bool) (q quote, err error) {
	if err := checkEntry(e, s); err != nil {
		return q, err
	}
	prices := quotePrices{schedule: &e.Price, estimateCost: estimateCost}
	q.tiers = e.Model.ServiceTiers
	window := e.Model.ContextLength
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
	if d.Direct {
		q.outputCap = 0 // a direct call generates nothing, whatever its batch or cap says
	}
	output, allowance, overshoot := q.outputCap, int64(0), int64(0) // overshoot in token-nanoseconds
	if e.Segmentable && !d.Direct {
		q.segment, output = s.Output, min(output, s.Output)
		allowance, overshoot = s.Allowance(e.Model.OutputSpeed)
	}
	// A saturated count understates the quote, so one that reached math.MaxInt64 refuses the entry. Only the first
	// prompt may saturate: the context length caps it anyway.
	if max(samplings, rest, q.outputCap, add(output, allowance), overshoot) == math.MaxInt64 {
		return q, errOverflow
	}
	resample, err := q.priceTokens(d, e, &prices, tiers, first, rest, longest)
	if err != nil {
		return q, err
	}
	if err := q.priceServerTools(d, e, &prices, tiers, uses, searchable, resample, longest); err != nil {
		return q, err
	}
	if err := q.priceCounted(d, &prices, tiers, longest, samplings); err != nil {
		return q, err
	}
	q.allowance = q.output.Mul(allowance)
	q.amount = sum(q.fixed, q.allowance, q.output.Mul(output))
	return q, nil
}

// priceTokens adds every sampling's input and the whole output at their dearest token prices, and returns the input price
// of the samplings after the first, which a search beyond the tool's uses pays too. Token prices are required for a model
// that may report any token item, as every model did before. A counted-only model (a speech or transcription endpoint
// that reports characters or seconds rather than tokens) has no token charge to bound, so its schedule must be quotable
// without token prices; an empty Reports is unknown, not none.
func (q *quote) priceTokens(d Demand, e Quotable, prices *quotePrices, tiers catalog.ServiceTierSet, first, rest, longest int64) (inputPrice, error) {
	if e.Model.Reports != 0 && e.Model.Reports&catalog.TokenItems == 0 {
		return inputPrice{}, nil
	}
	in := d.InputItems | e.Model.Reports&(1<<catalog.ItemCacheRead) // a cache hit is the upstream's, not the request's
	in.Add(catalog.ItemInput)
	if e.Model.OpenAIUsage {
		// OpenAI reports image tokens whether or not the model's spec lists them, and cache writes it was not asked for.
		in |= e.Model.Reports&(1<<catalog.ItemCacheWrite|1<<catalog.ItemAudioIn) | 1<<catalog.ItemImageIn
	}
	firstPrice, ok := prices.inputCeiling(in, tiers, first, e.Model.OpenAIUsage)
	if !ok {
		return inputPrice{}, ErrUnpriced
	}
	q.fixed = firstPrice.Mul(first)
	if d.Direct {
		return inputPrice{}, nil // no output, and nothing samples again
	}
	restPrice, ok := prices.inputCeiling(in, tiers, e.Model.ContextLength, e.Model.OpenAIUsage)
	if !ok {
		return inputPrice{}, ErrUnpriced
	}
	outItems := catalog.ItemSet(1<<catalog.ItemOutput|1<<catalog.ItemReasoning|1<<catalog.ItemImageOut) |
		e.Model.Reports&(1<<catalog.ItemAudioOut)
	outPrice, ok := prices.ceiling(outItems, tiers, longest)
	if !ok {
		return inputPrice{}, ErrUnpriced
	}
	q.output = outPrice
	q.fixed = sum(q.fixed, restPrice.Mul(rest))
	return restPrice, nil
}

// priceCounted adds every piece, second and character the request states at the dearest price it can have: the count
// is the whole request's, so no band above the longest prompt applies, and every sampling may report it, so each is
// covered.
func (q *quote) priceCounted(d Demand, prices *quotePrices, tiers catalog.ServiceTierSet, longest, samplings int64) error {
	for it := range catalog.NumItems {
		n := d.Counts[it]
		if n <= 0 || !catalog.CountedItems.Has(it) {
			continue
		}
		price, ok := prices.ceiling(1<<it, tiers, longest)
		if !ok {
			return ErrUnpriced
		}
		bound := mul(n, samplings)
		if bound == math.MaxInt64 {
			return errOverflow
		}
		q.fixed = sum(q.fixed, price.Mul(bound))
	}
	return nil
}

// checkEntry refuses an entry that data, not the request, makes impossible to quote: a model with no context length, and
// a segmentable entry with no segment size or overshoot to reserve it by.
func checkEntry(e Quotable, s Segments) error {
	if e.Model.ContextLength <= 0 {
		return errors.New("billing: the model has no context length")
	}
	if e.Segmentable && (s.Output <= 0 || s.Overshoot <= 0) {
		return errNoSegments
	}
	return nil
}

// priceServerTools adds every server tool at its most uses to the fixed part and, when the request enables web
// search, prices what a search beyond them costs the meter: its fee and the samplings it adds, rereading the whole
// context at the input price of the later samplings, twice with compaction. A tool's price is the dearest up to the
// context, which a later sampling can fill; a direct call samples nothing, so its band is its batch's length.
func (q *quote) priceServerTools(d Demand, e Quotable, prices *quotePrices, tiers catalog.ServiceTierSet, uses [catalog.NumItems]int64,
	searchable bool, resample inputPrice, longest int64,
) error {
	window := e.Model.ContextLength
	band := window
	if d.Direct {
		band = longest
	}
	for it, n := range uses {
		if n == 0 {
			continue
		}
		price, ok := prices.ceiling(1<<catalog.Item(it), tiers, band)
		if !ok {
			return ErrUnpriced
		}
		q.fixed = sum(q.fixed, price.Mul(n))
	}
	if !searchable {
		return nil
	}
	var ok bool
	if q.search, ok = prices.ceiling(1<<catalog.ItemWebSearch, tiers, band); !ok {
		return ErrUnpriced
	}
	q.searches, q.resample, q.rereads = uses[catalog.ItemWebSearch], resample, window
	if d.Direct {
		// A rerank's search unit is a flat charge per use, not a sampling of the model: only the item is reserved.
		q.resample, q.rereads = inputPrice{}, 0
	}
	if d.Compaction {
		q.rereads = mul(window, 2)
	}
	return nil
}

// quotePrices keeps sale admission strict while native cost estimation can use an incomplete upstream schedule.
type quotePrices struct {
	schedule      *catalog.Schedule
	estimateCost  bool
	fallback      platform.UnitPrice
	fallbackReady bool
}

func (prices *quotePrices) ceiling(items catalog.ItemSet, tiers catalog.ServiceTierSet, length int64) (platform.UnitPrice, bool) {
	highest, missing := prices.schedule.Highest(items, tiers, length)
	if !missing || !prices.estimateCost {
		return highest, !missing
	}
	if !prices.fallbackReady {
		prices.fallback, _ = prices.schedule.Highest(catalog.TokenItems, ^catalog.ServiceTierSet(0), math.MaxInt64)
		prices.fallbackReady = true
	}
	if highest.Cmp(prices.fallback) < 0 {
		highest = prices.fallback
	}
	return highest, true
}

// inputPrice bounds either disjoint input quantities or overlapping cache reads and writes. Keeping two unit
// prices avoids overflowing the unit-price limit when their sum exceeds 1000; their products fit Exact.
type inputPrice struct{ ordinary, read, write platform.UnitPrice }

func (p inputPrice) Mul(n int64) platform.Exact {
	ordinary, cached := p.ordinary.Mul(n), sum(p.read.Mul(n), p.write.Mul(n))
	if cached.Cmp(ordinary) > 0 {
		return cached
	}
	return ordinary
}

// inputCeiling is the dearest input price. An OpenAI call may bill the same cached tokens as a read and as a write, so
// its ceiling keeps both prices to be added; any other call bills its items apart, and the dearest one bounds them.
func (prices *quotePrices) inputCeiling(items catalog.ItemSet, tiers catalog.ServiceTierSet, length int64, overlap bool) (p inputPrice, ok bool) {
	p.ordinary, ok = prices.ceiling(items, tiers, length)
	if !overlap || !ok || !items.Has(catalog.ItemCacheRead) || !items.Has(catalog.ItemCacheWrite) {
		return p, ok
	}
	p.read, _ = prices.ceiling(1<<catalog.ItemCacheRead, tiers, length)
	p.write, _ = prices.ceiling(1<<catalog.ItemCacheWrite, tiers, length)
	return p, true
}

// InputBound returns the most input tokens the first sampling of the request can have, and the most any sampling can
// have, which is what bounds the context tier a charge falls in. Quote prices with them, and routing holds a price limit
// against the same bound, so the two never read a request's length differently.
func InputBound(d Demand, m Model) (first, longest int64) {
	_, samplings := serverToolUses(d, m)
	return inputBound(d, m, samplings)
}

// The interim estimate of the input a charge takes when the upstream reported none (docs/domains/billing.md「用量来源与
// 暂估」): text runs about 4 bytes a token, an image is taken at 1600 tokens and a file at 4096, unless the model's bound
// is smaller. The text bytes are the rule only for a request counted in no tokenizer of its family; one that was counted
// (Demand.TextTokens) takes its count instead.
const (
	InputBytesPerToken   = 4
	estimatedImageTokens = 1600
	estimatedFileTokens  = 4096
)

// InputEstimate is the input tokens of the request as a charge takes them when the upstream reported none: the text at
// its own count when the caller counted it and at InputBytesPerToken otherwise, each image and file at the lesser of
// its bound and the estimate, held to the context except for a direct call, whose batch is charged for every input.
// It never exceeds InputBound's first, which the reservation covers: every part is the lesser of the part's bound and
// something, and a token covers a byte or more, so the text's count never passes its bytes. d.TextBytes is bytes.
func InputEstimate(d Demand, m Model) int64 {
	text := min(d.TextTokens, d.TextBytes) // a token covers a byte or more, and nothing counted keeps the rule below
	if text <= 0 {
		text = ceilDiv(d.TextBytes, InputBytesPerToken) // a token begun is charged
	}
	images := mul(d.Images, min(imageTokens(m), estimatedImageTokens))
	files := mul(d.Documents, min(fileTokens(m), estimatedFileTokens))
	if d.Direct {
		return add(text, add(images, files)) // each input fits the context; the batch's total is what is charged
	}
	return min(m.ContextLength, add(text, add(images, files)))
}

func inputBound(d Demand, m Model, samplings int64) (first, longest int64) {
	window := m.ContextLength
	total := add(d.TextBytes, add(mul(d.Images, imageTokens(m)), mul(d.Documents, fileTokens(m))))
	first = min(window, total)
	if d.Direct {
		// A batch is one charge for every input, and the one length every price of it is banded at: the settlement
		// bands its reported total, which can pass any one input's context.
		return total, total
	}
	searchable := slices.ContainsFunc(d.ServerTools, func(t ServerTool) bool { return t.Item == catalog.ItemWebSearch })
	if samplings > 1 || searchable {
		return first, window // every later sampling, and one a search beyond the quote adds, rereads the whole context
	}
	return first, first
}

// imageTokens bounds one image: the model's most per image, or the whole context when the catalog has none (the model
// spec records no image size yet), the same convention as fileTokens.
func imageTokens(m Model) int64 {
	if m.ImageTokens <= 0 {
		return m.ContextLength
	}
	return m.ImageTokens
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
		uses[t.Item] = add(uses[t.Item], n)
		if !d.Direct { // a direct call's tool charge is per use, without sampling the model again
			samplings = add(samplings, n)
		}
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
