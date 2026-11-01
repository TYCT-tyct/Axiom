package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/gateway"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
)

type fakeModelProjection struct {
	audience billing.Audience
	models   []gateway.PublicModel
}

func (f *fakeModelProjection) Public(a billing.Audience) []gateway.PublicModel {
	f.audience = a
	return f.models
}

func TestPublicModelsFilterAndProject(t *testing.T) {
	tier := accounts.TierID{}
	price := func(text string) platform.UnitPrice {
		t.Helper()
		p, err := platform.ParseUnitPrice(text)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	fake := &fakeModelProjection{models: []gateway.PublicModel{
		{
			ID: "acme/claude-sonnet", Name: "claude-sonnet", Author: "Acme", ContextLength: 200000, MaxOutput: 64000, Streams: true,
			Abilities: []string{"tools", "reasoning"},
			Pricing:   catalog.ScheduleView{Currency: platform.CurrencyNone, Basis: []catalog.Item{catalog.ItemInput}},
		},
		{
			ID: "other/haiku", Name: "haiku", Author: "other", ContextLength: 100000, MaxOutput: 8000,
			Abilities: []string{"tools"},
			Pricing: catalog.ScheduleView{Currency: platform.CurrencyNone, Rates: []catalog.ScheduleRate{
				{ServiceTier: catalog.ServiceTierStandard, Base: true, Item: catalog.ItemInput, Price: price("0.000001")},
				{ServiceTier: catalog.ServiceTierStandard, Above: 0, Item: catalog.ItemInput, Price: price("0.000003")},
				{ServiceTier: catalog.ServiceTierPriority, Above: 100000, Item: catalog.ItemOutput, Price: price("0.000002")},
			}},
		},
	}}
	api := consoleAPI{models: fake}
	ctx := context.WithValue(context.Background(), callKey{}, call{site: accounts.Site{DefaultTier: tier}})
	call := func(params consoleapi.ListPublicModelsParams) consoleapi.PublicModelList {
		t.Helper()
		response, err := api.ListPublicModels(ctx, consoleapi.ListPublicModelsRequestObject{Params: params})
		if err != nil {
			t.Fatal(err)
		}
		return consoleapi.PublicModelList(response.(consoleapi.ListPublicModels200JSONResponse))
	}

	if got := call(consoleapi.ListPublicModelsParams{}); len(got.Data) != 2 || fake.audience != (billing.Audience{}) {
		t.Fatalf("all models: %+v audience %+v", got, fake.audience)
	}
	sonnet, other, acme := "SONNET", "other/", "ACME"
	reasoning, images := consoleapi.ModelAbilityReasoning, consoleapi.ModelAbilityImages
	for _, tc := range []struct {
		name   string
		params consoleapi.ListPublicModelsParams
		want   []string
	}{
		{"q by name", consoleapi.ListPublicModelsParams{Q: &sonnet}, []string{"acme/claude-sonnet"}},
		{"q by public id", consoleapi.ListPublicModelsParams{Q: &other}, []string{"other/haiku"}},
		{"author, ignoring case", consoleapi.ListPublicModelsParams{Author: &acme}, []string{"acme/claude-sonnet"}},
		{"ability", consoleapi.ListPublicModelsParams{Ability: &reasoning}, []string{"acme/claude-sonnet"}},
		{"ability no model has", consoleapi.ListPublicModelsParams{Ability: &images}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ids []string
			for _, m := range call(tc.params).Data {
				ids = append(ids, m.Id)
			}
			if diff := cmp.Diff(tc.want, ids); diff != "" {
				t.Errorf("models (-want +got):\n%s", diff)
			}
		})
	}

	body := call(consoleapi.ListPublicModelsParams{Q: &other}).Data[0]
	zero, above := int64(0), int64(100000)
	want := consoleapi.PublicModel{
		Id: "other/haiku", Name: "haiku", Author: "other", ContextLength: 100000, MaxOutput: 8000,
		Abilities: []consoleapi.ModelAbility{consoleapi.ModelAbilityTools},
		Pricing: consoleapi.ModelPricing{
			Currency: "XXX",
			Basis:    []consoleapi.ModelItem{},
			Rates: []consoleapi.ModelRate{
				{ServiceTier: consoleapi.Standard, Item: consoleapi.ModelItemInput, Unit: consoleapi.ModelUnitToken, Amount: "0.000001"},
				{ServiceTier: consoleapi.Standard, Item: consoleapi.ModelItemInput, Unit: consoleapi.ModelUnitToken, Amount: "0.000003", Above: &zero},
				{ServiceTier: consoleapi.Priority, Item: consoleapi.ModelItemOutput, Unit: consoleapi.ModelUnitToken, Amount: "0.000002", Above: &above},
			},
		},
	}
	if diff := cmp.Diff(want, body); diff != "" {
		t.Errorf("body (-want +got):\n%s", diff)
	}
}

func consoleBase(t *testing.T) commandLine {
	t.Helper()
	c, _ := ownerFixture.get(t, buildOwnerDatabase)
	return c
}

func TestModelsPickTheAudienceOfThePage(t *testing.T) {
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeModelProjection{models: []gateway.PublicModel{{
		ID: "acme/alpha", Name: "alpha", Author: "Acme", ContextLength: 1000, MaxOutput: 100, Streams: true,
		Pricing: catalog.ScheduleView{Currency: currency},
	}}}
	c := startNodeWithModels(consoleBase(t), &testClock{now: time.Now()}, false, fake)
	site, _, err := c.svc.SiteForHost(consoleHost)
	if err != nil {
		t.Fatal(err)
	}
	if rec := c.do(http.MethodGet, "/models", ""); rec.Code != http.StatusOK {
		t.Fatalf("public models: %d %s", rec.Code, rec.Body)
	}
	public := fake.audience
	if public.Tier != site.DefaultTier || public.Account != (billing.Audience{}).Account {
		t.Errorf("anonymous audience: %+v, want the site's default tier %v", public, site.DefaultTier)
	}
	s := c.signIn(ownerEmail, ownerPassword)
	if rec := c.do(http.MethodGet, "/models/account", "", as(s)); rec.Code != http.StatusOK {
		t.Fatalf("account models: %d %s", rec.Code, rec.Body)
	}
	if fake.audience.Account == public.Account || fake.audience.Tier != site.DefaultTier {
		t.Errorf("account audience: %+v, want the owner's account at tier %v", fake.audience, site.DefaultTier)
	}
}

func TestModelsEndpoints(t *testing.T) {
	c := newConsoleFixture(t)
	// The website reads the public projection without a session; an empty catalog is an empty list, not an error.
	rec := c.do(http.MethodGet, "/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("public models: %d %s", rec.Code, rec.Body)
	}
	// The console's account prices need a session.
	if rec := c.do(http.MethodGet, "/models/account", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("account models without a session: %d", rec.Code)
	}
	s := c.signIn(ownerEmail, ownerPassword)
	if rec := c.do(http.MethodGet, "/models/account", "", as(s)); rec.Code != http.StatusOK {
		t.Errorf("account models: %d %s", rec.Code, rec.Body)
	}
	for _, query := range []string{"ability=telepathy", "q=" + strings.Repeat("a", 101)} {
		if rec := c.do(http.MethodGet, "/models?"+query, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("models?%s: %d", query, rec.Code)
		}
	}
	if rec := c.do(http.MethodGet, "/models", "", atHost("unknown.example")); rec.Code != http.StatusNotFound {
		t.Errorf("unknown host: %d", rec.Code)
	}
}
