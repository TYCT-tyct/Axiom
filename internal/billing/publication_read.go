package billing

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func defaultPricePolicy() PricePolicy {
	return PricePolicy{FloorMultiplier: "1", ChangeRatio: "0.1", MaxIncreaseRatio: "1", ByokFeeBPS: 500}
}

func loadPricePolicy(ctx context.Context, tx pg.Tx, site uuid.UUID) (PricePolicy, error) {
	rows, err := queries.ReadPricePolicy(ctx, tx, site)
	if err != nil {
		return PricePolicy{}, err
	}
	if len(rows) == 0 {
		return defaultPricePolicy(), nil
	}
	return PricePolicy{
		Rev: rows[0].Rev, FloorMultiplier: rows[0].FloorMultiplier, ChangeRatio: rows[0].ChangeRatio,
		MaxIncreaseRatio: rows[0].MaxIncreaseRatio, ByokFeeBPS: rows[0].ByokFeeBps,
	}, nil
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
// endpoint evidence; a new or more expensive local endpoint cannot borrow another endpoint's exception.
func (s Sold) AllowsCosts(model catalog.Model, costs []PriceCost) bool {
	cost, err := modelPriceCost(costs, model, s.Schedule.Currency())
	if err != nil {
		return false
	}
	floor, err := cost.Scaled(s.floor, s.Schedule.Currency())
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

// WithCosts withholds unsafe audience layers while retaining their targets, so they cannot fall back.
func (s Sold) WithCosts(model catalog.Model, costs []PriceCost) Sold {
	if s.Audiences != nil {
		layers := slices.Clone(s.Audiences.Layers())
		for i := range layers {
			layers[i].Sold = layers[i].Sold.WithCosts(model, costs)
		}
		s.Audiences = CompilePrices(layers)
	}
	if !s.AllowsCosts(model, costs) || Unpriced(s.Schedule, model.Reportable()) != (catalog.Reportable{}) {
		s.Version = uuid.Nil()
		s.Schedule = catalog.Schedule{}
	}
	return s
}

// ByokFee is the site's share of a model's reference price, charged when the account's own provider key serves the
// request (docs/domains/billing.md「定价」). It is a decimal multiplier for a reference schedule: 500 basis points is
// "0.0500". A site without a policy row has the default, the same value the column's default gives.
func ByokFee(ctx context.Context, tx pg.Tx, site uuid.UUID) (string, error) {
	policy, err := loadPricePolicy(ctx, tx, site)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%04d", policy.ByokFeeBPS/10000, policy.ByokFeeBPS%10000), nil
}

// PricePolicyOf reads the site's policy, with the defaults for a site that has none. A caller that changes one field
// reads it first, so the fields it did not send keep their values.
func PricePolicyOf(ctx context.Context, tx pg.Tx, site uuid.UUID) (PricePolicy, error) {
	return loadPricePolicy(ctx, tx, site)
}
