package billing

import (
	"bytes"
	"context"
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
	for _, row := range rows {
		if row.CurrentID == nil || row.Status != "published" && row.Status != "pending" {
			continue
		}
		sold, err := readPublishedPrice(row, policy)
		if err != nil {
			return nil, err
		}

		out[catalog.ModelID(row.ModelID)] = sold
	}
	return out, nil
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
