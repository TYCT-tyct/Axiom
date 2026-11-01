package billing

import (
	"fmt"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// Pricing of one settlement (docs/domains/billing.md「免单规则」「价目表缺项」): the schedule the entry locked, the
// per-use and fallback prices, and the estimate flag a correction later replaces.

type priced struct {
	price             int64
	serverTool        platform.Exact
	cacheDiscount     platform.Exact
	hasServerTool     bool
	estimated, waived bool
	tiers             int16
}

// price applies 免单规则 to the outcome at the locked price of the last attempt's entry. A waivable call is waived, with
// no limit on how many an account may have: the rules are what decides, and nothing else.
func (c *locked) price(last AttemptResult) (priced, error) {
	if last.Result == NotExecuted {
		return priced{tiers: -1}, nil
	}
	s, f, err := c.pricing(last.Entry)
	if err != nil {
		return priced{}, err
	}
	waived := waivable(last)
	p, err := priceSegments(s, f, last.Segments, waived, c.quotes[last.Entry].tiers)
	p.waived = waived
	p.tiers = int16(c.quotes[last.Entry].tiers) //nolint:gosec // a model declares only the four catalog service tiers
	return p, err
}

// waivable: an attempt the platform may not charge for its model usage. One that ended because the client left never
// is, whether or not anything was delivered; the judgement is of the charged attempt, so an attempt that failed first
// and was followed by the client leaving is judged by its failure. One whose content never reached the client is; one
// that did is only when the upstream reported no output and an empty or error finish (the zero-output waiver).
func waivable(last AttemptResult) bool {
	delivered := last.Result == Committed || last.Result == Aggregated
	return last.Finish != FinishCancelled && (!delivered || zeroOutput(last))
}

// deliveredOutputs are the items that show a reply reached the client as billable output: the token outputs and the
// pieces and seconds an image or speech endpoint delivers instead of tokens.
const deliveredOutputs = catalog.OutputSide | 1<<catalog.ItemImageOutPiece | 1<<catalog.ItemAudioOutSecond

// zeroOutput is OpenRouter's zero-completion condition: every sampling reported its output, which with reasoning, audio
// and images, in tokens and in pieces and seconds, adds up to zero, and the finish was empty or an error. An output
// count the upstream never reported is a default zero, not a reported one, so it proves nothing.
func zeroOutput(last AttemptResult) bool {
	var output int64
	for _, s := range last.Segments {
		if !s.Reported.Has(catalog.ItemOutput) {
			return false
		}
		for it := range catalog.NumItems {
			if deliveredOutputs.Has(it) {
				output += s.Usage.Count(it)
			}
		}
	}
	return len(last.Segments) > 0 && output == 0 && (last.Finish == FinishNone || last.Finish == FinishError)
}

// perUseItems are the per-use items a fallback row must price for every currency (parseFallbacks), and the ones the
// listing gate leaves out because admission fills them in (Unpriced). They have a site-wide default price instead of a
// price in every model's schedule.
const perUseItems catalog.ItemSet = 1<<catalog.ItemWebSearch | 1<<catalog.ItemCodeExecution

// waivedPerUseItems are the per-use items charged even when a call is waived (「按次计费的项目照常收费」). A file search
// is one too, but it is priced by the model's schedule rather than a fallback row, so it is not in perUseItems: a
// per-use price is not a site default, and a model sells the tool only with a price the listing gate has checked. A
// piece, second or character is not one: it is output the call delivers, so a waived call does not charge it.
const waivedPerUseItems catalog.ItemSet = perUseItems | 1<<catalog.ItemFileSearch

func chargedItems(perUseOnly bool) catalog.ItemSet {
	if perUseOnly {
		return waivedPerUseItems
	}
	return ^catalog.ItemSet(0)
}

func unknownCharged(tokens bool, charged catalog.ItemSet) bool {
	if tokens {
		return charged&catalog.TokenItems != 0
	}
	return charged&waivedPerUseItems != 0
}

// priceSegments prices each sampling at its own band, sums them exactly and rounds up once; perUseOnly charges only
// the per-use items, still at the band the whole sampling falls in, as the cost of the sampling less its cost without
// them. s is the schedule admission filled with the per-use defaults. Charged items with usage the upstream did not
// report or the schedule cannot price, or any usage protocol could not map, make the charge estimated; usage protocol
// could not map, and an item the schedule prices nowhere, are charged at their fallback prices
// (docs/domains/billing.md「价目表缺项」).
func priceSegments(s catalog.Schedule, f fallbacks, segments []Segment, perUseOnly bool, tiers catalog.ServiceTierSet) (priced, error) {
	var p priced
	total := platform.Exact{}
	charged := chargedItems(perUseOnly)
	var err error
	for _, seg := range segments {
		if total, err = p.addSegment(total, s, f, seg, perUseOnly, tiers, charged); err != nil {
			return priced{}, fmt.Errorf("billing: price the usage: %w", err)
		}
	}
	request := requestChargeOf(s, f, segments, tiers)
	p.estimated = p.estimated || request.fallback
	if total, err = total.Add(request.amount); err != nil {
		return priced{}, fmt.Errorf("billing: price the request: %w", err)
	}
	p.price = ceilAmount(total)
	return p, nil
}

// addSegment prices one sampling and adds it to the total: the usage at its band, then the per-use items the client's
// usage shows, the unknown usage and the items the schedule prices nowhere, each at its fallback, and reports that the
// charge is estimated when any of it was guessed.
func (p *priced) addSegment(total platform.Exact, s catalog.Schedule, f fallbacks, seg Segment, perUseOnly bool, tiers catalog.ServiceTierSet, charged catalog.ItemSet) (platform.Exact, error) {
	cost, missing, err := segmentCost(s, seg.Usage, perUseOnly)
	if undeclaredTier(tiers, seg.Usage.ServiceTier()) {
		cost, missing, err = platform.Exact{}, ^catalog.ItemSet(0), nil
		p.estimated = true
	}
	if err == nil {
		err = p.addExtras(s, f, seg, tiers, perUseOnly)
	}
	if err == nil {
		cost, err = addFallbacks(s, f, cost, seg.Usage.Unknown(), charged)
	}
	if err == nil {
		cost, err = addMissing(s, f, cost, seg.Usage, missing, charged)
	}
	if err != nil {
		return platform.Exact{}, err
	}
	p.estimated = p.estimated || guessed(seg, charged, missing)
	return total.Add(cost)
}

// addExtras computes the two amounts the client's usage shows beyond its token counts: the sale price of the per-use
// items and what cache reads saved against the input price. Both use the same rate a charge item would, including the
// fallback rate of a tier the schedule does not declare.
func (p *priced) addExtras(s catalog.Schedule, f fallbacks, seg Segment, tiers catalog.ServiceTierSet, waived bool) error {
	var err error
	for it := range catalog.NumItems {
		if !waivedPerUseItems.Has(it) {
			continue
		}
		n := seg.Usage.Count(it)
		if n == 0 {
			continue
		}
		p.hasServerTool = true
		if p.serverTool, err = p.serverTool.Add(itemRate(s, f, seg, tiers, it).Mul(n)); err != nil {
			return err
		}
	}
	cached := seg.Usage.Count(catalog.ItemCacheRead)
	if cached == 0 || waived {
		return nil
	}
	input := itemRate(s, f, seg, tiers, catalog.ItemInput)
	cache := itemRate(s, f, seg, tiers, catalog.ItemCacheRead)
	saved, err := input.Mul(cached).Add(cache.Mul(cached).Neg())
	if err != nil || saved.Cmp(platform.Exact{}) <= 0 {
		return err
	}
	p.cacheDiscount, err = p.cacheDiscount.Add(saved)
	return err
}

// itemRate is the rate a charge item uses: the schedule's rate when the tier is declared and priced, else the fallback.
func itemRate(s catalog.Schedule, f fallbacks, seg Segment, tiers catalog.ServiceTierSet, it catalog.Item) platform.UnitPrice {
	rate, _, ok := s.Rate(seg.Usage, it)
	if !ok || undeclaredTier(tiers, seg.Usage.ServiceTier()) {
		rate = missingRate(s, f, it)
	}
	return rate
}

// guessed: the sampling's charge rests on usage the upstream did not report, an item without a price, or usage
// protocol could not map.
func guessed(seg Segment, charged, missing catalog.ItemSet) bool {
	if len(seg.Usage.Unknown()) > 0 {
		return true
	}
	for it := range catalog.NumItems {
		if charged.Has(it) && seg.Usage.Count(it) > 0 && (!seg.Reported.Has(it) || missing.Has(it)) {
			return true
		}
	}
	return false
}

// addFallbacks adds the fallback price of unknown usage within the same charged classes as known items.
func addFallbacks(s catalog.Schedule, f fallbacks, cost platform.Exact, unknown []catalog.UnknownUsage, charged catalog.ItemSet) (platform.Exact, error) {
	for _, u := range unknown {
		price, tokens := unknownCost(s, f, u)
		if !unknownCharged(tokens, charged) {
			continue
		}
		var err error
		if cost, err = cost.Add(price); err != nil {
			return platform.Exact{}, err
		}
	}
	return cost, nil
}

// addMissing adds to cost the fallback price of each charged item the schedule prices nowhere, which Cost leaves out of
// its total rather than failing: an upstream that reports a service tier no price list has, or a price list that leaves
// an item out, is charged at the fallback prices instead of nothing (docs/domains/billing.md「价目表缺项」). A token is
// charged at tokenPrice, the highest price of a token anywhere; a per-use item at its default, the price admission would
// have filled in every tier the schedule offers. Only items with usage are in missing, and charged is what a waiver
// leaves chargeable.
func addMissing(s catalog.Schedule, f fallbacks, cost platform.Exact, u catalog.Usage, missing, charged catalog.ItemSet) (platform.Exact, error) {
	charged &= missing // nothing short of it: the schedule priced what it could and it was all priced
	if charged == 0 {
		return cost, nil
	}
	for it := range catalog.NumItems {
		if !charged.Has(it) {
			continue
		}
		rate := missingRate(s, f, it)
		var err error
		if cost, err = cost.Add(rate.Mul(u.Count(it))); err != nil {
			return platform.Exact{}, err
		}
	}
	return cost, nil
}

// segmentCost is one sampling's cost, of its per-use items alone when perUseOnly, and the items without a price.
func segmentCost(s catalog.Schedule, u catalog.Usage, perUseOnly bool) (platform.Exact, catalog.ItemSet, error) {
	cost, missing, err := s.Cost(u)
	if err != nil || !perUseOnly {
		return cost, missing, err
	}
	rest, err := costWithout(s, u, waivedPerUseItems)
	if err == nil {
		cost, err = cost.Add(rest.Neg())
	}
	return cost, missing, err
}

// costWithout is the cost of u with items left out; no per-use item counts toward the band, so the band stays.
func costWithout(s catalog.Schedule, u catalog.Usage, items catalog.ItemSet) (platform.Exact, error) {
	counts := map[catalog.Item]int64{}
	for it := range catalog.NumItems {
		if !items.Has(it) {
			counts[it] = u.Count(it)
		}
	}
	rest, err := u.Recount(counts, nil)
	if err != nil {
		return platform.Exact{}, err
	}
	cost, _, err := s.Cost(rest)
	return cost, err
}
