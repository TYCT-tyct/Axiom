package gateway

import (
	"math/big"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// TestByokFeeAndPreference: an account's own key is priced at the site's share of the model's reference price, one
// scaled schedule that the reservation and the settlement both use, and it is tried before the site's endpoints.
//
//nolint:cyclop // the assertions are one price comparison each
func TestByokFeeAndPreference(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	f.byokFee = "0.05"
	own := routable(f.served, protocol.Messages, protocol.Responses)
	own.Byok, own.ByokFallback = true, true
	shared := routable(f.served, protocol.Messages, protocol.Responses)
	f.build([]suppliers.Routable{shared, own})

	plan, wait, err := f.router.Plan(call(f.served))
	if err != nil || wait != nil {
		t.Fatalf("Plan = %v, %v", wait, err)
	}
	if len(plan.Entries) != 2 || uuid.UUID(plan.Entries[0].Endpoint) != own.ID {
		t.Fatalf("the plan is %+v, want the account's key %s first and the site's endpoint after it", plan.Entries, own.ID)
	}
	snap, err := f.cat.Current()
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := snap.ReferencePrice(f.served, "USD")
	if !ok {
		t.Fatal("the fixture model has no reference price")
	}
	base, ok := ref.Schedule.BaseRate(catalog.ItemInput)
	if !ok {
		t.Fatal("the fixture price has no input rate")
	}
	want, err := base.Scale(big.NewRat(5, 100))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := plan.Entries[0].Price.Schedule.BaseRate(catalog.ItemInput); got.Cmp(want) != 0 {
		t.Errorf("the key's input price is %s, want 5%% of %s = %s", got, base, want)
	}
	saleBase, ok := f.prices[f.served].Schedule.BaseRate(catalog.ItemInput)
	if !ok {
		t.Fatal("the sale price has no input rate")
	}
	if got, _ := plan.Entries[1].Price.Schedule.BaseRate(catalog.ItemInput); got.Cmp(saleBase) != 0 {
		t.Errorf("the site's endpoint is priced %s, want the published %s", got, saleBase)
	}
	if !plan.Entries[0].Byok || plan.Entries[1].Byok {
		t.Errorf("the entries' byok flags are %v, %v", plan.Entries[0].Byok, plan.Entries[1].Byok)
	}
}

// TestByokNoSharedFallback: a key that forbids the site's endpoints leaves them out of the plan for a model it covers.
func TestByokNoSharedFallback(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	own := routable(f.served, protocol.Messages, protocol.Responses)
	own.Byok, own.ByokFallback = true, false
	shared := routable(f.served, protocol.Messages, protocol.Responses)
	f.build([]suppliers.Routable{shared, own})
	plan, wait, err := f.router.Plan(call(f.served))
	if err != nil || wait != nil {
		t.Fatalf("Plan = %v, %v", wait, err)
	}
	if len(plan.Entries) != 1 || uuid.UUID(plan.Entries[0].Endpoint) != own.ID {
		t.Fatalf("the plan is %+v, want only the account's key %s", plan.Entries, own.ID)
	}
}

// TestByokPriceWithoutSharedSale: the account's key may serve a model the site does not sell. Without this, filtering
// the key's zero cost out of the shared price evidence would also take the key's only model out of routing.
func TestByokPriceWithoutSharedSale(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	snap, err := f.cat.Current()
	if err != nil {
		t.Fatal(err)
	}
	own := routable(f.served, protocol.Messages, protocol.Responses)
	own.Byok = true
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	e, why := endpointOf(snap, nil, nil, "0.05", nil, usd, own, f.health.MeasuredFor(own, time.Now()))
	if why != "" {
		t.Fatalf("pricing a key without a shared sale price: %s", why)
	}
	if e.Price.Schedule.IsZero() {
		t.Error("the key's fee is zero, want the reference price times 5%")
	}
}

// TestByokPriceKeepsTheKeysEndpointWhenTheSitePricesEachEndpoint: a version that prices the site's endpoints one by one
// (billing.Sold.Endpoints) has no price for the account's own endpoint, which is not in that table. The key's fee is
// the whole of its price, so the endpoint must still be routed, at the reference price times the fee.
//
//nolint:cyclop // the assertions are one price comparison each
func TestByokPriceKeepsTheKeysEndpointWhenTheSitePricesEachEndpoint(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	f.byokFee = "0.05"
	shared := routable(f.served, protocol.Messages, protocol.Responses)
	own := routable(f.served, protocol.Messages, protocol.Responses)
	own.Byok, own.ByokFallback = true, true
	sold := f.prices[f.served]
	sold.Endpoints = billing.EndpointPrices{shared.ID: sold.Schedule}
	f.prices[f.served] = sold
	f.build([]suppliers.Routable{shared, own})

	plan, wait, err := f.router.Plan(call(f.served))
	if err != nil || wait != nil {
		t.Fatalf("Plan = %v, %v", wait, err)
	}
	if len(plan.Entries) != 2 || uuid.UUID(plan.Entries[0].Endpoint) != own.ID {
		t.Fatalf("the plan is %+v, want the account's key %s first and the site's endpoint after it", plan.Entries, own.ID)
	}
	snap, err := f.cat.Current()
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := snap.ReferencePrice(f.served, "USD")
	if !ok {
		t.Fatal("the fixture model has no reference price")
	}
	base, ok := ref.Schedule.BaseRate(catalog.ItemInput)
	if !ok {
		t.Fatal("the fixture price has no input rate")
	}
	want, err := base.Scale(big.NewRat(5, 100))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := plan.Entries[0].Price.Schedule.BaseRate(catalog.ItemInput); got.Cmp(want) != 0 {
		t.Errorf("the key's input price is %s, want 5%% of %s = %s", got, base, want)
	}
	if plan.Entries[0].Price.Version != ref.ID {
		t.Errorf("the key's price version is %s, want the reference's %s", plan.Entries[0].Price.Version, ref.ID)
	}
	saleBase, ok := sold.Schedule.BaseRate(catalog.ItemInput)
	if !ok {
		t.Fatal("the published price has no input rate")
	}
	if got, _ := plan.Entries[1].Price.Schedule.BaseRate(catalog.ItemInput); got.Cmp(saleBase) != 0 {
		t.Errorf("the site's endpoint is priced %s, want the published %s", got, saleBase)
	}
}

// TestByokKeepsTheEndpointsAttributes: an account's own key changes the credential, not the endpoint. Its source class
// is what fingerprints decide, the relay fallback before any rule matched, and its data policy the worst case, as a
// site endpoint's; so a request that wants only official supply, or no retention, does not get the key on the strength
// of it being the vendor's address or the user's own (docs/domains/routing.md「BYOK（#578）」).
func TestByokKeepsTheEndpointsAttributes(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	f.byokFee = "0.05"
	own := routable(f.served, protocol.Messages, protocol.Responses)
	own.Byok, own.ByokFallback = true, false
	shared := routable(f.served, protocol.Messages, protocol.Responses)
	snap, err := f.cat.Current()
	if err != nil {
		t.Fatal(err)
	}
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []suppliers.Routable{own, shared} {
		e, why := endpointOf(snap, f.prices, nil, "0.05", nil, usd, r, f.health.MeasuredFor(r, time.Now()))
		if why != "" {
			t.Fatalf("building %s: %s", r.ID, why)
		}
		if e.Class != routing.ClassRelay || e.Policy != routing.PolicyMayTrain {
			t.Errorf("byok %v: class %q, policy %d; want the fallback relay and the worst case", r.Byok, e.Class, e.Policy)
		}
	}
	f.build([]suppliers.Routable{shared, own})
	for name, edit := range map[string]func(*routing.Override){
		"only official": func(o *routing.Override) { o.Classes = routing.ClassSet{routing.ClassOfficial} },
		"no retention":  func(o *routing.Override) { o.Policies = noRetention },
	} {
		in := call(f.served)
		edit(&in.Override)
		if plan, _, err := f.router.Plan(in); err == nil {
			t.Errorf("%s: planned %+v, want no endpoint: neither the key nor the site's endpoint is known to qualify", name, plan.Entries)
		}
	}
}
