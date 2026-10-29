package suppliers

import (
	"context"
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers/internal/store"
)

func TestUnitValuesOnlyApplyToTheirCurrencyAndNativeUnit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, currency, unit string }{
		{"currency", "CNY", "quota"},
		{"native unit", "USD", "usd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkUnitValueDimensions(t, tc.currency, tc.unit)
		})
	}
}

func checkUnitValueDimensions(t *testing.T, currency, unit string) {
	t.Helper()
	w := newWorld(t, 2)
	began, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	old := began.Done()
	// No application path changes these fields today. Simulate a future configuration change.
	if err := pgtest.Run(t.Context(), w.database.Superuser, "UPDATE suppliers.suppliers SET settlement_currency = '"+currency+"', native_unit = '"+unit+"'"); err != nil {
		t.Fatal(err)
	}

	w.checkUnitValueReaders("")
	w.addValue("3") // Legacy INSERT, as an old node would still write during rolling release.
	w.checkUnitValueReaders("3")
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	later, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if receipt := later.Done(); receipt.unitValue.String() != "3" || receipt.unitValueID == old.unitValueID {
		t.Errorf("new receipt did not use the new value: %+v", receipt)
	}
	if n := w.count("SELECT count(*) FROM suppliers.unit_values WHERE id = $1 AND currency = 'USD' AND native_unit = 'quota' AND value = 2", old.unitValueID); n != 1 {
		t.Error("historical unit value dimensions changed")
	}
}

func (w *world) checkUnitValueReaders(want string) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		q := store.New()
		routes, err := q.ListRoutable(ctx, tx)
		if err != nil {
			return err
		}
		costs, err := PriceCosts(ctx, tx)
		if err != nil {
			return err
		}
		view, err := q.GetSupplierView(ctx, tx, w.supplier)
		if err != nil {
			return err
		}
		page, err := q.ListSuppliersPage(ctx, tx, store.ListSuppliersPageParams{PageSize: 10})
		if err != nil {
			return err
		}
		if (len(routes) == 0) != (want == "") || (len(costs) == 0) != (want == "") || view[0].UnitValue != want || page[0].UnitValue != want {
			w.t.Errorf("want unit value %q: routes %d costs %d view %q page %q", want, len(routes), len(costs), view[0].UnitValue, page[0].UnitValue)
		}
		return nil
	})
}
