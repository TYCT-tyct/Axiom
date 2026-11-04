package billing

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"slices"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// RefreshPrices reconciles input state, not events. It serializes publishers on the policy row and publishes only
// safe prices. Costs must be from the same immutable endpoint/rate reading the caller will put into routing.
func RefreshPrices(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, snap *catalog.Snapshot, costs []PriceCost, rates ...platform.ExchangeRate) (map[catalog.ModelID]Sold, error) {
	policy, err := LockPricePolicy(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	rules, err := Rules(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	rows, err := queries.ReadPublications(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	grouped, previous := publicationInputs(rules, rows)
	byModel := map[catalog.ModelID][]PriceCost{}
	for _, c := range costs {
		byModel[c.Model] = append(byModel[c.Model], c)
	}
	defaults, err := priceDefaults(ctx, tx, currency)
	if err != nil {
		return nil, err
	}
	// Reconcile previous selectors too: an edit may have moved the rule to a different audience.
	for audience, rules := range grouped {
		if err := refreshAudience(ctx, tx, site, currency, snap, byModel, defaults, policy, audience, rules, previous[audience], rates); err != nil {
			return nil, err
		}
	}
	if err := queries.ConfirmPriceRefresh(ctx, tx, site); err != nil {
		return nil, err
	}
	return PublishedPrices(ctx, tx, site)
}

func publicationInputs(rules []Rule, rows []ReadPublicationsRow) (map[Selector][]Rule, map[Selector]map[catalog.ModelID]ReadPublicationsRow) {
	previous := map[Selector]map[catalog.ModelID]ReadPublicationsRow{}
	grouped := map[Selector][]Rule{{Kind: "all"}: nil}
	for _, r := range rules {
		if r.Enabled {
			grouped[r.Audience] = append(grouped[r.Audience], r)
		}
	}
	for _, r := range rows {
		audience := Selector{Kind: r.AudienceKind, ID: r.AudienceID}
		if previous[audience] == nil {
			previous[audience] = map[catalog.ModelID]ReadPublicationsRow{}
		}
		previous[audience][catalog.ModelID(r.ModelID)] = r
	}
	for audience := range previous {
		if _, ok := grouped[audience]; !ok {
			grouped[audience] = nil
		}
	}
	return grouped, previous
}

func refreshAudience(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, snap *catalog.Snapshot, costs map[catalog.ModelID][]PriceCost, defaults fallbacks, policy PricePolicy, audience Selector, rules []Rule, previous map[catalog.ModelID]ReadPublicationsRow, rates []platform.ExchangeRate) error {
	for m := range snap.Models() {
		old := previous[m.ID]
		if audience.Kind != "all" && old.CandidateID == uuid.Nil() && !slices.ContainsFunc(rules, func(r Rule) bool { return r.Enabled && pricesModel(r.Target, m) }) {
			continue
		}
		p, err := proposeAudiencePrice(audience, policy, rules, m, snap, currency, costs[m.ID], defaults, rates...)
		if err != nil {
			return err
		}
		if audience.Kind != "all" && !p.matched && old.CandidateID == uuid.Nil() {
			continue
		}
		if _, err := reconcilePrice(ctx, tx, site, policy, p, old); err != nil {
			return fmt.Errorf("billing: refresh price %s: %w", m.ID, err)
		}
	}
	return nil
}

func digestPrice(raw []byte) uuid.UUID {
	sum := sha256.Sum256(raw)
	var id uuid.UUID
	copy(id[:], sum[:16])
	id[6] = id[6]&0x0f | 0x80
	id[8] = id[8]&0x3f | 0x80
	return id
}

func reconcilePrice(ctx context.Context, tx pg.Tx, site uuid.UUID, policy PricePolicy, p Publication, old ReadPublicationsRow) (Sold, error) {
	if p.Candidate == old.CandidateID {
		if old.Status == "published" || old.Status == "pending" {
			return soldPublication(old)
		}
		return Sold{}, nil
	}
	p.Current = old.CurrentID
	current, status, err := publicationDecision(policy, p, old)
	if err != nil {
		return Sold{}, err
	}
	p.Status = status
	if status == "published" {
		current, err = publishVersion(ctx, tx, audit.System("billing.prices"), site, p, false, "automatic price publication")
		if err != nil {
			return Sold{}, err
		}
		p.Current = &current.Version
	}
	if err := publicationSignal(ctx, tx, site, p, false, old.Status); err != nil {
		return Sold{}, err
	}
	if err := persistPublication(ctx, tx, site, p); err != nil {
		return Sold{}, err
	}
	return current, nil
}

func publicationDecision(policy PricePolicy, p Publication, old ReadPublicationsRow) (Sold, string, error) {
	if len(p.Schedule) == 0 {
		return Sold{}, "unpriced", nil
	}
	next, err := catalog.ParseSchedule(p.Schedule)
	if err != nil {
		return Sold{}, "", err
	}
	previous, err := catalog.ParseSchedule(p.Requested)
	if err != nil {
		return Sold{}, "", err
	}
	floor, err := catalog.ParseSchedule(p.Floor)
	if err != nil {
		return Sold{}, "", err
	}
	var current Sold
	if old.CurrentID != nil {
		current, err = soldPublication(old)
		if err != nil {
			return Sold{}, "", err
		}
		// A shared version has no endpoint map, so its cost evidence is the only record of which endpoints it
		// prices. It is read only on this decision path, not when routing loads every published price.
		if current.Endpoints == nil {
			current.endpointCosts = endpointCostsFromBasis(old.Basis, current.Schedule.Currency())
		}
		previous = current.Schedule
	}
	status := publicationStatus(policy, p, current, previous, next, floor, old.CurrentID != nil)
	if status == "blocked" {
		current = Sold{}
	}
	return current, status, nil
}

// publicationStatus classifies a proposal against the current version. A per-endpoint proposal gets the endpoint gate;
// a return from endpoint prices to one shared price compares every current endpoint; a shared proposal that does not
// move still waits when its cost evidence shows an endpoint added or removed.
func publicationStatus(policy PricePolicy, p Publication, current Sold, previous, next, floor catalog.Schedule, hasHead bool) string {
	status := automaticPriceStatus(policy, previous, next, floor, hasHead)
	switch {
	case p.Endpoints != nil:
		return automaticEndpointPriceStatus(policy, current, p.Endpoints, p.Floors, hasHead)
	case hasHead && current.Endpoints != nil:
		// The proposal goes back to a shared fixed or reference price. Every endpoint's current price is a real old
		// charge, so compare each one with the shared proposal rather than only the model envelope, which may not move.
		// The proposal's own cost evidence recovers its endpoint floors; an old candidate without it falls back to
		// the ordinary move gate, which still holds any change for confirmation.
		return automaticSharedEndpointPriceStatus(policy, current, next, endpointFloorsFromBasis(p.Basis, next.Currency(), policy.FloorMultiplier))
	case hasHead && status == "published" && sharedMembershipMoved(current, p.Basis, next.Currency()):
		return "pending"
	}
	return status
}

// sharedMembershipMoved reports whether a shared proposal changes the endpoint set recorded in the current version's
// cost evidence. The price itself does not move, but adding or removing an endpoint changes what the shared price covers.
func sharedMembershipMoved(current Sold, nextBasis jsonx.Raw, currency platform.Currency) bool {
	if current.Endpoints != nil {
		return false
	}
	nextCosts := endpointCostsFromBasis(nextBasis, currency)
	return len(current.endpointCosts) > 0 && len(nextCosts) > 0 &&
		endpointMembershipMoved(endpointSet(current.endpointCosts), endpointSet(nextCosts))
}

func automaticPriceStatus(policy PricePolicy, previous, next, floor catalog.Schedule, hasHead bool) string {
	safe, err := previous.CoversCost(floor)
	safe = hasHead && safe && err == nil
	if !safe && priceIncreased(previous, next, policy.MaxIncreaseRatio) {
		return "blocked"
	}
	if !hasHead || !safe || !priceMoved(previous, next, policy.ChangeRatio) {
		return "published"
	}
	return "pending"
}

func priceIncreased(previous, next catalog.Schedule, ratio string) bool {
	r, _ := new(big.Rat).SetString(ratio)
	moved, err := previous.PriceIncrease(next, r)
	return moved || err != nil
}

func priceMoved(previous, next catalog.Schedule, ratio string) bool {
	r, _ := new(big.Rat).SetString(ratio)
	moved, err := previous.PriceChange(next, r)
	return moved || err != nil
}

func soldPublication(r ReadPublicationsRow) (Sold, error) {
	if r.CurrentID == nil {
		return Sold{}, nil
	}
	s, err := catalog.ParseSchedule(r.Schedule)
	if err != nil {
		return Sold{}, err
	}
	endpoints, err := parseEndpointPrices(r.Endpoints)
	if err != nil {
		return Sold{}, err
	}
	return Sold{Version: *r.CurrentID, Schedule: s, Endpoints: endpoints}, nil
}

func persistPublication(ctx context.Context, tx pg.Tx, site uuid.UUID, p Publication) error {
	// CurrentEndpoints and CurrentSchedule are a read view of the joined current version, not part of the proposal.
	// PublishPrice reads them from PriceState and persists the candidate afterwards; keeping them would make an old
	// view override the row's actual current version after the next publication.
	p.CurrentEndpoints, p.CurrentSchedule = nil, nil
	raw, err := jsonx.MarshalCanonical(p)
	if err != nil {
		return err
	}
	if p.Audience.Kind != "all" {
		return queries.SaveAudiencePublication(ctx, tx, SaveAudiencePublicationParams{SiteID: site, ModelID: uuid.UUID(p.Model), AudienceKind: p.Audience.Kind, AudienceID: p.Audience.ID, CandidateID: p.Candidate, Candidate: raw, CurrentID: p.Current, Status: p.Status})
	}
	return queries.SavePublication(ctx, tx, SavePublicationParams{SiteID: site, ModelID: uuid.UUID(p.Model), CandidateID: p.Candidate, Candidate: raw, CurrentID: p.Current, Status: p.Status})
}
