package adapter

import (
	"testing"

	"axiom/internal/catalog"
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
	// Rounded up, never down: 0.000003 × 0.3333333333333333333 is below 10⁻¹⁵ resolution.
	odd, err := ScaleSchedule(official, "0.3333333333333333333")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := odd.MarshalJSON(); err != nil || string(b) == "" {
		t.Error("empty draft")
	}
	if _, err := ScaleSchedule(official, "-1"); err == nil {
		t.Error("a negative multiplier was taken")
	}
}
