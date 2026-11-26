package suppliers

import (
	"context"
	"math/big"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

// TestEndpointCostIsWhatAnAttemptIsCharged: the invariant of EndpointCost. For each line, an attempt of n units of its
// item at its condition (above its threshold, the basis passed) is charged by Credits exactly n × native price × unit
// value of the lines in force there, summed and rounded up to a billionth once; and each line's price is its native
// price times the unit value, rounded up to the 15 decimals of a unit price. The products here are computed apart from
// the code under test.
func TestEndpointCostIsWhatAnAttemptIsCharged(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		plan, err := store.New().InsertCostSchedule(ctx, tx, store.InsertCostScheduleParams{
			SupplierID: w.supplier, EndpointID: w.endpoint, NativeUnit: "quota", Basis: "published", Fingerprint: "v2",
			Schedule: []byte(`{"v":1,"currency":"XXX","rates":{"input":"0.075","cache_read":"0.0075","output":"0.3"},
				"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":1000,"rates":{"input":"0.15","output":"0.45"}}]}}`),
		})
		if err != nil {
			return err
		}
		return store.New().SetEndpointCost(ctx, tx, store.SetEndpointCostParams{ID: w.endpoint, CostScheduleID: &plan})
	})
	w.addValue("0.000002137") // a value whose products carry more decimals than a unit price
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	var cost EndpointCost
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		cost, err = w.adminOf().EndpointCost(ctx, tx, w.supplier, w.endpoint, PlatformOwned)
		return err
	})
	if len(cost.Lines) != 6 || cost.UnitValue == nil || cost.UnitValue.String() != "0.000002137" {
		t.Fatalf("the cost %+v, want 6 lines (3 items in 2 bands) at the newest unit value", cost)
	}
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	receipt := a.Done()
	uv := cost.UnitValue.Rat()
	native := func(band int64, it catalog.Item) *big.Rat {
		for _, l := range cost.Lines {
			if l.Item == it && l.Above == band {
				return l.Native.Rat()
			}
		}
		t.Fatalf("no line of %s above %d", it, band)
		return nil
	}
	for _, l := range cost.Lines {
		exact := new(big.Rat).Mul(l.Native.Rat(), uv)
		if l.Price == nil || l.Price.Rat().Cmp(exact) < 0 || new(big.Rat).Sub(l.Price.Rat(), exact).Cmp(big.NewRat(1, 1e15)) >= 0 {
			t.Errorf("%s above %d: price %v, want %s rounded up to 15 decimals", l.Item, l.Above, l.Price, exact.FloatString(30))
		}
		counts := map[catalog.Item]int64{l.Item: 777}
		if !l.Base && l.Item != catalog.ItemInput {
			counts[catalog.ItemInput] = l.Above + 1
		} else if !l.Base {
			counts[catalog.ItemInput] = l.Above + 777
		}
		want := new(big.Rat)
		for it, n := range counts {
			want.Add(want, new(big.Rat).Mul(native(l.Above, it), big.NewRat(n, 1)))
		}
		want.Mul(want, uv)
		billionths := new(big.Rat).Mul(want, big.NewRat(1e9, 1))
		ceil := new(big.Int).Quo(billionths.Num(), billionths.Denom())
		if new(big.Rat).SetInt(ceil).Cmp(billionths) < 0 {
			ceil.Add(ceil, big.NewInt(1))
		}
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, got, known, err := w.svc.Credits(receipt, []catalog.Usage{u}, receipt.currency)
		if err != nil || !known || got != ceil.Int64() {
			t.Errorf("%s above %d: charged %d (%v, %v), the lines say %d", l.Item, l.Above, got, known, err, ceil.Int64())
		}
	}
}
