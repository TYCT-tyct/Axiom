package billing

import (
	"errors"
	"math"
	"slices"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

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
	if err := boundedServerTools(d, e.Model); err != nil {
		return q, err
	}
	uses, samplings, repeats := serverToolUses(d, e.Model)
	first, longest := inputBound(d, e.Model, samplings)
	searchable := slices.ContainsFunc(d.ServerTools, func(t ServerTool) bool { return t.Item == catalog.ItemWebSearch })
	rest := mul(samplings-1, window)
	q.outputCap = mul(e.MaxOutput, max(d.Choices, 1))
	if d.Direct {
		q.outputCap = 0 // a direct call generates nothing, whatever its batch or cap says
	}
	// Each repeated sampling is a separate upstream call under the output limit: the reservation covers the worst case
	// of every one of them generating it (docs/domains/billing.md「预留与结算」).
	q.outputCap = mul(q.outputCap, add(1, repeats))
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
	if err := q.priceRequest(d.Requests, &prices, tiers, longest); err != nil {
		return q, err
	}
	q.allowance = q.output.Mul(allowance)
	q.amount = sum(q.fixed, q.allowance, q.output.Mul(output))
	return q, nil
}

// priceRequest reserves one request per call at the price settlement charges: the served tier's, or the highest request
// price in the schedule when the eligible tiers price none. A caller with no request to charge, as a probe's estimate,
// reserves nothing.
func (q *quote) priceRequest(requests int64, prices *quotePrices, tiers catalog.ServiceTierSet, longest int64) error {
	if requests == 0 || !prices.schedule.Priced(1<<catalog.ItemRequest) {
		return nil
	}
	price, missing := prices.schedule.Highest(1<<catalog.ItemRequest, tiers, longest)
	if missing {
		// Settlement charges a request missing at the served tier at the highest request price in the schedule, never
		// at a token or another per-use price. Reserve the same amount, so admission and settlement agree when a model
		// prices only some service tiers per request.
		var ok bool
		if price, ok = itemPrice(*prices.schedule, catalog.ItemRequest); !ok {
			return ErrUnpriced
		}
	}
	q.fixed = sum(q.fixed, price.Mul(requests))
	return nil
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
// search, reserves what a search costs the meter (reserveSearch). A tool's price is the dearest up to the context,
// which a later sampling can fill; a direct call samples nothing, so its band is its batch's length.
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
	return q.reserveSearch(d, e, prices, tiers, uses, resample, band, window)
}

// reserveSearch reserves a search beyond the tools: its fee and the samplings it adds, rereading the whole context at
// the input price of the later samplings, twice with compaction. band is the longest input whose price applies and
// window the context every later sampling rereads.
func (q *quote) reserveSearch(d Demand, e Quotable, prices *quotePrices, tiers catalog.ServiceTierSet, uses [catalog.NumItems]int64,
	resample inputPrice, band, window int64,
) error {
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
	flatUses, flat, native := flatSearches(d, e.Model)
	if !flat {
		return nil
	}
	if native {
		// A flat search adds no sampling, so when a native tool shares the item it must not stand in for that
		// tool in q.searches: the meter counts only what the upstream reports, and a native search beyond its
		// bound still has to extend for its sampling.
		q.searches = max(0, q.searches-flatUses)
		return nil
	}
	q.resample = inputPrice{}
	if !d.Compaction {
		q.rereads = 0
	}
	return nil
}

// flatSearches reports whether the request enables a flat web search, and how many of its uses the quote reserves:
// the request's own or else the model's default. native says a native web search tool shares the item.
func flatSearches(d Demand, m Model) (uses int64, flat, native bool) {
	for _, t := range d.ServerTools {
		if t.Item != catalog.ItemWebSearch {
			continue
		}
		if !t.Flat {
			native = true
			continue
		}
		flat = true
		n := t.MaxUses
		if n == 0 {
			n = m.MaxUses[catalog.ItemWebSearch]
		}
		uses += n
	}
	return uses, flat, native
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
