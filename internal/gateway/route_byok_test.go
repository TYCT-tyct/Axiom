package gateway

import (
	"math/big"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// TestByokFeeAndPreference: an account's own key is priced at the site's share of the model's published price, one
// scaled schedule that the reservation and the settlement both use, and it is tried before the site's endpoints.
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
	base, ok := f.prices[f.served].Schedule.BaseRate(catalog.ItemInput)
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
	if got, _ := plan.Entries[1].Price.Schedule.BaseRate(catalog.ItemInput); got.Cmp(base) != 0 {
		t.Errorf("the site's endpoint is priced %s, want the published %s", got, base)
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
