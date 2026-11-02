package billing

import (
	"bytes"
	"context"
	"slices"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func defaultPricePolicy() PricePolicy {
	return PricePolicy{FloorMultiplier: "1", ChangeRatio: "0.1", MaxIncreaseRatio: "1"}
}

func loadPricePolicy(ctx context.Context, tx pg.Tx, site uuid.UUID) (PricePolicy, error) {
	rows, err := queries.ReadPricePolicy(ctx, tx, site)
	if err != nil {
		return PricePolicy{}, err
	}
	if len(rows) == 0 {
		return defaultPricePolicy(), nil
	}
	return PricePolicy(rows[0]), nil
}

// PublishedPrices reads the serving heads without creating policy rows or taking publication locks.
func PublishedPrices(ctx context.Context, tx pg.Tx, site uuid.UUID) (map[catalog.ModelID]Sold, error) {
	fresh, err := queries.FreshPricePolicy(ctx, tx, FreshPricePolicyParams{SiteID: site, MaxAgeSeconds: int32(priceRefreshMaxAge / time.Second)})
	if err != nil {
		return nil, err
	}
	if len(fresh) == 0 {
		return map[catalog.ModelID]Sold{}, nil
	}
	policy, err := loadPricePolicy(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	rows, err := queries.ReadPublications(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	out := make(map[catalog.ModelID]Sold, len(rows))
	layers := map[catalog.ModelID][]AudiencePrice{}
	for _, row := range rows {
		price, err := publishedLayer(row, policy)
		if err != nil {
			return nil, err
		}
		if price.Target.Kind == "" {
			continue
		}
		model := catalog.ModelID(row.ModelID)
		layers[model] = append(layers[model], price)
		if row.AudienceKind == "all" && price.Sold.Version != uuid.Nil() {
			out[model] = price.Sold
		}
	}

	return packPublishedPrices(out, layers), nil
}

func packPublishedPrices(out map[catalog.ModelID]Sold, layers map[catalog.ModelID][]AudiencePrice) map[catalog.ModelID]Sold {
	for model, prices := range layers {
		// Preserve the default-only API's missing-price behavior. Audience layers must keep unavailable matches.
		if len(prices) == 1 && prices[0].Audience == (Audience{}) {
			continue
		}
		price := out[model]
		price.Audiences = CompilePrices(prices)
		out[model] = price
	}

	return out
}

func publishedLayer(row ReadPublicationsRow, policy PricePolicy) (AudiencePrice, error) {
	var proposal Publication
	if err := jsonx.Unmarshal(row.Candidate, &proposal); err != nil {
		return AudiencePrice{}, err
	}
	var basis struct {
		Rule *Rule `json:"rule"`
	}
	raw := proposal.Basis
	// A pending proposal must not change precedence before its price version is confirmed.
	if row.Status == "pending" && row.CurrentID != nil {
		raw = row.Basis
	}
	if err := jsonx.Unmarshal(raw, &basis); err != nil {
		return AudiencePrice{}, err
	}
	if basis.Rule == nil {
		return AudiencePrice{}, nil
	}
	selector := Selector{Kind: row.AudienceKind, ID: row.AudienceID}
	if err := checkAudience(selector); err != nil {
		return AudiencePrice{}, err
	}
	audience, err := selectorAudience(selector)
	if err != nil {
		return AudiencePrice{}, err
	}
	price := AudiencePrice{Audience: audience, Target: basis.Rule.Target}
	if row.CurrentID != nil && (row.Status == "published" || row.Status == "pending") {
		price.Sold, err = readPublishedPrice(row, policy)
	}
	return price, err
}

func readPublishedPrice(row ReadPublicationsRow, policy PricePolicy) (Sold, error) {
	sold, err := soldPublication(row)
	if err != nil {
		return Sold{}, err
	}
	sold.floor = policy.FloorMultiplier
	if !row.Overridden.Bool || row.PublishedCandidate == nil || row.CandidateID != *row.PublishedCandidate {
		return sold, nil
	}
	var basis struct {
		Policy PricePolicy `json:"policy"`
		Costs  []jsonx.Raw `json:"costs"`
	}
	if err := jsonx.Unmarshal(row.Basis, &basis); err != nil {
		return Sold{}, err
	}
	if basis.Policy != policy {
		return sold, nil
	}
	sold.overrideCosts = make(map[uuid.UUID][]byte, len(basis.Costs))
	for _, cost := range basis.Costs {
		var endpoint struct {
			Endpoint uuid.UUID `json:"endpoint"`
		}
		if err := jsonx.Unmarshal(cost, &endpoint); err != nil {
			return Sold{}, err
		}
		raw, err := jsonx.MarshalCanonical(cost)
		if err != nil {
			return Sold{}, err
		}
		sold.overrideCosts[endpoint.Endpoint] = raw
	}
	return sold, nil
}

// AllowsCosts checks this node's costs before it serves a published price. An override permits only its exact audited
// endpoint evidence; a new or more expensive local endpoint cannot borrow another endpoint's exception. A per-endpoint
// version is safe only when every endpoint it prices has this node's cost and that price covers it; routing filters an
// endpoint the node cannot serve rather than the whole model (WithCosts).
func (s Sold) AllowsCosts(model catalog.Model, costs []PriceCost) bool {
	if s.Endpoints != nil {
		return s.allowsEndpointCosts(model, costs)
	}
	return s.allowsSharedCosts(model, costs)
}

func (s Sold) allowsEndpointCosts(model catalog.Model, costs []PriceCost) bool {
	seen := 0
	for _, c := range costs {
		price, ok := s.Endpoints[c.Endpoint]
		if !ok {
			continue
		}
		seen++
		if !s.endpointAllows(model, c, price) {
			return false
		}
	}
	return seen == len(s.Endpoints)
}

func (s Sold) allowsSharedCosts(model catalog.Model, costs []PriceCost) bool {
	for _, c := range costs {
		if Unpriced(c.Schedule, model.Reportable()) != (catalog.Reportable{}) {
			return false
		}
	}
	cost, err := convertedCosts(costs, s.Schedule.Currency())
	if err != nil {
		return false
	}
	floor, err := envelopeOf(cost)
	if err != nil {
		return false
	}
	floor, err = floor.Scaled(s.floor, s.Schedule.Currency())
	if err != nil {
		return false
	}
	if safe, err := s.Schedule.CoversCost(floor); err == nil && safe {
		return true
	}
	if len(s.overrideCosts) == 0 {
		return false
	}
	for _, c := range costs {
		raw, err := jsonx.MarshalCanonical(c)
		if err != nil {
			return false
		}
		if !bytes.Equal(raw, s.overrideCosts[c.Endpoint]) {
			return false
		}
	}
	return true
}

// endpointAllows is one endpoint's local cost against the price this version set for it, including an override audited
// for that exact cost.
func (s Sold) endpointAllows(model catalog.Model, c PriceCost, price catalog.Schedule) bool {
	if Unpriced(price, model.Reportable()) != (catalog.Reportable{}) ||
		Unpriced(c.Schedule, model.Reportable()) != (catalog.Reportable{}) {
		return false
	}
	cost, err := c.converted(price.Currency())
	if err != nil {
		return false
	}
	floor, err := cost.Scaled(s.floor, price.Currency())
	if err != nil {
		return false
	}
	if safe, err := price.CoversCost(floor); err == nil && safe {
		return true
	}
	if len(s.overrideCosts) == 0 {
		return false
	}
	raw, err := jsonx.MarshalCanonical(c)
	return err == nil && bytes.Equal(raw, s.overrideCosts[c.Endpoint])
}

// WithCosts withholds unsafe audience layers while retaining their targets, so they cannot fall back. A per-endpoint
// version keeps the endpoints this node can serve safely and drops the rest; a shared version is all or nothing.
func (s Sold) WithCosts(model catalog.Model, costs []PriceCost) Sold {
	if s.Audiences != nil {
		layers := slices.Clone(s.Audiences.Layers())
		for i := range layers {
			layers[i].Sold = layers[i].Sold.WithCosts(model, costs)
		}
		s.Audiences = CompilePrices(layers)
	}
	if s.Endpoints != nil {
		return s.withEndpointCosts(model, costs)
	}
	return s.withSharedCosts(model, costs)
}

func (s Sold) withEndpointCosts(model catalog.Model, costs []PriceCost) Sold {
	kept := make(EndpointPrices, len(s.Endpoints))
	for _, c := range costs {
		if price, ok := s.Endpoints[c.Endpoint]; ok && s.endpointAllows(model, c, price) {
			kept[c.Endpoint] = price
		}
	}
	if len(kept) == 0 {
		s.Version = uuid.Nil()
		s.Schedule = catalog.Schedule{}
		s.Endpoints = nil
		return s
	}
	// Keep the model-level envelope the envelope of the endpoints this node can serve, so a reader that falls back to
	// it does not see a price for an endpoint this node withheld. A schedule that cannot be enclosed leaves the published
	// envelope in place: no charge reads it for an endpoint in the map.
	if envelope, err := envelopeOf(kept); err == nil {
		s.Schedule = envelope
	}
	s.Endpoints = kept
	return s
}

func (s Sold) withSharedCosts(model catalog.Model, costs []PriceCost) Sold {
	if !s.allowsSharedCosts(model, costs) || Unpriced(s.Schedule, model.Reportable()) != (catalog.Reportable{}) {
		s.Version = uuid.Nil()
		s.Schedule = catalog.Schedule{}
	}
	return s
}
