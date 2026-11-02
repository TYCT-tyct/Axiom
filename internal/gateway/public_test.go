package gateway

import (
	"slices"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// comparePrices lets a diff see through the unexported fields of a price and a currency.
var comparePrices = cmp.Options{
	cmp.Comparer(func(a, b platform.UnitPrice) bool { return a.Cmp(b) == 0 }),
	cmp.Comparer(func(a, b platform.Currency) bool { return a.String() == b.String() }),
}

func TestPublicModelsAreTheServedCatalogAtTheAudiencePrice(t *testing.T) {
	price := func(text string) platform.UnitPrice {
		p, err := platform.ParseUnitPrice(text)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	r := newRig(t)
	got := r.models.Public(billing.Audience{})
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	// beta and claude are served; bare is listed but has no endpoint, so a call to it has no route and the page does not
	// offer it. The order is by author and then public ID, not the catalog's UUID order.
	if want := []string{"acme/alpha", "acme/beta", "anthropic/claude-live"}; !slices.Equal(ids, want) {
		t.Fatalf("models: %v, want %v", ids, want)
	}
	wantPricing := catalog.ScheduleView{Currency: exchangeCurrency(t, "USD"), Rates: []catalog.ScheduleRate{
		{ServiceTier: catalog.ServiceTierStandard, Base: true, Item: catalog.ItemInput, Price: price("0.000001")},
		{ServiceTier: catalog.ServiceTierStandard, Base: true, Item: catalog.ItemCacheWrite, Price: price("0.000001")},
		{ServiceTier: catalog.ServiceTierStandard, Base: true, Item: catalog.ItemOutput, Price: price("0.000002")},
	}}
	want := PublicModel{
		ID: "acme/alpha", Name: "alpha", Author: "acme", ContextLength: 128000, MaxOutput: 1000, Streams: true,
		Abilities: []string{},
		Pricing:   wantPricing,
		// The endpoints are the public groups that serve alpha, in codename order, each with the attributes routing
		// serves and the price this audience pays. The model's context and max output stay the catalog's values; an
		// endpoint may offer more.
		Endpoints: []PublicEndpoint{
			{Name: "alpha-1", ContextLength: 1_000_000, MaxOutput: 1000, Streams: true, Abilities: []string{}, Verified: true, Pricing: wantPricing},
			{Name: "alpha-2", ContextLength: 1_000_000, MaxOutput: 1000, Streams: true, Abilities: []string{}, Verified: true, Pricing: wantPricing},
		},
	}
	if diff := cmp.Diff(want, got[0], comparePrices); diff != "" {
		t.Errorf("alpha (-want +got):\n%s", diff)
	}
}

func TestPublicModelsSelectTheAccountThenTierThenDefaultPrice(t *testing.T) {
	r := newRig(t)
	account, tier := accounts.AccountID(uuid.NewV7()), accounts.TierID(uuid.NewV7())
	r.sold[r.alpha] = billing.Sold{Version: uuid.NewV7(), Audiences: billing.CompilePrices([]billing.AudiencePrice{
		{Audience: billing.Audience{}, Target: billing.Selector{Kind: "all"}, Sold: publicLayer(t, "0.000001", "0.000002")},
		{Audience: billing.Audience{Tier: tier}, Target: billing.Selector{Kind: "all"}, Sold: publicLayer(t, "0.000003", "0.000004")},
		{Audience: billing.Audience{Account: account}, Target: billing.Selector{Kind: "all"}, Sold: publicLayer(t, "0.000005", "0.000006")},
	})}
	r.publish()
	for _, tc := range []struct {
		name     string
		audience billing.Audience
		want     string
	}{
		{"account before tier", billing.Audience{Account: account, Tier: tier}, "0.000005"},
		{"tier before default", billing.Audience{Tier: tier}, "0.000003"},
		{"default", billing.Audience{}, "0.000001"},
	} {
		if got := publicInput(t, r, tc.audience); got != tc.want {
			t.Errorf("%s: model input %s, want %s", tc.name, got, tc.want)
		}
		if got := publicEndpointInputs(t, r, tc.audience); !cmp.Equal(got, map[string]string{"alpha-1": tc.want, "alpha-2": tc.want}) {
			t.Errorf("%s: endpoint input %v, want every endpoint at %s", tc.name, got, tc.want)
		}
	}
}

// publicInput is alpha's input price below the first context threshold, as the public projection gives it to a.
func publicInput(t *testing.T, r *rig, a billing.Audience) string {
	t.Helper()
	for _, m := range r.models.Public(a) {
		if m.ID != "acme/alpha" {
			continue
		}
		for _, rate := range m.Pricing.Rates {
			if rate.Item == catalog.ItemInput && rate.Above == 0 {
				return rate.Price.String()
			}
		}
	}
	t.Fatalf("no alpha input price for %+v", a)
	return ""
}

// publicEndpointInputs is each alpha endpoint's input price below the first context threshold, by codename.
func publicEndpointInputs(t *testing.T, r *rig, a billing.Audience) map[string]string {
	t.Helper()
	for _, m := range r.models.Public(a) {
		if m.ID != "acme/alpha" {
			continue
		}
		prices := make(map[string]string, len(m.Endpoints))
		for _, e := range m.Endpoints {
			for _, rate := range e.Pricing.Rates {
				if rate.Item == catalog.ItemInput && rate.Above == 0 {
					prices[e.Name] = rate.Price.String()
				}
			}
		}
		return prices
	}
	t.Fatalf("no alpha endpoints for %+v", a)
	return nil
}

func publicLayer(t *testing.T, input, output string) billing.Sold {
	t.Helper()
	schedule := `{"v":1,"currency":"USD","rates":{"input":"` + input + `","output":"` + output + `"}}`
	return billing.Sold{Version: uuid.NewV7(), Schedule: mustPublicSchedule(t, schedule)}
}

func mustPublicSchedule(t *testing.T, text string) catalog.Schedule {
	t.Helper()
	s, err := catalog.ParseSchedule([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
