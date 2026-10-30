package gateway

import (
	"slices"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
)

func TestPublicModelsAreTheServedCatalogAtTheAudiencePrice(t *testing.T) {
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
	alpha := got[0]
	if alpha.Name != "alpha" || alpha.Author != "acme" || alpha.ContextLength != 128000 || alpha.MaxOutput != 1000 || !alpha.Streams {
		t.Errorf("alpha: %+v", alpha)
	}
	if len(alpha.Abilities) != 0 {
		t.Errorf("alpha abilities: %v", alpha.Abilities)
	}
	if alpha.Pricing.Currency.String() != "USD" || len(alpha.Pricing.Basis) != 0 {
		t.Errorf("alpha pricing: %+v", alpha.Pricing)
	}
	rates := map[catalog.Item]string{}
	for _, rate := range alpha.Pricing.Rates {
		if rate.ServiceTier != catalog.ServiceTierStandard || !rate.Base || rate.Above != 0 {
			t.Errorf("alpha rate at %v base %v above %d", rate.ServiceTier, rate.Base, rate.Above)
		}
		rates[rate.Item] = rate.Price.String()
	}
	if rates[catalog.ItemInput] != "0.000001" || rates[catalog.ItemOutput] != "0.000002" {
		t.Errorf("alpha rates: %v", rates)
	}
}

func TestPublicModelsSelectTheAccountThenTierThenDefaultPrice(t *testing.T) {
	r := newRig(t)
	account, tier := accounts.AccountID(uuid.NewV7()), accounts.TierID(uuid.NewV7())
	layer := func(schedule string) billing.Sold {
		return billing.Sold{Version: uuid.NewV7(), Schedule: mustPublicSchedule(t, schedule)}
	}
	r.sold[r.alpha] = billing.Sold{Version: uuid.NewV7(), Audiences: billing.CompilePrices([]billing.AudiencePrice{
		{Audience: billing.Audience{}, Target: billing.Selector{Kind: "all"}, Sold: layer(`{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"}}`)},
		{Audience: billing.Audience{Tier: tier}, Target: billing.Selector{Kind: "all"}, Sold: layer(`{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000004"}}`)},
		{Audience: billing.Audience{Account: account}, Target: billing.Selector{Kind: "all"}, Sold: layer(`{"v":1,"currency":"USD","rates":{"input":"0.000005","output":"0.000006"}}`)},
	})}
	r.publish()

	input := func(a billing.Audience) string {
		t.Helper()
		for _, m := range r.models.Public(a) {
			if m.ID == "acme/alpha" {
				for _, rate := range m.Pricing.Rates {
					if rate.Item == catalog.ItemInput && rate.Above == 0 {
						return rate.Price.String()
					}
				}
			}
		}
		t.Fatalf("no alpha input price for %+v", a)
		return ""
	}
	if got := input(billing.Audience{Account: account, Tier: tier}); got != "0.000005" {
		t.Errorf("account price: %s", got)
	}
	if got := input(billing.Audience{Tier: tier}); got != "0.000003" {
		t.Errorf("tier price: %s", got)
	}
	if got := input(billing.Audience{}); got != "0.000001" {
		t.Errorf("default price: %s", got)
	}
}

func mustPublicSchedule(t *testing.T, text string) catalog.Schedule {
	t.Helper()
	s, err := catalog.ParseSchedule([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
