package main

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

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
	audience := billing.Audience{}
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
	query := func(q, author string, ability *consoleapi.ModelAbility) consoleapi.ListPublicModelsParams {
		p := consoleapi.ListPublicModelsParams{}
		if q != "" {
			p.Q = &q
		}
		if author != "" {
			p.Author = &author
		}
		p.Ability = ability
		return p
	}
	call := func(params consoleapi.ListPublicModelsParams) consoleapi.PublicModelList {
		t.Helper()
		response, err := api.ListPublicModels(ctx, consoleapi.ListPublicModelsRequestObject{Params: params})
		if err != nil {
			t.Fatal(err)
		}
		return consoleapi.PublicModelList(response.(consoleapi.ListPublicModels200JSONResponse))
	}

	if got := call(query("", "", nil)); len(got.Data) != 2 || fake.audience != audience {
		t.Fatalf("all models: %+v audience %+v", got, fake.audience)
	}
	if got := call(query("SONNET", "", nil)); len(got.Data) != 1 || got.Data[0].Id != "acme/claude-sonnet" {
		t.Errorf("q by name: %+v", got.Data)
	}
	if got := call(query("other/", "", nil)); len(got.Data) != 1 || got.Data[0].Id != "other/haiku" {
		t.Errorf("q by id: %+v", got.Data)
	}
	if got := call(query("", "ACME", nil)); len(got.Data) != 1 || got.Data[0].Author != "Acme" {
		t.Errorf("author: %+v", got.Data)
	}
	ability := consoleapi.ModelAbilityReasoning
	if got := call(query("", "", &ability)); len(got.Data) != 1 || got.Data[0].Id != "acme/claude-sonnet" {
		t.Errorf("ability: %+v", got.Data)
	}
	ability = consoleapi.ModelAbilityImages
	if got := call(query("", "", &ability)); len(got.Data) != 0 {
		t.Errorf("no images: %+v", got.Data)
	}

	body := call(query("other/", "", nil)).Data[0]
	if !slices.Equal(body.Abilities, []consoleapi.ModelAbility{"tools"}) ||
		len(body.Pricing.Basis) != 0 || len(body.Pricing.Rates) != 3 {
		t.Errorf("body: %+v", body)
	}
	if body.Pricing.Rates[0].Above != nil ||
		body.Pricing.Rates[1].Above == nil || *body.Pricing.Rates[1].Above != 0 ||
		body.Pricing.Rates[2].Above == nil || *body.Pricing.Rates[2].Above != 100000 {
		t.Errorf("bands: %+v", body.Pricing.Rates)
	}
	if body.Pricing.Rates[0].Unit != consoleapi.ModelUnitToken || body.Pricing.Rates[0].Amount != "0.000001" {
		t.Errorf("rate: %+v", body.Pricing.Rates[0])
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
	c := startNodeWithModels(consoleBase(t), &testClock{now: time.Now()}, 0, false, fake)
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
