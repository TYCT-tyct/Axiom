package catalog

import "axiom/internal/platform"

// Reportable is what an upstream may report for a model: its billing items and the service tiers other than standard.
// The model data records it and review confirms it (README.md「上架门槛与缺项」).
type Reportable struct {
	Items        ItemSet
	ServiceTiers ServiceTierSet
}

// Missing returns the part of r the schedule cannot price, reasoning falling back to output as Cost does: the items
// the standard tier lacks in any band, and the service tiers the schedule lacks or that lack any of r's items in any
// band. Billing checks its listing threshold with it: a call must never meet an item or tier it cannot price.
func (s Schedule) Missing(r Reportable) Reportable {
	m := Reportable{Items: s.unpriced(ServiceTierStandard, r.Items)}
	for st := ServiceTierStandard + 1; st < numServiceTiers; st++ {
		if r.ServiceTiers.Has(st) && (!s.offered.Has(st) || s.unpriced(st, r.Items) != 0) {
			m.ServiceTiers.Add(st)
		}
	}
	return m
}

// unpriced returns the items that have no price at the service tier in some band. A context tier only adds prices to
// its service tier's base prices, so an item priced below the first threshold is priced in every band.
func (s Schedule) unpriced(st ServiceTier, items ItemSet) (missing ItemSet) {
	for it := range NumItems {
		if _, ok := s.price(st, 0, it); !ok && items.Has(it) {
			missing.Add(it)
		}
	}
	return missing
}

// Fill returns the schedule with each of prices given to every offered service tier that has no base price of its own
// for the item. A context tier only adds prices to its tier's base, so the filled item is priced in every band, and a
// price the schedule sets anywhere is kept. Billing fills the known per-use items with the site's default prices
// (docs/domains/billing.md「价目表缺项」).
func (s Schedule) Fill(prices map[Item]platform.UnitPrice) Schedule {
	for st := range numServiceTiers {
		if !s.offered.Has(st) {
			continue
		}
		base := &s.levels[st].base
		for it, p := range prices {
			if !base.priced.Has(it) {
				base.price[it] = p
				base.priced.Add(it)
			}
		}
	}
	return s
}
