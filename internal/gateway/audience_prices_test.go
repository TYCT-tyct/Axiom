package gateway

import (
	"bytes"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

func TestModelDiscoveryUsesAudiencePrices(t *testing.T) {
	f := newRouteFixture(t)
	base := f.prices[f.served]
	dear := base
	var err error
	dear.Schedule, err = base.Schedule.Scaled("2", base.Schedule.Currency())
	if err != nil {
		t.Fatal(err)
	}
	dear.Version = uuid.NewV7()
	account := accounts.AccountID(uuid.NewV7())
	prices := []billing.AudiencePrice{
		{Target: billing.Selector{Kind: "all"}, Sold: base},
		{Audience: billing.Audience{Account: account}, Target: billing.Selector{Kind: "all"}, Sold: dear},
	}
	for _, blocked := range []bool{false, true} {
		if blocked {
			prices[1].Sold = billing.Sold{}
		}
		base.Audiences = billing.CompilePrices(prices)
		f.prices[f.served] = base
		f.build([]suppliers.Routable{routable(f.served, protocol.Messages)})
		audience := billing.Audience{Account: account}
		list := f.models.published()
		assertAudienceMetadata(t, list)
		visibility := visibility{audience: audience}
		raw := list.encode(visibility, orQuery{})
		in := call(f.served)
		in.Admission.Account = routing.AccountID(account)
		plan, _, err := f.router.Plan(in)
		if blocked {
			if err == nil || f.router.ServesFor(f.served, audience) || bytes.Contains(raw, []byte("acme/served")) || list.count(visibility, orQuery{}) != 0 {
				t.Fatal("blocked audience still listed or routed")
			}
			continue
		}
		if err != nil || len(plan.Entries) != 1 {
			t.Fatal(err)
		}
		assertDiscoveryPrice(t, list, visibility, plan, raw)
	}
}

func assertDiscoveryPrice(t *testing.T, list *openRouterListing, visibility visibility, plan routing.Plan, raw []byte) {
	t.Helper()
	model, err := list.find(visibility, "acme/served")
	if err != nil {
		t.Fatal(err)
	}
	prompt, ok := plan.Entries[0].Price.Schedule.BaseRate(catalog.ItemInput)
	if !ok || model.Pricing.Prompt != prompt.String() || !bytes.Contains(raw, []byte(`"prompt":"`+prompt.String()+`"`)) || !bytes.Contains(model.endpoints, []byte(`"prompt":"`+prompt.String()+`"`)) || list.count(visibility, orQuery{}) != 1 {
		t.Fatalf("discovery and charge differ: %s", raw)
	}
}

func assertAudienceMetadata(t *testing.T, list *openRouterListing) {
	t.Helper()
	for _, model := range list.models {
		if model.audiences != nil && (len(model.body) != 0 || len(model.endpoints) != 0 || model.Pricing != (orPricing{}) || model.prompt != (platform.UnitPrice{}) || model.output != (platform.UnitPrice{})) {
			t.Fatal("shared audience metadata contains a price or encoded response")
		}
	}
}
