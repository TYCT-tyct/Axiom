package gateway

import (
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// TestRouteBuildServesAnAggregatorAtTheStandardTierOnly: OpenRouter lists no price per service tier, so its endpoint is
// priced and served at the standard tier only (suppliers.Routable.StandardOnly). A flex call of a model that another
// supplier also serves goes to that supplier, never to OpenRouter; a standard call may go to OpenRouter; and a flex call
// of a model only OpenRouter serves finds no endpoint, rather than one that would be charged at a tier it has no price
// for.
func TestRouteBuildServesAnAggregatorAtTheStandardTierOnly(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	spec := strings.Replace(specAll, `"tiers":["standard"]`, `"tiers":["standard","flex"]`, 1)
	flexReference := strings.Replace(reference, `}}`,
		`},"service_tiers":{"flex":{"rates":{"input":"0.000001","output":"0.000005","cache_read":"0.0000001","cache_write":"0.00000125"}}}}`, 1)
	tiered := f.seedPrice("acme/tiered", spec, flexReference)
	alone := f.seedPrice("acme/alone", spec, flexReference)
	if err := f.cat.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	schedule := func(s string) catalog.Schedule {
		parsed, err := catalog.ParseSchedule([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	const standard = `"rates":{"input":"0.000003","output":"0.000015","cache_read":"0.0000003","cache_write":"0.00000375"}`
	sale := schedule(`{"v":1,"currency":"USD",` + standard +
		`,"service_tiers":{"flex":{"rates":{"input":"0.0000015","output":"0.0000075","cache_read":"0.00000015","cache_write":"0.000001875"}}}}`)
	f.prices[tiered] = billing.Sold{Version: uuid.NewV7(), Schedule: sale}
	f.prices[alone] = billing.Sold{Version: uuid.NewV7(), Schedule: sale}
	value, err := platform.ParseUnitPrice("1")
	if err != nil {
		t.Fatal(err)
	}
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	costed := func(model catalog.ModelID, cost string, standardOnly bool) suppliers.Routable {
		r := routable(model, protocol.Messages)
		r.Schedule, r.StandardOnly, r.Currency, r.UnitValue, r.UnitValueID = schedule(cost), standardOnly, usd, value, uuid.NewV7()
		return r
	}
	const half = `"rates":{"input":"0.000001","output":"0.000005","cache_read":"0.0000001","cache_write":"0.0000012"}`
	other := costed(tiered, `{"v":1,"currency":"USD",`+half+`,"service_tiers":{"flex":{`+half+`}}}`, false)
	const ceiling = `{"v":1,"currency":"USD","rates":{"input":"0.000002","output":"0.00001","cache_read":"0.0000002","cache_write":"0.0000025"}}`
	aggregator := costed(tiered, ceiling, true)
	f.build([]suppliers.Routable{other, aggregator, costed(alone, ceiling, true)})
	flexCall := func(r *protocol.Request) { r.ServiceTiers = 1 << catalog.ServiceTierFlex }
	if flex := f.planned(call(tiered, flexCall)); len(flex) != 1 || flex[0] != other.ID {
		t.Errorf("a flex call was planned to %v, want only the other supplier %s", flex, other.ID)
	}
	if standardCall := f.planned(call(tiered)); !slices.Contains(standardCall, aggregator.ID) {
		t.Errorf("a standard call was planned to %v, without OpenRouter's %s", standardCall, aggregator.ID)
	}
	if plan, _, err := f.router.Plan(call(alone, flexCall)); err == nil {
		t.Errorf("a flex call of a model only OpenRouter serves was planned to %+v", plan.Entries)
	}
}
