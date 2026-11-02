package main

import (
	"math/big"
	"slices"
	"strings"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
)

// previewEndpointRows is one row per endpoint of a cost-plus proposal: each endpoint's own sale, cost and floor. The
// model-level row would hide exactly the difference endpoint pricing exists for. Endpoints sort by ID, so a page is
// stable across requests.
func previewEndpointRows(id string, snap *catalog.Snapshot, model catalog.ModelID, p billing.Publication, current billing.Sold, audience audience, serving bool) ([]adminapi.PricingPreviewRow, error) {
	m, ok := snap.Model(model)
	if !ok {
		return nil, catalog.ErrModelNotFound
	}
	endpoints := make([]uuid.UUID, 0, len(p.Endpoints))
	for endpoint := range p.Endpoints {
		endpoints = append(endpoints, endpoint)
	}
	slices.SortFunc(endpoints, func(a, b uuid.UUID) int { return a.Compare(b) })
	out := make([]adminapi.PricingPreviewRow, 0, len(endpoints))
	for _, endpoint := range endpoints {
		row, err := previewEndpointRow(id, m.Slug, endpoint, p, current, audience, serving)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// previewEndpointRow is one endpoint's sale, cost, margin and status. When the endpoint is serving, its current
// published sale is what the page shows and the price the margin is computed against.
func previewEndpointRow(id, slug string, endpoint uuid.UUID, p billing.Publication, current billing.Sold, audience audience, serving bool) (adminapi.PricingPreviewRow, error) {
	row := adminapi.PricingPreviewRow{ModelId: id, Slug: slug, Status: adminapi.PricingPreviewRowStatusUnpriced}
	endpointID := endpoint.String()
	row.EndpointId = &endpointID
	sale := displayedEndpointSale(p, current, audience, serving, endpoint)
	var err error
	row.Sale, err = scheduleBody(sale)
	if err != nil {
		return row, err
	}
	if cost, ok := p.Costs[endpoint]; ok {
		body, err := scheduleBody(cost)
		if err != nil {
			return row, err
		}
		row.Cost = body
		row.MarginRatio = marginOf(sale, cost)
	}
	requested, ok := p.RequestedEndpoints[endpoint]
	if !ok {
		requested = sale
	}
	if floor, ok := p.Floors[endpoint]; ok {
		row.Status, err = previewFloorStatusOf(requested, floor)
		if err != nil {
			return row, err
		}
	} else {
		row.Status = adminapi.PricingPreviewRowStatusCostUnknown
	}
	if err := previewCurrent(&row, current, audience, serving, endpoint); err != nil {
		return row, err
	}
	return row, nil
}

// displayedEndpointSale is the sale a serving endpoint shows: its current published price when there is one, else the
// proposal the preview is for.
func displayedEndpointSale(p billing.Publication, current billing.Sold, audience audience, serving bool, endpoint uuid.UUID) catalog.Schedule {
	sale := p.Endpoints[endpoint]
	if !serving {
		return sale
	}
	price := current.For(billing.Audience{Account: audience.account, Tier: audience.tier})
	if price.Version == uuid.Nil() {
		return sale
	}
	if shown, ok := price.PriceFor(endpoint); ok {
		return shown
	}
	return sale
}

// marginBand identifies one effective band of a sale schedule: its service tier and either its base or its threshold.
type marginBand struct {
	tier  catalog.ServiceTier
	base  bool
	above int64
}

func marginBands(views ...catalog.ScheduleView) []marginBand {
	seen := map[marginBand]struct{}{}
	var bands []marginBand
	for _, view := range views {
		for _, r := range view.Rates {
			b := marginBand{tier: r.ServiceTier, base: r.Base, above: r.Above}
			if _, ok := seen[b]; ok {
				continue
			}
			seen[b] = struct{}{}
			bands = append(bands, b)
		}
	}
	return bands
}

func marginBasis(sale, cost catalog.ScheduleView) ([]catalog.Item, bool) {
	switch {
	case len(sale.Basis) == 0:
		return cost.Basis, true
	case len(cost.Basis) == 0:
		return sale.Basis, true
	case slices.Equal(sale.Basis, cost.Basis):
		return sale.Basis, true
	default:
		return nil, false
	}
}

func marginAtBand(sale, cost catalog.Schedule, basis []catalog.Item, b marginBand) (*big.Rat, bool) {
	counts := make(map[catalog.Item]int64, len(basis))
	if !b.base {
		if b.above >= 10_000_000_000 || len(basis) == 0 {
			return nil, false // a context tier above the usage domain cannot be sampled
		}
		// The basis items sum to the prompt length. Put the whole threshold margin on one basis item; filling every
		// item with above+1 would skip the bands between thresholds when the basis has more than one item.
		counts[basis[0]] = b.above + 1
	}
	usage, err := catalog.NewUsage(counts, b.tier, nil)
	if err != nil {
		return nil, false
	}
	var lowest *big.Rat
	for it := range catalog.NumItems {
		price, _, ok := sale.Rate(usage, it)
		if !ok || price.Rat().Sign() == 0 {
			continue
		}
		c, _, ok := cost.Rate(usage, it)
		if !ok {
			return nil, false // an item the sale prices has no cost at this band: the margin is unknown
		}
		margin := new(big.Rat).Sub(price.Rat(), c.Rat())
		margin.Quo(margin, price.Rat())
		if lowest == nil || margin.Cmp(lowest) < 0 {
			lowest = margin
		}
	}
	return lowest, true
}

// marginOf is the lowest margin of sale over cost across the items, service tiers and context bands the sale prices:
// (sale - cost) / sale, as the preview contract defines it. It reads the effective price, including the reasoning and
// image fallbacks and a tier's inherited base rate; a rate whose cost is unknown makes the margin unknown rather than
// a hundred percent, and a zero sale rate is skipped rather than divided by zero.
func marginOf(sale, cost catalog.Schedule) *string {
	saleView, costView := sale.View(), cost.View()
	basis, ok := marginBasis(saleView, costView)
	if !ok {
		return nil // the two schedules count different prompt items, so a shared sample has no meaning
	}
	var lowest *big.Rat
	for _, b := range marginBands(saleView, costView) {
		margin, ok := marginAtBand(sale, cost, basis, b)
		if !ok {
			return nil
		}
		if margin != nil && (lowest == nil || margin.Cmp(lowest) < 0) {
			lowest = margin
		}
	}
	if lowest == nil {
		return nil
	}
	text := strings.TrimRight(strings.TrimRight(lowest.FloatString(15), "0"), ".")
	if text == "" || text == "-" || text == "-0" {
		text = "0"
	}
	return &text
}
