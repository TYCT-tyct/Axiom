package main

import (
	"context"
	"log/slog"
	"uuid"

	attr "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

type missingExchange struct{ from, to string }

// exchangeRates is read once per routing build. A failed read returns an error, leaving the old snapshot's age intact.
func exchangeRates(d callDeps) func(context.Context) ([]platform.ExchangeRate, error) {
	meter := d.meter
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("billing")
	}
	changes, err := meter.Int64Counter("axiom.billing.exchange_rate.missing_changes", metric.WithDescription("Missing supplier exchange rate transitions, by state"))
	if err != nil {
		panic(err)
	}
	missing := map[uuid.UUID]missingExchange{}
	return func(ctx context.Context) ([]platform.ExchangeRate, error) {
		site, ok := d.accounts.MainSite()
		if !ok {
			return nil, nil
		}
		var rates []billing.BillingExchangeRate
		var enabled []suppliers.SupplierCurrency
		err := d.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
			rates, err = billing.ExchangeRates(ctx, tx)
			if err == nil {
				enabled, err = suppliers.EnabledCurrencies(ctx, tx)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		basis := make([]platform.ExchangeRate, len(rates))
		for i, r := range rates {
			basis[i] = r.Basis()
		}
		next := missingExchanges(enabled, site.Currency, basis)
		reportExchangeChanges(ctx, d.logger, changes, missing, next)
		missing = next
		return basis, nil
	}
}

func missingExchanges(enabled []suppliers.SupplierCurrency, site platform.Currency, rates []platform.ExchangeRate) map[uuid.UUID]missingExchange {
	missing := map[uuid.UUID]missingExchange{}
	for _, s := range enabled {
		if s.Currency == site {
			continue
		}
		found := false
		for _, r := range rates {
			if _, ok := r.Between(s.Currency, site); ok {
				found = true
				break
			}
		}
		if !found {
			missing[s.Supplier] = missingExchange{s.Currency.String(), site.String()}
		}
	}
	return missing
}

func reportExchangeChanges(ctx context.Context, logger *slog.Logger, count metric.Int64Counter, old, next map[uuid.UUID]missingExchange) {
	for id, pair := range old {
		if next[id] == pair {
			continue
		}
		logger.Info("billing.exchange_rate.resolved", "supplier_id", id.String(), "from", pair.from, "to", pair.to)
		count.Add(ctx, 1, metric.WithAttributes(attr.String("state", "resolved")))
	}
	for id, pair := range next {
		if old[id] == pair {
			continue
		}
		logger.Warn("billing.exchange_rate.missing", "supplier_id", id.String(), "from", pair.from, "to", pair.to)
		count.Add(ctx, 1, metric.WithAttributes(attr.String("state", "missing")))
	}
}
