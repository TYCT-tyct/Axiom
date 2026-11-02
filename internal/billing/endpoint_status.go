package billing

import "axiom/internal/catalog"

// automaticEndpointPriceStatus applies the same change and floor gates to every endpoint's own price. A model-level
// envelope can stay unchanged while one endpoint's cost, and so its price, moves; comparing only the envelope would
// publish that move without confirmation. A missing head is a first publication; a version that has no price for an
// endpoint being published now is a new charge and waits for the same confirmation as any other move.
func automaticEndpointPriceStatus(policy PricePolicy, current Sold, next, floors EndpointPrices, hasHead bool) string {
	if !hasHead {
		return "published"
	}
	if len(next) == 0 || len(floors) == 0 {
		return "blocked"
	}
	pending := false
	for endpoint := range current.Endpoints {
		if _, exists := next[endpoint]; !exists {
			// Removing a priced endpoint also changes what the model can serve; hold the old version until the
			// removal is confirmed, rather than silently dropping it while the envelope stays the same.
			pending = true
		}
	}
	for endpoint, price := range next {
		floor, exists := floors[endpoint]
		if !exists {
			return "blocked"
		}
		previous, exists := current.PriceFor(endpoint)
		if !exists {
			// The version being published adds an endpoint. Its price is a new charge; hold the old version, which
			// keeps serving the other endpoints at their current prices, rather than stopping the whole model.
			pending = true
			continue
		}
		status := endpointPriceStatus(policy, previous, price, floor)
		if status == "blocked" {
			return "blocked"
		}
		pending = pending || status == "pending"
	}
	if pending {
		return "pending"
	}
	return "published"
}

// automaticSharedEndpointPriceStatus applies the endpoint gate when the current version prices endpoints separately but
// the proposal returns to one shared fixed or reference price. The old version charged each endpoint its own price, so
// each of those prices is compared with the shared proposal; the model envelope alone can hide an endpoint's move.
func automaticSharedEndpointPriceStatus(policy PricePolicy, current Sold, next, floor catalog.Schedule, hasHead bool) string {
	if !hasHead {
		return "published"
	}
	if len(current.Endpoints) == 0 {
		return automaticPriceStatus(policy, current.Schedule, next, floor, hasHead)
	}
	pending := false
	for _, previous := range current.Endpoints {
		// The shared version stores no per-endpoint floor; comparing an old endpoint price with the model envelope
		// would wrongly treat a cheap endpoint as unsafe. The local cost filter drops an old endpoint that no longer
		// covers its own cost, while every actual move still needs confirmation.
		if priceIncreased(previous, next, policy.MaxIncreaseRatio) || priceMoved(previous, next, policy.ChangeRatio) {
			pending = true
		}
	}
	if pending {
		return "pending"
	}
	return "published"
}

// endpointPriceStatus classifies one endpoint's move. An old price below its floor may rise to the floor without a
// confirmation when the rise stays inside the automatic cap; a safe old price needs confirmation for any move beyond
// the normal change threshold.
func endpointPriceStatus(policy PricePolicy, previous, next, floor catalog.Schedule) string {
	safe, err := previous.CoversCost(floor)
	if err != nil {
		return "blocked"
	}
	if !safe {
		if priceIncreased(previous, next, policy.MaxIncreaseRatio) {
			return "blocked"
		}
		return "published"
	}
	if priceIncreased(previous, next, policy.MaxIncreaseRatio) || priceMoved(previous, next, policy.ChangeRatio) {
		return "pending"
	}
	return "published"
}
