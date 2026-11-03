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
	"axiom/internal/platform/pg"
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
			Endpoints: []gateway.PublicEndpoint{{
				Name: "g-haiku", ContextLength: 100000, MaxOutput: 8000, Streams: true,
				Abilities: []string{"tools"}, Quantization: "fp8", Verified: true,
				Pricing: catalog.ScheduleView{Currency: platform.CurrencyNone, Basis: []catalog.Item{catalog.ItemInput}},
			}},
		},
	}}
	api := consoleAPI{models: fake}
	ctx := context.WithValue(context.Background(), callKey{}, call{site: accounts.Site{DefaultTier: tier, ModelPricesPublic: true, ModelPricesPublicReady: true}})
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
	quantization := "fp8"
	endpoints := []consoleapi.PublicEndpoint{{
		Name: "g-haiku", ContextLength: 100000, MaxOutput: 8000, Streams: true,
		Abilities: []consoleapi.ModelAbility{consoleapi.ModelAbilityTools}, Quantization: &quantization, Verified: true,
		Pricing: &consoleapi.ModelPricing{
			Currency: "XXX",
			Basis:    []consoleapi.ModelItem{consoleapi.ModelItemInput},
			Rates:    []consoleapi.ModelRate{},
		},
	}}
	pricing := consoleapi.ModelPricing{
		Currency: "XXX",
		Basis:    []consoleapi.ModelItem{},
		Rates: []consoleapi.ModelRate{
			{ServiceTier: consoleapi.Standard, Item: consoleapi.ModelItemInput, Unit: consoleapi.ModelUnitToken, Amount: "0.000001"},
			{ServiceTier: consoleapi.Standard, Item: consoleapi.ModelItemInput, Unit: consoleapi.ModelUnitToken, Amount: "0.000003", Above: &zero},
			{ServiceTier: consoleapi.Priority, Item: consoleapi.ModelItemOutput, Unit: consoleapi.ModelUnitToken, Amount: "0.000002", Above: &above},
		},
	}
	want := consoleapi.PublicModel{
		Id: "other/haiku", Name: "haiku", Author: "other", ContextLength: 100000, MaxOutput: 8000,
		Abilities: []consoleapi.ModelAbility{consoleapi.ModelAbilityTools},
		Pricing:   &pricing,
		Endpoints: &endpoints,
	}
	if diff := cmp.Diff(want, body); diff != "" {
		t.Errorf("body (-want +got):\n%s", diff)
	}
}

// TestPublicModelsOmitPricesWhenHidden: when the site keeps its prices to signed-in members, the anonymous projection
// leaves out the model's and the endpoint's pricing rather than zeroing them, and the model itself stays.
func TestPublicModelsOmitPricesWhenHidden(t *testing.T) {
	fake := pricedFakeModel(t)
	api := consoleAPI{models: fake}
	list := api.publicModels(consoleapi.ListPublicModelsParams{}, billing.Audience{}, false)
	if len(list.Data) != 1 || list.Data[0].Pricing != nil || (*list.Data[0].Endpoints)[0].Pricing != nil {
		t.Fatalf("hidden prices: %+v", list)
	}
	if list.Data[0].Id != "acme/alpha" {
		t.Errorf("the model list changed to %+v", list.Data)
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

// pricedFakeModel is one public model whose model and endpoint both carry a price.
func pricedFakeModel(t *testing.T) *fakeModelProjection {
	t.Helper()
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	return &fakeModelProjection{models: []gateway.PublicModel{{
		ID: "acme/alpha", Name: "alpha", Author: "Acme", ContextLength: 1000, MaxOutput: 100, Streams: true,
		Pricing: catalog.ScheduleView{Currency: currency},
		Endpoints: []gateway.PublicEndpoint{{
			Name: "g-alpha", ContextLength: 1000, MaxOutput: 100, Streams: true,
			Pricing: catalog.ScheduleView{Currency: currency},
		}},
	}}}
}

// readModelList reads the models endpoint and checks it answered the one model with one endpoint the fake publishes.
func readModelList(t *testing.T, c consoleFixture, path string, mods ...mod) consoleapi.PublicModelList {
	t.Helper()
	rec := c.do(http.MethodGet, path, "", mods...)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
	}
	var list consoleapi.PublicModelList
	decode(t, rec, &list)
	if len(list.Data) != 1 || list.Data[0].Endpoints == nil || len(*list.Data[0].Endpoints) != 1 {
		t.Fatalf("%s: %+v", path, list)
	}
	return list
}

// pricesShown says whether the model and its endpoint both carry a price.
func pricesShown(list consoleapi.PublicModelList) bool {
	return list.Data[0].Pricing != nil && (*list.Data[0].Endpoints)[0].Pricing != nil
}

// setModelPricesPublicReady publishes whether every live node can hide prices.
func setModelPricesPublicReady(t *testing.T, db *pg.DB, ready bool) {
	t.Helper()
	if err := db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO platform.model_prices_public_readiness (id, enabled)
			VALUES (true, $1) ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled, updated_at = clock_timestamp()`, ready)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// setModelPricesPublic stores the site's choice and reloads this node's snapshot.
func setModelPricesPublic(t *testing.T, c consoleFixture, v bool) {
	t.Helper()
	if err := c.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO accounts.site_settings (site_id, model_prices_public)
			SELECT id, $1 FROM accounts.sites WHERE is_main
			ON CONFLICT (site_id) DO UPDATE SET model_prices_public = excluded.model_prices_public`, v)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.svc.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestModelPricesFollowTheSiteSetting: the anonymous models endpoint answers with prices while the site publishes
// them and without any when it keeps them to signed-in members; the signed-in account's endpoint keeps its prices in
// both states. The setting is read from the database through the node's snapshot, as a request would see it.
func TestModelPricesFollowTheSiteSetting(t *testing.T) {
	c := startNodeWithModels(consoleBase(t), &testClock{now: time.Now()}, false, pricedFakeModel(t))
	if !pricesShown(readModelList(t, c, "/models")) {
		t.Fatal("a site that sets nothing: the anonymous page has no prices, want public")
	}

	setModelPricesPublicReady(t, c.db, true)
	setModelPricesPublic(t, c, false)
	hidden := readModelList(t, c, "/models")
	if pricesShown(hidden) {
		t.Fatal("prices kept to members: the anonymous page still has model or endpoint prices")
	}
	if hidden.Data[0].Id != "acme/alpha" {
		t.Fatalf("prices kept to members: the model list changed to %+v", hidden.Data)
	}
	s := c.signIn(ownerEmail, ownerPassword)
	if !pricesShown(readModelList(t, c, "/models/account", as(s))) {
		t.Fatal("prices kept to members: the signed-in account lost its prices")
	}

	// An old node that cannot hide prices is live: every node falls back to public, so a request served by either
	// node gives the same answer instead of leaking on one.
	setModelPricesPublicReady(t, c.db, false)
	if err := c.svc.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !pricesShown(readModelList(t, c, "/models")) {
		t.Fatal("with an old node live: the anonymous page hid prices that node still returns")
	}
	if !pricesShown(readModelList(t, c, "/models/account", as(s))) {
		t.Fatal("with an old node live: the signed-in account lost its prices")
	}
	setModelPricesPublicReady(t, c.db, true)
	if err := c.svc.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pricesShown(readModelList(t, c, "/models")) {
		t.Fatal("after the old node left: the anonymous page still has prices")
	}

	setModelPricesPublic(t, c, true)
	if !pricesShown(readModelList(t, c, "/models")) {
		t.Fatal("prices public again: the anonymous page has no prices")
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
