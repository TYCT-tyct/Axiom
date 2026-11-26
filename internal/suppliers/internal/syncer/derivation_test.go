package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/suppliers/internal/adapter"
)

// TestAPlanKeepsItsFactors: a plan stores what its prices are the product of — a confirmed base's price and the group's
// ratio, or what the adapter handed over — and the factors multiply out to the stored price; a factor that does not is
// dropped and the price stands. A plan without factors is named by its schedule alone, the fingerprint it had before
// plans carried factors, and the same prices with factors are another version of the plan.
func TestAPlanKeepsItsFactors(t *testing.T) {
	t.Parallel()
	published, err := catalog.ParseSchedule([]byte(`{"currency":"XXX","v":1,"rates":{"input":"0.15","output":"0.6"}}`))
	if err != nil {
		t.Fatal(err)
	}
	ratios := adapter.Derive(published, func(catalog.ScheduleRate) map[adapter.FactorKind]*big.Rat {
		return map[adapter.FactorKind]*big.Rat{adapter.FactorModelRatio: big.NewRat(1, 10), adapter.FactorGroupRatio: big.NewRat(3, 2)}
	}, []adapter.FactorKind{adapter.FactorModelRatio, adapter.FactorGroupRatio})
	right := adapter.Derive(published, func(r catalog.ScheduleRate) map[adapter.FactorKind]*big.Rat {
		f := map[adapter.FactorKind]*big.Rat{adapter.FactorModelRatio: big.NewRat(1, 10), adapter.FactorGroupRatio: big.NewRat(3, 2)}
		if r.Item == catalog.ItemOutput {
			f[adapter.FactorCompletionRatio] = big.NewRat(4, 1)
		}
		return f
	}, []adapter.FactorKind{adapter.FactorModelRatio, adapter.FactorGroupRatio, adapter.FactorCompletionRatio})
	wrong := adapter.Derivation{{ServiceTier: "standard", Item: "input", Factors: []adapter.Factor{{Kind: adapter.FactorModelRatio, Value: "7"}}}}
	cases := []struct {
		name      string
		model     adapter.Model
		confirmed bool
		factors   bool // the plan stores factors
	}{
		{"published without factors", adapter.Model{UpstreamName: "m1", Cost: &published}, false, false},
		{"published with factors", adapter.Model{UpstreamName: "m1", Cost: &published, Derivation: right}, false, true},
		// Only the input is 0.1 × 1.5: the output's factors do not multiply out to 0.6, so none are kept.
		{"published with factors that do not multiply out", adapter.Model{UpstreamName: "m1", Cost: &published, Derivation: ratios}, false, false},
		{"published with factors of another schedule", adapter.Model{UpstreamName: "m1", Cost: &published, Derivation: wrong}, false, false},
		{"confirmed", adapter.Model{UpstreamName: "m1"}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := healthy()
			m.offer = offerOf(tc.model, "1.5")
			e, st, _ := engine(m)
			if tc.confirmed {
				st.state.Confirmed = map[string]string{"m1": confirmedBase}
			}
			round(t, e)
			if len(st.plans) != 1 {
				t.Fatalf("%d plans, want 1", len(st.plans))
			}
			p := st.plans[0]
			s, err := catalog.ParseSchedule([]byte(p.Schedule))
			if err != nil {
				t.Fatal(err)
			}
			d, err := adapter.ParseDerivation(p.Derivation)
			if err != nil || (d != nil) != tc.factors {
				t.Fatalf("derivation %q (%v), want factors %t", p.Derivation, err, tc.factors)
			}
			if err := d.Check(s); tc.factors && err != nil {
				t.Error(err)
			}
			if sum := sha256.Sum256([]byte(p.Schedule)); (p.Fingerprint == hex.EncodeToString(sum[:8])) == tc.factors {
				t.Errorf("fingerprint %s: named by the schedule alone %t, want %t", p.Fingerprint, !tc.factors, !tc.factors)
			}
		})
	}
	// The confirmed plan's factors: the base's price, then the group's ratio.
	got := adapter.ScaledDerivation(mustSchedule(t, confirmedBase), "1.5")
	want := []adapter.Factor{{Kind: adapter.FactorBasePrice, Value: "0.000015"}, {Kind: adapter.FactorGroupRatio, Value: "1.5"}}
	if len(got) != 2 || got[1].Item != "output" || len(got[1].Factors) != 2 || got[1].Factors[0] != want[0] || got[1].Factors[1] != want[1] {
		t.Errorf("the confirmed plan's derivation %+v, want the output's to be %v", got, want)
	}
}

// TestFactorsMultiplyOutAcrossBands: a confirmed base with a context tier and a ratio whose products need rounding
// keeps factors for every rate of every band, each multiplying out to the stored price within the plan's rounding.
func TestFactorsMultiplyOutAcrossBands(t *testing.T) {
	t.Parallel()
	base := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000003","cache_read":"0.0000003","output":"0.000015"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":200000,"rates":{"input":"0.000006","output":"0.0000225"}}]}}`)
	for _, ratio := range []adapter.Amount{"1", "0.333", "1.37", "12.5"} {
		scaled, err := adapter.ScaleSchedule(base, ratio)
		if err != nil {
			t.Fatal(err)
		}
		d := adapter.ScaledDerivation(base, ratio)
		if len(d) != len(scaled.View().Rates) {
			t.Fatalf("ratio %s: %d derived rates for %d", ratio, len(d), len(scaled.View().Rates))
		}
		if err := d.Check(scaled); err != nil {
			t.Errorf("ratio %s: %v", ratio, err)
		}
	}
}

func mustSchedule(t *testing.T, text string) catalog.Schedule {
	t.Helper()
	s, err := catalog.ParseSchedule([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// offerOf is an offer of one group of the ratio with the model in it.
func offerOf(m adapter.Model, ratio adapter.Amount) func(adapter.Listing) (adapter.Page[adapter.Group], error) {
	return func(adapter.Listing) (adapter.Page[adapter.Group], error) {
		return adapter.Page[adapter.Group]{Done: true, Items: []adapter.Group{{
			Ref: "1", Name: "g", Ratio: ratio, Facts: adapter.GroupFacts(`{}`), Models: []adapter.Model{m},
		}}}, nil
	}
}
