package suppliers

import (
	"context"
	"math/big"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
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
	cost := w.tieredCost()
	if len(cost.Lines) != 6 || cost.UnitValue == nil || cost.UnitValue.String() != "0.000002137" {
		t.Fatalf("the cost %+v, want 6 lines (3 items in 2 bands) at the newest unit value", cost)
	}
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	receipt := a.Done()
	uv := cost.UnitValue.Rat()
	for _, l := range cost.Lines {
		expectPrice(t, l, uv)
		counts := attemptAt(l)
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, got, known, err := w.svc.Credits(receipt, []catalog.Usage{u}, receipt.currency)
		if want := linesCharge(t, cost, l.Above, counts); err != nil || !known || got != want {
			t.Errorf("%s above %d: charged %d (%v, %v), the lines say %d", l.Item, l.Above, got, known, err, want)
		}
	}
}

// expectPrice checks that a line's price is its native price times the unit value, rounded up to 15 decimals.
func expectPrice(t *testing.T, l CostLine, uv *big.Rat) {
	t.Helper()
	exact := new(big.Rat).Mul(l.Native.Rat(), uv)
	if l.Price == nil || l.Price.Rat().Cmp(exact) < 0 || new(big.Rat).Sub(l.Price.Rat(), exact).Cmp(big.NewRat(1, 1e15)) >= 0 {
		t.Errorf("%s above %d: price %v, want %s rounded up to 15 decimals", l.Item, l.Above, l.Price, exact.FloatString(30))
	}
}

// tieredCost gives the world's endpoint a plan with a context tier and a unit value whose products carry more decimals
// than a unit price, and reads its price lines.
func (w *world) tieredCost() EndpointCost {
	w.t.Helper()
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
	w.addValue("0.000002137")
	if err := w.svc.Reload(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	var cost EndpointCost
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		cost, err = w.adminOf().EndpointCost(ctx, tx, w.supplier, w.endpoint, PlatformOwned)
		return err
	})
	return cost
}

// attemptAt is the usage of an attempt at a line's condition: 777 units of its item, with the input that takes it past
// the line's threshold (the plan's basis is the input).
func attemptAt(l CostLine) map[catalog.Item]int64 {
	counts := map[catalog.Item]int64{l.Item: 777}
	if !l.Base {
		counts[catalog.ItemInput] += l.Above + 1
	}
	return counts
}

// linesCharge is what the lines in force above the threshold say the counts cost, in billionths of the settlement
// currency, rounded up once.
func linesCharge(t *testing.T, cost EndpointCost, above int64, counts map[catalog.Item]int64) int64 {
	t.Helper()
	sum := new(big.Rat)
	for it, n := range counts {
		found := false
		for _, l := range cost.Lines {
			if l.Item == it && l.Above == above {
				sum.Add(sum, new(big.Rat).Mul(l.Native.Rat(), big.NewRat(n, 1)))
				found = true
			}
		}
		if !found {
			t.Fatalf("no line of %s above %d", it, above)
		}
	}
	billionths := sum.Mul(sum, cost.UnitValue.Rat()).Mul(sum, big.NewRat(1e9, 1))
	q, r := new(big.Int).QuoRem(billionths.Num(), billionths.Denom(), new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

// TestEndpointCostLinesShowTheirFactors: a line of a plan stored with its factors carries them, in order, and they
// multiply out to the line's native price within the plan's own rounding (up, once, to a unit price), across bands; a
// plan stored without factors (one from before plans kept them) has lines with none.
func TestEndpointCostLinesShowTheirFactors(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	old := w.tieredCost()
	cost := w.confirmedCost("1.37")
	if len(cost.Lines) != len(old.Lines) {
		t.Fatalf("%d lines, want %d", len(cost.Lines), len(old.Lines))
	}
	for i, l := range cost.Lines {
		if len(old.Lines[i].Factors) != 0 {
			t.Errorf("a plan stored without factors has %v on %s", old.Lines[i].Factors, l.Item)
		}
		if len(l.Factors) != 2 || l.Factors[0].Kind != "base_price" || l.Factors[1] != (CostFactor{Kind: "group_ratio", Value: "1.37"}) {
			t.Fatalf("%s above %d: factors %v, want the base price then the group ratio", l.Item, l.Above, l.Factors)
		}
		product := productOf(t, l.Factors)
		if gap := new(big.Rat).Sub(l.Native.Rat(), product); gap.Sign() < 0 || gap.Cmp(big.NewRat(1, 1e15)) >= 0 {
			t.Errorf("%s above %d: the factors multiply out to %s, the native price is %s", l.Item, l.Above, product.FloatString(18), l.Native)
		}
		expectPrice(t, l, cost.UnitValue.Rat())
	}
}

// confirmedCost gives the world's endpoint the plan tieredCost's schedule makes as a confirmed base at the group ratio,
// stored with its factors as the sync stores them, and reads its price lines.
func (w *world) confirmedCost(ratio adapter.Amount) EndpointCost {
	w.t.Helper()
	base, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"XXX","rates":{"input":"0.075","cache_read":"0.0075","output":"0.3"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":1000,"rates":{"input":"0.15","output":"0.45"}}]}}`))
	if err != nil {
		w.t.Fatal(err)
	}
	scaled, err := adapter.ScaleSchedule(base, ratio)
	if err != nil {
		w.t.Fatal(err)
	}
	derivation, err := adapter.ScaledDerivation(base, ratio).Text()
	if err != nil {
		w.t.Fatal(err)
	}
	schedule, err := scaled.MarshalJSON()
	if err != nil {
		w.t.Fatal(err)
	}
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		plan, err := store.New().EnsureCostSchedule(ctx, tx, store.EnsureCostScheduleParams{
			SupplierID: w.supplier, EndpointID: w.endpoint, NativeUnit: "quota", Basis: "confirmed", Fingerprint: "v3",
			Schedule: schedule, Derivation: derivation,
		})
		if err != nil {
			return err
		}
		return store.New().SetEndpointCost(ctx, tx, store.SetEndpointCostParams{ID: w.endpoint, CostScheduleID: &plan})
	})
	var cost EndpointCost
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		cost, err = w.adminOf().EndpointCost(ctx, tx, w.supplier, w.endpoint, PlatformOwned)
		return err
	})
	return cost
}

// productOf is the exact product of the factors.
func productOf(t *testing.T, factors []CostFactor) *big.Rat {
	t.Helper()
	product := big.NewRat(1, 1)
	for _, f := range factors {
		v, ok := new(big.Rat).SetString(f.Value)
		if !ok {
			t.Fatalf("factor %v", f)
		}
		product.Mul(product, v)
	}
	return product
}
