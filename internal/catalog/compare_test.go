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

func TestLargestChangeAndSmallestMargin(t *testing.T) {
	plain := func(input, output string) Schedule {
		t.Helper()
		s, err := ParseSchedule(fmt.Appendf(nil, `{"v":1,"currency":"USD","rates":{"input":%q,"output":%q}}`, input, output))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	inputOnly, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000001"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		old, next Schedule
		change    string // "" is unmeasured
		cost      Schedule
		margin    string // "" is unmeasured
	}{
		{"unchanged", plain("0.000001", "0.000002"), plain("0.000001", "0.000002"), "0", plain("0.0000005", "0.000001"), "1/2"},
		{"largest move wins, either way", plain("0.000001", "0.000002"), plain("0.0000012", "0.000001"), "1/2", plain("0.0000006", "0.0000009"), "1/10"},
		// Above 128 tokens the next prices change by a quarter; the base band stays the same.
		{"a band only one side has", compareSchedule(t, 1000, "0.000001", "0.000001"), compareSchedule(t, 128, "0.000001", "0.00000125"), "1/4", compareSchedule(t, 128, "0.000001", "0.000001"), "0"},
		{"zero to a price", plain("0", "0.000002"), plain("0.000001", "0.000002"), "", plain("0", "0.000001"), "1/2"},
		{"a price lost", plain("0.000001", "0.000002"), inputOnly, "", plain("0.000001", "0.000001"), ""},
		{"a loss", plain("0.000001", "0.000001"), plain("0.000001", "0.000001"), "0", plain("0.000002", "0.000001"), "-1"},
		{"no cost under a zero price", plain("0", "0"), plain("0", "0"), "0", plain("0", "0.000001"), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			change, measured, err := c.old.LargestChange(c.next)
			if err != nil || measured != (c.change != "") || measured && change.RatString() != c.change {
				t.Fatalf("change %v measured %v err %v, want %q", change, measured, err, c.change)
			}
			margin, measured, err := c.next.SmallestMargin(c.cost)
			if err != nil || measured != (c.margin != "") || measured && margin.RatString() != c.margin {
				t.Fatalf("margin %v measured %v err %v, want %q", margin, measured, err, c.margin)
			}
		})
	}
}
