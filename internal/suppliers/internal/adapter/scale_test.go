package adapter

import (
	"math/big"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// TestScaleScheduleScalesEveryPriceAndMarksTheUnit: every price, in every tier and service level, is multiplied and rounded up,
// and the currency becomes "XXX" because the amounts are the supplier's own unit.
func TestScaleScheduleScalesEveryPriceAndMarksTheUnit(t *testing.T) {
	t.Parallel()
	official, err := catalog.ParseSchedule([]byte(`{"currency":"USD","v":1,
		"rates":{"input":"0.000003","output":"0.000015","cache_read":"0.0000003"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":200000,"rates":{"input":"0.000006","output":"0.0000225"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	draft, err := ScaleSchedule(official, "1.5")
	if err != nil {
		t.Fatal(err)
	}
	want, err := catalog.ParseSchedule([]byte(`{"currency":"XXX","v":1,
		"rates":{"input":"0.0000045","output":"0.0000225","cache_read":"0.00000045"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":200000,"rates":{"input":"0.000009","output":"0.00003375"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := draft.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	exp, err := want.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(exp) {
		t.Errorf("draft = %s\nwant    %s", got, exp)
	}
	if _, err := ScaleSchedule(official, "-1"); err == nil {
		t.Error("a negative multiplier was taken")
	}
}

// TestScaleScheduleRoundsUp: a price that does not divide at the finest place a schedule holds (10⁻¹⁵) is rounded up,
// never down, and by less than one place: a cost plan below the cost is the mistake to avoid. The figures are checked
// against the exact fraction, and one is spelled out.
func TestScaleScheduleRoundsUp(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ price, multiplier, want string }{
		{"0.000003", "0.3333333333333333333", "0.000001"},       // 9.999999999999999999e-7 up to 10⁻⁶
		{"0.000000001", "0.0000005", "0.000000000000001"},       // 5e-16, below one place: up to one place
		{"0.000002", "0.1234567891234567", "0.000000246913579"}, // 2.469135782469134e-7 up
		{"0.000004", "0.25", "0.000001"},                        // divides exactly: unchanged
	} {
		official, err := catalog.ParseSchedule([]byte(`{"currency":"USD","v":1,"rates":{"input":"` + c.price + `"}}`))
		if err != nil {
			t.Fatal(err)
		}
		scaled, err := ScaleSchedule(official, Amount(c.multiplier))
		if err != nil {
			t.Fatal(err)
		}
		b, err := scaled.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Rates map[string]string `json:"rates"`
		}
		if err := jsonx.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		got, exact := new(big.Rat), new(big.Rat)
		got.SetString(doc.Rates["input"])
		exact.SetString(c.price)
		m, _ := new(big.Rat).SetString(c.multiplier)
		exact.Mul(exact, m)
		place := new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(15), nil))
		if doc.Rates["input"] != c.want || got.Cmp(exact) < 0 || new(big.Rat).Sub(got, exact).Cmp(place) >= 0 {
			t.Errorf("%s × %s = %s (exact %s); want %s, not below the exact value and within one place of it", c.price, c.multiplier, doc.Rates["input"], exact.FloatString(24), c.want)
		}
	}
}
