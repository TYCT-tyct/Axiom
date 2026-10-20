package suppliers

import (
	"context"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

// SupplierCurrency is an enabled supplier's settlement currency, including suppliers with no routable endpoints.
type SupplierCurrency struct {
	Supplier uuid.UUID
	Currency platform.Currency
}

// EnabledCurrencies reads configuration for the routing refresh's missing-rate alerts, in platform scope.
func EnabledCurrencies(ctx context.Context, tx pg.Tx) ([]SupplierCurrency, error) {
	rows, err := store.New().EnabledSupplierCurrencies(ctx, tx)
	if err != nil {
		return nil, err
	}
	result := make([]SupplierCurrency, 0, len(rows))
	for _, r := range rows {
		currency, err := platform.ParseCurrency(r.SettlementCurrency)
		if err != nil {
			return nil, err
		}
		result = append(result, SupplierCurrency{Supplier: r.ID, Currency: currency})
	}
	return result, nil
}
