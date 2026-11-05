package billing

import (
	"math/big"

	"axiom/internal/catalog"
)

// PriceMove is how far publishing p moves what its model is charged now, and the least margin p keeps over its cost,
// for the operations agent's price envelope (docs/domains/operations.md「售价的累计变动上限」). change is the largest
// |new − now| ÷ now of any price an endpoint is charged, comparing each endpoint with its own current price; it is nil
// when there is no current version, an endpoint has no current price, or a move is no share of an old price. margin is
// the smallest (price − cost) ÷ price of any endpoint, against that endpoint's cost, or the model's dearest cost for a
// price every endpoint shares; it is nil when the cost is unknown or does not measure so.
func PriceMove(p Publication) (change, margin *big.Rat, err error) {
	next, err := catalog.ParseSchedule(p.Schedule)
	if err != nil {
		return nil, nil, err
	}
	// Each helper answers nil for what it cannot measure.
	if change, _, err = priceChange(p, next); err != nil {
		return nil, nil, err
	}
	if margin, _, err = priceMargin(p, next); err != nil {
		return nil, nil, err
	}
	return change, margin, nil
}

// priceChange pairs every price p charges with the one the same endpoint is charged now and keeps the largest move.
func priceChange(p Publication, next catalog.Schedule) (*big.Rat, bool, error) {
	if p.Current == nil || len(p.CurrentSchedule) == 0 {
		return nil, false, nil
	}
	current, err := catalog.ParseSchedule(p.CurrentSchedule)
	if err != nil {
		return nil, false, err
	}
	pairs, ok := pricePairs(p, current, next)
	if !ok {
		return nil, false, nil
	}
	largest := new(big.Rat)
	for _, c := range pairs {
		move, measured, err := c.now.LargestChange(c.next)
		if err != nil || !measured {
			return nil, measured, err
		}
		if move.Cmp(largest) > 0 {
			largest = move
		}
	}
	return largest, true, nil
}

type pricePair struct{ now, next catalog.Schedule }

// pricePairs is each price p charges beside what the same endpoint is charged now; false when an endpoint p prices has
// no current price of its own.
func pricePairs(p Publication, current, next catalog.Schedule) ([]pricePair, bool) {
	switch {
	case p.Endpoints != nil:
		pairs := make([]pricePair, 0, len(p.Endpoints))
		for endpoint, price := range p.Endpoints {
			now, ok := current, true
			if p.CurrentEndpoints != nil {
				now, ok = p.CurrentEndpoints[endpoint]
			}
			if !ok {
				return nil, false
			}
			pairs = append(pairs, pricePair{now, price})
		}
		return pairs, true
	case p.CurrentEndpoints != nil:
		// A shared price replacing endpoint prices moves each endpoint from its own price, which the model envelope hides.
		pairs := make([]pricePair, 0, len(p.CurrentEndpoints))
		for _, now := range p.CurrentEndpoints {
			pairs = append(pairs, pricePair{now, next})
		}
		return pairs, true
	}
	return []pricePair{{current, next}}, true
}

// priceMargin is the least margin any endpoint's price keeps over its cost.
func priceMargin(p Publication, next catalog.Schedule) (*big.Rat, bool, error) {
	if p.Endpoints == nil {
		if len(p.Cost) == 0 {
			return nil, false, nil
		}
		cost, err := catalog.ParseSchedule(p.Cost)
		if err != nil {
			return nil, false, err
		}
		return next.SmallestMargin(cost)
	}
	var least *big.Rat
	for endpoint, price := range p.Endpoints {
		cost, ok := p.Costs[endpoint]
		if !ok {
			return nil, false, nil
		}
		margin, measured, err := price.SmallestMargin(cost)
		if err != nil || !measured {
			return nil, measured, err
		}
		if least == nil || margin.Cmp(least) < 0 {
			least = margin
		}
	}
	return least, true, nil
}
