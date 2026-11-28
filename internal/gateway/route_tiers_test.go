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

// TestRouteBuildKeepsACopiedTierFromAnEndpointItWouldSellAtALoss: an aggregator's endpoint prices flex only by
// copying its standard ceiling (CopiedTiers), which sets no floor under the shared price. Where the shared flex price
// is below that copy, a flex call goes to the other supplier, never to the aggregator; a standard call, which the
// shared price covers there, still may.
func TestRouteBuildKeepsACopiedTierFromAnEndpointItWouldSellAtALoss(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	tiered := f.seedPrice("acme/tiered", strings.Replace(specAll, `"tiers":["standard"]`, `"tiers":["standard","flex"]`, 1),
		strings.Replace(reference, `}}`, `},"service_tiers":{"flex":{"rates":{"input":"0.000001","output":"0.000005","cache_read":"0.0000001","cache_write":"0.00000125"}}}}`, 1))
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
	f.prices[tiered] = billing.Sold{Version: uuid.NewV7(), Schedule: schedule(`{"v":1,"currency":"USD",` + standard +
		`,"service_tiers":{"flex":{"rates":{"input":"0.0000015","output":"0.0000075","cache_read":"0.00000015","cache_write":"0.000001875"}}}}`)}
	value, err := platform.ParseUnitPrice("1")
	if err != nil {
		t.Fatal(err)
	}
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	costed := func(cost string, copied catalog.ServiceTierSet) suppliers.Routable {
		r := routable(tiered, protocol.Messages)
		r.Schedule, r.CopiedTiers, r.Currency, r.UnitValue, r.UnitValueID = schedule(cost), copied, usd, value, uuid.NewV7()
		return r
	}
	const half = `"rates":{"input":"0.000001","output":"0.000005","cache_read":"0.0000001","cache_write":"0.0000012"}`
	other := costed(`{"v":1,"currency":"USD",`+half+`,"service_tiers":{"flex":{`+half+`}}}`, 0)
	const ceiling = `"rates":{"input":"0.000002","output":"0.00001","cache_read":"0.0000002","cache_write":"0.0000025"}`
	aggregator := costed(`{"v":1,"currency":"USD",`+ceiling+`,"service_tiers":{"flex":{`+ceiling+`}}}`, 1<<catalog.ServiceTierFlex)
	f.build([]suppliers.Routable{other, aggregator})
	flex := f.planned(call(tiered, func(r *protocol.Request) { r.ServiceTiers = 1 << catalog.ServiceTierFlex }))
	if len(flex) != 1 || flex[0] != other.ID {
		t.Errorf("a flex call was planned to %v, want only the other supplier %s", flex, other.ID)
	}
	if standardCall := f.planned(call(tiered)); !slices.Contains(standardCall, aggregator.ID) {
		t.Errorf("a standard call was planned to %v, without the aggregator %s", standardCall, aggregator.ID)
	}
}
