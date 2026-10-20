package catalog

import "axiom/internal/platform"

// Cost is the exact price, in the schedule's currency, of the usage's priced items, and the items with usage that have
// no price. It never fails for a missing price: billing prices the missing items at its fallback prices and keeps the
// priced part (README.md「上架门槛与缺项」). Reasoning falls back to output and images to their side's ordinary price; a
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
			missing.Add(it)
			continue
		}
		if total, err = total.Add(price.Mul(u.q[it])); err != nil {
			return platform.Exact{}, 0, err
		}
	}
	return total, missing, nil
}

// band is the context tier the prompt length falls in: 0 for the base prices, i+1 above the i-th threshold. A basis
// that names input but not image_in counts the images as input, as it did while images were not split out of input.
func (s Schedule) band(u Usage) int {
	var length int64 // at most NumItems × 10¹⁰
	inputBasis := s.basis.Has(ItemInput)
	whole := inputBasis
	for it := range NumItems {
		switch {
		case s.basis.Has(it), it == ItemImageIn && inputBasis:
			length += u.q[it]
		case InputSide.Has(it) && u.q[it] != 0:
			whole = false
		}
	}
	// A whole-prompt basis reads the physical total directly. A partial basis still counts only its selected items,
	// capped by the physical prompt so cache overlap cannot turn a short prompt into a long-context request.
	if u.hasPhysicalInput {
		length = min(length, u.physicalInput)
		if whole {
			length = u.physicalInput
		}
	}
	return s.bandOf(length)
}

// bandOf is the context tier a prompt of the given length falls in.
func (s Schedule) bandOf(length int64) int {
	band := 0
	for band < len(s.above) && length > s.above[band] {
		band++
	}
	return band
}

// price is the item's price at the service tier in the band. A service tier inherits nothing from another, and
// reasoning and images without their own price fall back to the same tier and band's ordinary side price.
func (s Schedule) price(st ServiceTier, band int, it Item) (platform.UnitPrice, bool) {
	if !s.offered.Has(st) {
		return platform.UnitPrice{}, false
	}
	l := &s.levels[st]
	if p, ok := l.rate(band, it); ok {
		return p, ok
	}
	if it == ItemImageIn {
		return l.rate(band, ItemInput)
	}
	if it == ItemReasoning || it == ItemImageOut {
		return l.rate(band, ItemOutput)
	}
	return platform.UnitPrice{}, false
}

func (l *level) rate(band int, it Item) (platform.UnitPrice, bool) {
	if band > 0 && l.tiers[band-1].priced.Has(it) {
		return l.tiers[band-1].price[it], true
	}
	return l.base.price[it], l.base.priced.Has(it)
}

// BaseRate is the standard tier's price of the item below the first context threshold, for comparing schedules by what
// a typical request pays (routing's price limits and weights); it is not what a long or priority request pays.
func (s Schedule) BaseRate(it Item) (platform.UnitPrice, bool) {
	return s.price(ServiceTierStandard, 0, it)
}

// Ceiling is the highest unit price any of the items has at any of the service tiers, over every context tier a prompt
// whose basis adds up to at most length can fall in, and false when one of them has no price. Billing reserves
// count × Ceiling: the band follows the basis sum, which a split between basis and non-basis items can put anywhere
// up to length, and prices need not rise with the band, so no single band or item bounds the cost.
func (s Schedule) Ceiling(items ItemSet, tiers ServiceTierSet, length int64) (ceiling platform.UnitPrice, ok bool) {
	ceiling, missing := s.Highest(items, tiers, length)
	return ceiling, !missing
}

// Highest is Ceiling that does not stop at a missing price: the highest of the prices the schedule has, and whether any
// price was missing. Routing holds a price limit against the known prices even when some are absent.
func (s Schedule) Highest(items ItemSet, tiers ServiceTierSet, length int64) (highest platform.UnitPrice, missing bool) {
	for st := range numServiceTiers {
		for it := range NumItems {
			if tiers.Has(st) && items.Has(it) {
				var ok bool
				if highest, ok = s.highest(st, it, s.bandOf(length), highest); !ok {
					missing = true
				}
			}
		}
	}
	return highest, missing
}

// highest is the higher of floor and the item's prices at the service tier in bands 0 to last, and whether every band has
// a price.
func (s Schedule) highest(st ServiceTier, it Item, last int, floor platform.UnitPrice) (platform.UnitPrice, bool) {
	complete := true
	for band := range last + 1 {
		p, ok := s.price(st, band, it)
		switch {
		case !ok:
			complete = false
		case p.Cmp(floor) > 0:
			floor = p
		}
	}
	return floor, complete
}
