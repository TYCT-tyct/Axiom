package billing

import (
	"fmt"
	"maps"
	"slices"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// EndpointPrices is one published version's sale price by endpoint: what a call that the endpoint serves is charged
// (docs/domains/billing.md「定价」). It is nil for a version whose one Schedule prices every endpoint. The JSON form is an
// object keyed by endpoint UUID, so a version's prices stay one immutable value with it.
type EndpointPrices map[uuid.UUID]catalog.Schedule

// MarshalJSON writes the prices as the object the price version stores and the candidate carries. Schedules have their
// own canonical JSON, and the map keys are sorted by the canonical encoder, so equal prices give equal bytes.
func (e EndpointPrices) MarshalJSON() ([]byte, error) {
	raw := make(map[string]jsonx.Raw, len(e))
	for id, schedule := range e {
		body, err := schedule.MarshalJSON()
		if err != nil {
			return nil, err
		}
		raw[id.String()] = body
	}
	return jsonx.MarshalCanonical(raw)
}

// UnmarshalJSON reads the object back into schedules, refusing a key that is not an endpoint UUID and a schedule the
// catalog cannot hold: a version is evidence, and a form we cannot read is not silently skipped.
func (e *EndpointPrices) UnmarshalJSON(body []byte) error {
	if string(body) == "null" {
		*e = nil
		return nil
	}
	var raw map[string]jsonx.Raw
	if err := jsonx.Unmarshal(body, &raw); err != nil {
		return err
	}
	prices := make(EndpointPrices, len(raw))
	for key, schedule := range raw {
		id, err := uuid.Parse(key)
		if err != nil {
			return err
		}
		parsed, err := catalog.ParseSchedule(schedule)
		if err != nil {
			return err
		}
		prices[id] = parsed
	}
	*e = prices
	return nil
}

func parseEndpointPrices(raw []byte) (EndpointPrices, error) {
	if len(raw) == 0 {
		return nil, nil //nolint:nilnil // an empty column is no endpoint map, not a failed read
	}
	var prices EndpointPrices
	if err := jsonx.Unmarshal(raw, &prices); err != nil {
		return nil, err
	}
	return prices, nil
}

// PriceFor is the immutable price this version sets for one endpoint. A shared version prices every endpoint, so its
// schedule answers for any; a per-endpoint version has no price for an endpoint it did not publish.
func (s Sold) PriceFor(endpoint uuid.UUID) (catalog.Schedule, bool) {
	if s.Version == uuid.Nil() {
		return catalog.Schedule{}, false
	}
	if s.Endpoints == nil {
		return s.Schedule, true
	}
	schedule, ok := s.Endpoints[endpoint]
	return schedule, ok
}

// ModelPrice is the price a model list shows: the cheapest served endpoint's schedule, ordered by the sum of the input
// and output prices at the standard tier below the first threshold, as OpenRouter orders provider prices. A shared
// version is the same price for every endpoint, so it answers with its schedule. Ties keep the first endpoint served,
// which is the order the routing snapshot built.
func (s Sold) ModelPrice(served []uuid.UUID) (catalog.Schedule, bool) {
	if s.Version == uuid.Nil() {
		return catalog.Schedule{}, false
	}
	if s.Endpoints == nil {
		return s.Schedule, true
	}
	var cheapest catalog.Schedule
	found := false
	for _, id := range served {
		schedule, ok := s.Endpoints[id]
		if !ok {
			continue
		}
		if !found || cheaperEndpointPrice(schedule, cheapest) {
			cheapest, found = schedule, true
		}
	}
	return cheapest, found
}

// cheaperEndpointPrice orders two endpoint prices by input plus output at the base band, the sum the model list's price
// orderings compare. A schedule without both prices cannot be a model's list price and is never chosen over one that
// has them.
func cheaperEndpointPrice(a, b catalog.Schedule) bool {
	ai, hasAI := a.BaseRate(catalog.ItemInput)
	ao, hasAO := a.BaseRate(catalog.ItemOutput)
	bi, hasBI := b.BaseRate(catalog.ItemInput)
	bo, hasBO := b.BaseRate(catalog.ItemOutput)
	switch {
	case !hasAI || !hasAO:
		return false
	case !hasBI || !hasBO:
		return true
	}
	as, _ := ai.Mul(1).Add(ao.Mul(1)) //nolint:errcheck // each price is at most a thousand credits, so the sum cannot overflow
	bs, _ := bi.Mul(1).Add(bo.Mul(1)) //nolint:errcheck // as above
	return as.Cmp(bs) < 0
}

// envelopeOf is the dearest price of every endpoint, item by item, band by band and service tier by service tier: the
// one price a node that reads only the model-level schedule can charge without losing money on any endpoint.
func envelopeOf(prices EndpointPrices) (catalog.Schedule, error) {
	if len(prices) == 0 {
		return catalog.Schedule{}, ErrUnpriced
	}
	ids := slices.SortedFunc(maps.Keys(prices), func(a, b uuid.UUID) int { return a.Compare(b) })
	empty, err := catalog.ParseSchedule(fmt.Appendf(nil, `{"v":1,"currency":%q,"rates":{}}`, prices[ids[0]].Currency().String()))
	if err != nil {
		return catalog.Schedule{}, err
	}
	envelope := empty
	for _, id := range ids {
		envelope, err = envelope.Aligned(prices[id], false)
		if err != nil {
			return catalog.Schedule{}, err
		}
	}
	return envelope, nil
}
