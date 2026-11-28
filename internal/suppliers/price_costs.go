package suppliers

import (
	"context"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

// PriceCost is the database-owned cost of an eligible endpoint, independent of this node's decrypted credentials.
type PriceCost struct {
	Endpoint, Model uuid.UUID
	Currency        platform.Currency
	Schedule        catalog.Schedule
	UnitValue       string
	UnitValueID     uuid.UUID
	// CopiedTiers are the service tiers an aggregator's ceiling plan prices only by copying its standard prices: a
	// bound, which sets no floor under a shared sale price (billing.PriceCost.CopiedTiers).
	CopiedTiers catalog.ServiceTierSet
}

// PriceCosts reads one canonical cost set without loading an adapter, decrypting a key or using a node's snapshot.
func PriceCosts(ctx context.Context, tx pg.Tx) ([]PriceCost, error) {
	rows, err := store.New().ListPriceCosts(ctx, tx)
	if err != nil {
		return nil, err
	}
	costs := make([]PriceCost, 0, len(rows))
	for _, r := range rows {
		currency, err := platform.ParseCurrency(r.SettlementCurrency)
		if err != nil {
			return nil, err
		}
		schedule, err := catalog.ParseSchedule(r.Schedule)
		if err != nil {
			return nil, err
		}
		costs = append(costs, PriceCost{
			Endpoint: r.EndpointID, Model: r.ModelID, Currency: currency, Schedule: schedule, UnitValue: r.UnitValue,
			UnitValueID: r.UnitValueID, CopiedTiers: copiedTiers(r.Basis, schedule),
		})
	}
	return costs, nil
}
