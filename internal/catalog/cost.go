package catalog

import "axiom/internal/platform"

// Cost is the exact price, in the schedule's currency, of the usage's priced items, and the items with usage that have
// no price. It never fails for a missing price: billing prices the missing items at its fallback prices and keeps the
// priced part (README.md「上架门槛与缺项」). Only reasoning falls back, to output, the way Gemini prices thinking; a
// cache write without a write price is reported rather than guessed, since guessing the input price would undercharge
// an Anthropic schedule that left its write premium out. It does not allocate: it runs for every attempt.
func (s Schedule) Cost(u Usage) (total platform.Exact, missing ItemSet, err error) {
	tier := s.tier(u)
	for it := range NumItems {
		if u.q[it] == 0 {
			continue
		}
		price, ok := s.price(tier, it)
		if !ok && it == ItemReasoning {
			price, ok = s.price(tier, ItemOutput)
		}
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

// tier returns the highest tier whose threshold the prompt length exceeds, or nil for the base prices.
func (s Schedule) tier(u Usage) *contextTier {
	var length int64 // at most NumItems × 10¹⁰
	for it := range NumItems {
		if s.basis.Has(it) {
			length += u.q[it]
		}
	}
	for i := len(s.tiers) - 1; i >= 0; i-- {
		if length > s.tiers[i].above {
			return &s.tiers[i]
		}
	}
	return nil
}

func (s Schedule) price(tier *contextTier, it Item) (platform.UnitPrice, bool) {
	if tier != nil && tier.rates.priced.Has(it) {
		return tier.rates.price[it], true
	}
	return s.base.price[it], s.base.priced.Has(it)
}
