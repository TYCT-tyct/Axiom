package main

import (
	"context"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

func registerSalePrices(db *pg.DB, queue *pg.Queue) error {
	return billing.RegisterPrices(queue, func(ctx context.Context) error {
		return db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
			sites, err := accounts.New().ListSites(ctx, tx)
			if err != nil {
				return err
			}
			for _, site := range sites {
				if !site.IsMain || site.Status != "active" {
					continue
				}
				currency, err := platform.ParseCurrency(site.Currency)
				if err != nil {
					return err
				}
				return refreshSalePrices(ctx, tx, site.ID, currency)
			}
			return nil
		})
	})
}

// refreshSalePrices locks before reading database-owned inputs so an older node cannot replace newer evidence.
func refreshSalePrices(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency) error {
	if _, err := billing.LockPricePolicy(ctx, tx, site); err != nil {
		return err
	}
	snap, err := catalog.ReadSnapshot(ctx, tx)
	if err != nil {
		return err
	}
	costs, rates, err := publicationCosts(ctx, tx, currency)
	if err != nil {
		return err
	}
	_, err = billing.RefreshPrices(ctx, tx, site, currency, snap, costs, rates...)
	return err
}

func publicationCosts(ctx context.Context, tx pg.Tx, currency platform.Currency) ([]billing.PriceCost, []platform.ExchangeRate, error) {
	rows, err := suppliers.PriceCosts(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	endpoints := make([]suppliers.Routable, len(rows))
	for i, r := range rows {
		value, err := platform.ParseUnitPrice(r.UnitValue)
		if err != nil {
			return nil, nil, err
		}
		endpoints[i] = suppliers.Routable{ID: r.Endpoint, Model: r.Model, Currency: r.Currency, Schedule: r.Schedule, UnitValue: value, UnitValueID: r.UnitValueID}
	}
	exchange, err := billing.ExchangeRates(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	rates := make([]platform.ExchangeRate, len(exchange))
	for i, r := range exchange {
		rates[i] = r.Basis()
	}
	return priceCosts(endpoints, rates, currency), rates, nil
}
