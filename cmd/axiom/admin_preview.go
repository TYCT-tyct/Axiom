package main

import (
	"math/big"
	"slices"
	"strings"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
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

// previewEndpointRow is one endpoint's sale, cost, margin and status, before previewCurrent may replace the sale with
// the current published one.
func previewEndpointRow(id, slug string, endpoint uuid.UUID, p billing.Publication, current billing.Sold, audience audience, serving bool) (adminapi.PricingPreviewRow, error) {
	row := adminapi.PricingPreviewRow{ModelId: id, Slug: slug, Status: adminapi.PricingPreviewRowStatusUnpriced}
	endpointID := endpoint.String()
	row.EndpointId = &endpointID
	sale := p.Endpoints[endpoint]
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

// marginOf is the lowest margin of sale over cost across the items, service tiers and context bands both schedules
// price: (sale - cost) / sale, as the preview contract defines it. A rate the sale prices and the cost does not is a
// hundred percent margin, and a zero sale rate is skipped rather than divided by zero.
func marginOf(sale, cost catalog.Schedule) *string {
	type key struct {
		tier  catalog.ServiceTier
		above int64
		item  catalog.Item
	}
	costs := map[key]platform.UnitPrice{}
	for _, r := range cost.View().Rates {
		costs[key{r.ServiceTier, r.Above, r.Item}] = r.Price
	}
	var lowest *big.Rat
	for _, r := range sale.View().Rates {
		price := r.Price.Rat()
		if price.Sign() == 0 {
			continue
		}
		c := costs[key{r.ServiceTier, r.Above, r.Item}]
		margin := new(big.Rat).Sub(price, c.Rat())
		margin.Quo(margin, price)
		if lowest == nil || margin.Cmp(lowest) < 0 {
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
