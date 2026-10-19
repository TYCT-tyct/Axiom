package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"uuid"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/platform"
	"axiom/internal/suppliers"
)

func TestExchangeAlertsAppearAndResolveDuringRefresh(t *testing.T) {
	usd, _ := platform.ParseCurrency("USD")
	cny, _ := platform.ParseCurrency("CNY")
	id := uuid.NewV7()
	enabled := []suppliers.SupplierCurrency{{Supplier: id, Currency: cny}}
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	count, err := provider.Meter("test").Int64Counter("missing_changes")
	if err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	missing := missingExchanges(enabled, usd, nil)
	if len(missing) != 1 {
		t.Fatal("enabled foreign supplier not detected")
	}
	reportExchangeChanges(t.Context(), logger, count, nil, missing)
	reportExchangeChanges(t.Context(), logger, count, missing, missing)
	rates := []platform.ExchangeRate{{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: 10, Denominator: 71}}
	resolved := missingExchanges(enabled, usd, rates)
	reportExchangeChanges(t.Context(), logger, count, missing, resolved)
	if len(resolved) != 0 || strings.Count(logs.String(), "billing.exchange_rate.missing") != 1 || strings.Count(logs.String(), "billing.exchange_rate.resolved") != 1 {
		t.Fatalf("transitions: %s", logs.String())
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, point := range sum.DataPoints {
					total += point.Value
				}
			}
		}
	}
	if total != 2 {
		t.Fatalf("metric transitions %d want 2", total)
	}
}
