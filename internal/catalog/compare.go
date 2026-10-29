package catalog

import (
	"errors"
	"math/big"
	"slices"

	"axiom/internal/platform"
)

// Aligned combines effective prices over both schedules' thresholds. Missing prices stay missing unless the other
// schedule has one. Both schedules must count the same prompt when both have context tiers.
func (s Schedule) Aligned(other Schedule, add bool) (Schedule, error) {
	bands, err := comparisonBands(s, other)
	if err != nil {
		return Schedule{}, err
	}
	if s.currency != other.currency {
		return Schedule{}, errors.New("price schedules have different currencies")
	}
	out := Schedule{currency: s.currency, basis: s.basis, offered: s.offered | other.offered}
	if len(s.above) == 0 {
		out.basis = other.basis
	}
	out.above = append([]int64(nil), bands[1:]...)
	for st := range numServiceTiers {
		if !out.offered.Has(st) {
			continue
		}
		l, err := s.combinedLevel(other, st, bands, add)
		if err != nil {
			return Schedule{}, err
		}
		out.levels[st] = l
	}
	return out, nil
}

// PriceChange reports whether any effective unit price changes by more than ratio of its old value. Added or lost
// prices require confirmation too; zero to nonzero is always a large move.
func (s Schedule) PriceChange(next Schedule, ratio *big.Rat) (bool, error) {
	return s.priceChange(next, ratio, false)
}

// PriceIncrease applies the automatic floor cap only to upward changes.
func (s Schedule) PriceIncrease(next Schedule, ratio *big.Rat) (bool, error) {
	return s.priceChange(next, ratio, true)
}

func (s Schedule) priceChange(next Schedule, ratio *big.Rat, increaseOnly bool) (bool, error) {
	bands, err := comparisonBands(s, next)
	if err != nil {
		return false, err
	}
	for st := range numServiceTiers {
		for b, above := range bands {
			length := above
			if b > 0 {
				length++
			}
			if s.changedRates(next, st, length, ratio, increaseOnly) {
				return true, nil
			}
		}
	}
	return false, nil
}

// CoversCost checks each known cost, including fallback prices, across every interval. An unknown cost is never
// inferred to be zero; callers decide whether an endpoint with incomplete cost evidence can be sold.
func (s Schedule) CoversCost(cost Schedule) (bool, error) {
	bands, err := comparisonBands(s, cost)
	if err != nil {
		return false, err
	}
	if s.currency != cost.currency {
		return false, errors.New("price schedules have different currencies")
	}
	for st := range numServiceTiers {
		for b, above := range bands {
			length := above
			if b > 0 {
				length++
			}
			if !s.coversBand(cost, st, length) {
				return false, nil
			}
		}
	}
	return true, nil
}

func comparisonBands(a, b Schedule) ([]int64, error) {
	if len(a.above) > 0 && len(b.above) > 0 && a.basis != b.basis {
		return nil, ErrTierBasisChanged
	}
	thresholds := slices.Concat(a.above, b.above)
	slices.Sort(thresholds)
	thresholds = slices.Compact(thresholds)
	// The first element names length zero; later elements name strict thresholds, including a possible threshold zero.
	return append([]int64{0}, thresholds...), nil
}

// AddMarkup adds a flat per-item standard price to every offered service tier. Its item fallback follows Cost, so a
// markup on output also marks up reasoning and output images unless it explicitly gives them separate prices.
func (s Schedule) AddMarkup(flat Schedule) (Schedule, error) {
	markup := flat
	markup.offered = s.offered
	for st := range numServiceTiers {
		if s.offered.Has(st) {
			markup.levels[st] = flat.levels[ServiceTierStandard]
		}
	}
	return s.Aligned(markup, true)
}

func (s Schedule) combinedRates(other Schedule, st ServiceTier, length int64, add bool) (rates, error) {
	r := rates{}
	for it := range NumItems {
		p, has := s.price(st, s.bandOf(length), it)
		q, known := other.price(st, other.bandOf(length), it)
		if !has && !known {
			continue
		}
		if add {
			sum := new(big.Rat).Add(p.Rat(), q.Rat())
			var err error
			p, err = platform.ParseUnitPrice(sum.FloatString(15))
			if err != nil {
				return rates{}, err
			}
		} else if q.Cmp(p) > 0 {
			p = q
		}
		r.priced.Add(it)
		r.price[it] = p
	}
	return r, nil
}

func (s Schedule) changedRates(next Schedule, st ServiceTier, length int64, ratio *big.Rat, increaseOnly bool) bool {
	for it := range NumItems {
		old, had := s.price(st, s.bandOf(length), it)
		now, has := next.price(st, next.bandOf(length), it)
		if !increaseOnly && had != has {
			return true
		}
		diff := new(big.Rat).Sub(now.Rat(), old.Rat())
		allowed := new(big.Rat).Mul(old.Rat(), ratio)
		if !increaseOnly {
			diff.Abs(diff)
		}
		if diff.Cmp(allowed) > 0 {
			return true
		}
	}
	return false
}

func (s Schedule) coversBand(cost Schedule, st ServiceTier, length int64) bool {
	for it := range NumItems {
		c, known := cost.price(st, cost.bandOf(length), it)
		p, has := s.price(st, s.bandOf(length), it)
		if known && (!has || p.Cmp(c) < 0) {
			return false
		}
	}
	return true
}

func (s Schedule) combinedLevel(other Schedule, st ServiceTier, bands []int64, add bool) (level, error) {
	out := level{tiers: make([]rates, len(bands)-1)}
	for b, above := range bands {
		length := above
		if b > 0 {
			length++
		}
		r, err := s.combinedRates(other, st, length, add)
		if err != nil {
			return level{}, err
		}
		if b == 0 {
			out.base = r
		} else {
			out.tiers[b-1] = r
		}
	}
	return out, nil
}
