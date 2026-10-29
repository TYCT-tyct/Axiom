package catalog

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	"testing"
)

func compareSchedule(t *testing.T, threshold int64, base, above string) Schedule {
	t.Helper()
	s, err := ParseSchedule(fmt.Appendf(nil, `{"v":1,"currency":"USD","rates":{"input":%q,"output":%q},"context_tiers":{"method":"whole","basis":["input"],"tiers":[{"above":%d,"rates":{"input":%q,"output":%q}}]}}`, base, base, threshold, above, above))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFloorComparesUnionOfThresholds(t *testing.T) {
	sale := compareSchedule(t, 200000, "0.000001375", "0.00000275")
	cost := compareSchedule(t, 128000, "0.00000125", "0.0000025")
	if safe, err := sale.CoversCost(cost); err != nil || safe {
		t.Fatalf("missed loss at 150k: safe=%v err=%v", safe, err)
	}
	raised, err := sale.Aligned(cost, false)
	if err != nil {
		t.Fatal(err)
	}
	if safe, err := raised.CoversCost(cost); !safe || err != nil {
		t.Fatalf("merged floor unsafe: %v", err)
	}
	if moved, err := sale.PriceChange(raised, big.NewRat(1, 10)); !moved || err != nil {
		t.Fatal("threshold missed the interval above 128k")
	}
}

func TestAlignedFloorProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(234, 28)) //nolint:gosec // reproducible property-test input, not a secret
	for range 500 {
		price := func() string { return new(big.Rat).SetFrac64(int64(rng.IntN(100)), 1000000).FloatString(6) }
		sale := compareSchedule(t, int64(rng.IntN(200)), price(), price())
		cost := compareSchedule(t, int64(rng.IntN(200)), price(), price())
		merged, err := sale.Aligned(cost, false)
		if err != nil {
			t.Fatal(err)
		}
		for length := range int64(203) {
			for _, it := range []Item{ItemInput, ItemOutput, ItemReasoning, ItemImageIn, ItemImageOut} {
				got, _ := merged.price(ServiceTierStandard, merged.bandOf(length), it)
				old, _ := sale.price(ServiceTierStandard, sale.bandOf(length), it)
				floor, _ := cost.price(ServiceTierStandard, cost.bandOf(length), it)
				if got.Cmp(old) < 0 || got.Cmp(floor) < 0 {
					t.Fatalf("length %d %s: %s below sale %s / cost %s", length, it, got, old, floor)
				}
			}
		}
	}
}

func TestPriceChangeZeroAndMissingPrices(t *testing.T) {
	for _, c := range []struct {
		old, next string
		want      bool
	}{
		{"0", "0", false}, {"0", "0.000001", true}, {"0.000001", "0", true}, {"0.000001", "0.0000011", false}, {"0.000001", "0.000001100000001", true},
	} {
		a := compareSchedule(t, 128, c.old, c.old)
		b := compareSchedule(t, 200, c.next, c.next)
		got, err := a.PriceChange(b, big.NewRat(1, 10))
		if err != nil || got != c.want {
			t.Fatalf("%s -> %s: %v/%v", c.old, c.next, got, err)
		}
	}
}
