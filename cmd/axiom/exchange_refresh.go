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
		var rates []billing.BillingExchangeRate
		err := d.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
			rates, err = billing.ExchangeRates(ctx, tx)
			return err
		})
		if err != nil {
			return nil, err
		}
		basis := make([]platform.ExchangeRate, len(rates))
		for i, r := range rates {
			basis[i] = r.Basis()
		}
		// The alert serves the log and the metric only: not reading what it needs must not cost the build its rates.
		if next, ok := d.missingNow(ctx, basis); ok {
			reportExchangeChanges(ctx, d.logger, changes, missing, next)
			missing = next
		}
		return basis, nil
	}
}

// missingNow is the enabled suppliers that have no rate to the main site's currency; ok is false when that cannot be
// told now (no main site yet, or the suppliers unreadable), which leaves the last answer standing.
func (d callDeps) missingNow(ctx context.Context, rates []platform.ExchangeRate) (map[uuid.UUID]missingExchange, bool) {
	site, ok := d.accounts.MainSite()
	if !ok {
		return nil, false
	}
	var enabled []suppliers.SupplierCurrency
	err := d.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
		enabled, err = suppliers.EnabledCurrencies(ctx, tx)
		return err
	})
	if err != nil {
		d.logger.Warn("billing.exchange_rate.alert_skipped", "err", err)
		return nil, false
	}
	return missingExchanges(enabled, site.Currency, rates), true
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
