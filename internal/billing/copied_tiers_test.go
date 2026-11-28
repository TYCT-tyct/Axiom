package billing

import (
	"testing"
	"uuid"

	"axiom/internal/catalog"
)

// TestCopiedTiersAreNoCostFloor: an aggregator's ceiling plan copies its standard prices onto the model's other tiers
// (suppliers' CopiedTiers), which is a bound, not what the tier costs. A shared sale price whose flex price is below the
// copied flex is kept, so the other supplier still sells the model; a flex cost of the endpoint's own above the sale
// price still withholds the shared price, as before.
func TestCopiedTiersAreNoCostFloor(t *testing.T) {
	t.Parallel()
	f := newSnapshotter(t)
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
		name   string
		copied catalog.ServiceTierSet
		kept   bool
	}{
		{"a copied flex ceiling above the flex price", 1 << catalog.ServiceTierFlex, true},
		{"a flex cost of the endpoint's own above it", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ceiling := priceCost(t, model, "0.000002")
			ceiling.Schedule = mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"0.000002","output":"0.000008"},
				"service_tiers":{"flex":{"rates":{"input":"0.000002","output":"0.000008"}}}}`)
			ceiling.CopiedTiers = c.copied
			sold := Sold{Version: uuid.NewV7(), Schedule: sale, floor: "1"}
			if got := sold.WithCosts(m, []PriceCost{other, ceiling}).Available(); got != c.kept {
				t.Errorf("the shared price is kept: %v, want %v", got, c.kept)
			}
		})
	}
}
