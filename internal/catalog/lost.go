package catalog

import "errors"

// ErrTierBasisChanged is returned by Lost when two schedules both have context tiers and their thresholds count
// different items: one threshold then means different usage in each, and the schedules cannot be compared.
var ErrTierBasisChanged = errors.New("the context tiers' basis differs between the two schedules")

// PricePoint is one price a schedule has: the item at a service tier, for prompts longer than Above (0 for the first
// band).
type PricePoint struct {
	ServiceTier ServiceTier
	Above       int64
	Item        Item
}

// Lost returns the prices prev has and s does not, for the items and service tiers of r (the standard tier always
// counts), reasoning falling back to output on both sides as Cost does. Replacing a schedule with s must lose none:
// a priority tier's price that only the old schedule had would make those requests unpriced. Missing cannot do this,
// since it folds all of a tier's items into one answer. The bands are cut at the thresholds of both schedules, so in
// each band both prices are constant and comparing them item by item is exact. That needs one meaning of a length,
// so two schedules that both have context tiers must count the same items, or Lost returns ErrTierBasisChanged. A
// schedule without tiers does not use its basis, so it compares with any.
func (s Schedule) Lost(prev Schedule, r Reportable) ([]PricePoint, error) {
	bands, err := comparisonBands(s, prev)
	if err != nil {
		return nil, err
	}
	var lost []PricePoint
	for st := range numServiceTiers {
		if st != ServiceTierStandard && !r.ServiceTiers.Has(st) {
			continue
		}
		for b, above := range bands {
			lost = append(lost, s.lostIn(prev, st, above, bandLength(b, above), r.Items)...)
		}
	}
	return lost, nil
}

// lostIn lists the items prev prices at the service tier for a prompt of the given length that s does not; above is
// the band's lower threshold, which names it.
func (s Schedule) lostIn(prev Schedule, st ServiceTier, above, length int64, items ItemSet) (lost []PricePoint) {
	for it := range NumItems {
		_, had := prev.price(st, prev.bandOf(length), it)
		_, has := s.price(st, s.bandOf(length), it)
		if items.Has(it) && had && !has {
			lost = append(lost, PricePoint{ServiceTier: st, Above: above, Item: it})
		}
	}
	return lost
}
