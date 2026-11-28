package billing

import (
	"strings"
	"testing"
	"uuid"
)

// TestAStandardOnlyCostSetsNoFloorAtOtherTiers: an aggregator's endpoint is costed and served at the standard tier only
// (PriceCost.StandardOnly), so the flex tier its plan lacks is neither missing from its cost nor a floor under the
// model's shared price: the other supplier keeps selling the model at its flex price. The same plan without the flag is
// a cost that leaves the model's flex unpriced, and a flex cost of an endpoint's own above the price withholds it.
func TestAStandardOnlyCostSetsNoFloorAtOtherTiers(t *testing.T) {
	t.Parallel()
	f := newSnapshotter(t)
	f.spec = strings.Replace(saleSpec, `"tiers":[]`, `"tiers":["flex"]`, 1)
	f.reference = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"},` +
		`"service_tiers":{"flex":{"rates":{"input":"0.0000005","output":"0.000001"}}}}`
	model := f.model("acme/tiered")
	m, ok := f.snapshot().Model(model)
	if !ok {
		t.Fatal("the model is not in the catalog")
	}
	sale := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.00001"},
		"service_tiers":{"flex":{"rates":{"input":"0.000001","output":"0.000004"}}}}`)
	other := priceCost(t, model, "0.000001")
	other.Schedule = mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"0.000001","output":"0.000002"},
		"service_tiers":{"flex":{"rates":{"input":"0.0000005","output":"0.000001"}}}}`)
	for _, c := range []struct {
		name, schedule string
		standardOnly   bool
		kept           bool
	}{
		{"a standard-only ceiling", `{"v":1,"currency":"XXX","rates":{"input":"0.000002","output":"0.000008"}}`, true, true},
		{"a cost that leaves flex unpriced", `{"v":1,"currency":"XXX","rates":{"input":"0.000002","output":"0.000008"}}`, false, false},
		{"a flex cost of the endpoint's own above the price", `{"v":1,"currency":"XXX","rates":{"input":"0.000002","output":"0.000008"},
			"service_tiers":{"flex":{"rates":{"input":"0.000002","output":"0.000008"}}}}`, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cost := priceCost(t, model, "0.000002")
			cost.Schedule, cost.StandardOnly = mustSchedule(t, c.schedule), c.standardOnly
			sold := Sold{Version: uuid.NewV7(), Schedule: sale, floor: "1"}
			if got := sold.WithCosts(m, []PriceCost{other, cost}).Available(); got != c.kept {
				t.Errorf("the shared price is kept: %v, want %v", got, c.kept)
			}
		})
	}
}
