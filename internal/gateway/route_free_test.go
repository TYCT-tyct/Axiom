package gateway

import (
	"errors"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// freeReference prices every item specAll reports at zero, as a :free entry's reference price must.
const freeReference = `{"v":1,"currency":"USD","rates":{"input":"0","output":"0","cache_read":"0","cache_write":"0"}}`

// TestRouteBuildLeavesOutAFreeModelWithAPaidSale: the :free suffix promises no charge, so an already published paid
// price for any audience keeps the model out of routing rather than serving a call that charges.
func TestRouteBuildLeavesOutAFreeModelWithAPaidSale(t *testing.T) {
	f := newRouteFixture(t)
	free := f.seedPrice("acme/served:free", specAll, freeReference)
	paid, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"}}`))
	if err != nil {
		t.Fatal(err)
	}
	f.prices[free] = billing.Sold{Version: uuid.NewV7(), Schedule: paid}
	r := routable(free, protocol.Messages)
	f.build([]suppliers.Routable{r})
	if want := (Count{Endpoints: 0, Offered: 1}); f.count != want {
		t.Errorf("the build made %+v, want %+v", f.count, want)
	}
	if logs := f.logs.String(); !strings.Contains(logs, "non-zero sale price") {
		t.Errorf("the log does not say why:\n%s", logs)
	}
}

// TestRouteBuildDropsARetiredFreeModel: a supplier goes on mapping a :free model, and its sale price stays zero, after
// the catalog retires it, so what leaves the endpoint out is the catalog's status and not a missing mapping or price.
// The snapshot is the only source the routers that choose a model themselves rank from, so neither openrouter/auto nor
// openrouter/free then has anything to plan, and the log names the reason.
func TestRouteBuildDropsARetiredFreeModel(t *testing.T) {
	f := newRouteFixture(t)
	f.seedPrice("acme/free", specAll, reference) // the :free entry needs a listed base model with a public name
	free := f.seedPrice("acme/free:free", specAll, freeReference)
	prices, err := f.fixturePrices() // the fixture priced what it seeded before this test seeded more
	if err != nil {
		t.Fatal(err)
	}
	f.prices = prices
	r := routable(free, protocol.Messages)
	f.build([]suppliers.Routable{r})
	if want := (Count{Endpoints: 1, Offered: 1}); f.count != want {
		t.Fatalf("the build made %+v, want %+v", f.count, want)
	}
	if _, err := f.cat.Retire(t.Context(), catalog.ModelRequest{
		Actor: audit.System("gateway.test"), Key: uuid.NewV7(), Reason: "test", Model: free, Rev: 1,
	}); err != nil {
		t.Fatal(err)
	}
	f.build([]suppliers.Routable{r})
	if want := (Count{Endpoints: 0, Offered: 1}); f.count != want {
		t.Errorf("the rebuild made %+v, want %+v", f.count, want)
	}
	auto, freeIn := call(free), call(free)
	auto.Auto, auto.Models = true, nil
	freeIn.Free, freeIn.Models = true, nil
	for _, router := range []struct {
		name string
		in   routing.Input
	}{{"openrouter/auto", auto}, {"openrouter/free", freeIn}} {
		var none *routing.NoEndpoint
		if _, wait, err := f.router.Plan(router.in); wait != nil || !errors.As(err, &none) || !none.Permanent {
			t.Errorf("%s after retirement: wait %v, error %v; want a permanent no endpoint", router.name, wait, err)
		}
	}
	if logs := f.logs.String(); !strings.Contains(logs, "the catalog model is not listed") {
		t.Errorf("the log does not say the retired model is left out:\n%s", logs)
	}
}
