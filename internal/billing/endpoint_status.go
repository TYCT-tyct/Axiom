package billing

import (
	"uuid"

	"axiom/internal/catalog"
)

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
	currentSet := soldEndpointSet(current)
	nextSet := endpointSet(next)
	if current.Endpoints == nil && len(currentSet) == 0 {
		// An old shared version without cost evidence cannot say which endpoints it already priced. Treating every
		// proposed endpoint as an old one could publish a new charge without confirmation, so wait.
		return "pending"
	}
	// Removing a priced endpoint or adding one also changes what the model can serve; hold the old version until the
	// membership change is confirmed.
	pending := endpointMembershipMoved(currentSet, nextSet)
	for endpoint, price := range next {
		if _, exists := currentSet[endpoint]; !exists {
			pending = true
			continue
		}
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
// nextFloors is the proposal's own floor per endpoint; when old evidence lacks it, a move still waits for confirmation.
func automaticSharedEndpointPriceStatus(policy PricePolicy, current Sold, next catalog.Schedule, nextFloors EndpointPrices) string {
	currentSet := soldEndpointSet(current)
	nextSet := endpointSet(nextFloors)
	pending := endpointMembershipMoved(currentSet, nextSet)
	for endpoint := range currentSet {
		previous, exists := current.PriceFor(endpoint)
		if !exists {
			pending = true
			continue
		}
		if endpointFloor, exists := nextFloors[endpoint]; exists {
			status := endpointPriceStatus(policy, previous, next, endpointFloor)
			if status == "blocked" {
				return "blocked"
			}
			pending = pending || status == "pending"
			continue
		}
		// Without the proposal's per-endpoint cost evidence, any move still needs confirmation; no move retains the
		// old price. The local cost filter drops an old endpoint that no longer covers its own cost.
		if priceIncreased(previous, next, policy.MaxIncreaseRatio) || priceMoved(previous, next, policy.ChangeRatio) {
			pending = true
		}
	}
	if pending {
		return "pending"
	}
	return "published"
}

func endpointMembershipMoved(current, next map[uuid.UUID]struct{}) bool {
	for endpoint := range current {
		if _, exists := next[endpoint]; !exists {
			return true
		}
	}
	for endpoint := range next {
		if _, exists := current[endpoint]; !exists {
			return true
		}
	}
	return false
}

func soldEndpointSet(s Sold) map[uuid.UUID]struct{} {
	return endpointSet(s.Endpoints, s.endpointCosts)
}

func endpointSet(maps ...EndpointPrices) map[uuid.UUID]struct{} {
	set := map[uuid.UUID]struct{}{}
	for _, prices := range maps {
		for endpoint := range prices {
			set[endpoint] = struct{}{}
		}
	}
	return set
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
