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

// TestARefreshPricesAFlexModelWithAStandardOnlyEndpoint: the publication refresh of a model that declares flex, served
// by an OpenRouter endpoint costed at the standard tier only (StandardOnly) and by another supplier that prices flex,
// keeps the model's shared price: the OpenRouter plan's missing flex is not a cost the price lacks. The same plan
// without the flag leaves the model unpriced.
func TestARefreshPricesAFlexModelWithAStandardOnlyEndpoint(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name         string
		standardOnly bool
		priced       bool
	}{
		{"a standard-only OpenRouter endpoint", true, true},
		{"a cost that leaves flex unpriced", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &priceFixture{rules: newRules(t), catalog: newSnapshotter(t)}
			f.catalog.spec = strings.Replace(saleSpec, `"tiers":[]`, `"tiers":["flex"]`, 1)
			f.catalog.reference = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"},` +
				`"service_tiers":{"flex":{"rates":{"input":"0.0000005","output":"0.000001"}}}}`
			f.model = f.catalog.model("acme/tiered")
			other := priceCost(t, f.model, "0.0000005")
			other.Schedule = mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"0.0000005","output":"0.000001"},
				"service_tiers":{"flex":{"rates":{"input":"0.0000002","output":"0.0000005"}}}}`)
			ceiling := priceCost(t, f.model, "0.0000008")
			ceiling.StandardOnly = c.standardOnly
			f.costs = []PriceCost{other, ceiling}
			var err error
			if f.rule, err = f.create(rule(Selector{Kind: "all"}, faced("1"))); err != nil {
				t.Fatal(err)
			}
			if got := f.refresh()[f.model].Available(); got != c.priced {
				t.Errorf("the model is priced: %v, want %v (publication %+v)", got, c.priced, f.state())
			}
		})
	}
}
