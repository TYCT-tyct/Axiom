package catalog

import (
	"errors"
	"fmt"

	"axiom/internal/platform"
)

// ErrUnpriced means the usage has an item the schedule has no price for. Only reasoning falls back, to output, the way
// Gemini prices thinking; a cache write without a write price is an error rather than a guess, since guessing the
// input price would undercharge an Anthropic schedule that left its write premium out (README.md「计费项与用量」).
var ErrUnpriced = errors.New("price schedule: an item with usage has no price")

// Cost is the exact price of the usage in the schedule's currency. It does not allocate: it runs for every attempt.
func (s Schedule) Cost(u Usage) (platform.Exact, error) {
	tier := s.tier(u)
	var total platform.Exact
	for it := range NumItems {
		if u.q[it] == 0 {
			continue
		}
		price, ok := s.price(tier, it)
		if !ok && it == ItemReasoning {
			price, ok = s.price(tier, ItemOutput)
		}
		if !ok {
			return platform.Exact{}, fmt.Errorf("%w: %s", ErrUnpriced, it)
		}
		var err error
		if total, err = total.Add(price.Mul(u.q[it])); err != nil {
			return platform.Exact{}, err
		}
	}
	return total, nil
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
