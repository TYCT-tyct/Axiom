package catalog

import "axiom/internal/platform"

// Cost is the exact price, in the schedule's currency, of the usage's priced items, and the items with usage that have
// no price. It never fails for a missing price: billing prices the missing items at its fallback prices and keeps the
// priced part (README.md「上架门槛与缺项」). Only reasoning falls back, to output, the way Gemini prices thinking; a
// cache write without a write price is reported rather than guessed, since guessing the input price would undercharge
// an Anthropic schedule that left its write premium out. It does not allocate: it runs for every attempt.
func (s Schedule) Cost(u Usage) (total platform.Exact, missing ItemSet, err error) {
	band := s.band(u)
	for it := range NumItems {
		if u.q[it] == 0 {
			continue
		}
		price, ok := s.price(u.serviceTier, band, it)
		if !ok {
			missing.add(it)
			continue
		}
		if total, err = total.Add(price.Mul(u.q[it])); err != nil {
			return platform.Exact{}, 0, err
		}
	}
	return total, missing, nil
}

// band is the context tier the prompt length falls in: 0 for the base prices, i+1 above the i-th threshold.
func (s Schedule) band(u Usage) int {
	var length int64 // at most NumItems × 10¹⁰
	for it := range NumItems {
		if s.basis.Has(it) {
			length += u.q[it]
		}
	}
	band := 0
	for band < len(s.above) && length > s.above[band] {
		band++
	}
	return band
}

// price is the item's price at the service tier in the band. A service tier inherits nothing from another, and
// reasoning without a price of its own falls back to the same tier's output price in the same band, the only fallback.
func (s Schedule) price(st ServiceTier, band int, it Item) (platform.UnitPrice, bool) {
	if !s.offered.Has(st) {
		return platform.UnitPrice{}, false
	}
	l := &s.levels[st]
	if p, ok := l.rate(band, it); ok || it != ItemReasoning {
		return p, ok
	}
	return l.rate(band, ItemOutput)
}

func (l *level) rate(band int, it Item) (platform.UnitPrice, bool) {
	if band > 0 && l.tiers[band-1].priced.Has(it) {
		return l.tiers[band-1].price[it], true
	}
	return l.base.price[it], l.base.priced.Has(it)
}
