package main

import (
	"context"
	"slices"
	"strings"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/gateway"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/websearch"
)

// modelProjection is the public model list the node last built: *gateway.ModelList in a node, a fake in a test.
type modelProjection interface {
	Public(billing.Audience) []gateway.PublicModel
}

// ListPublicModels answers the website's models and prices page, anonymously, at the site's default tier: the price a
// visitor pays after registering. A site may keep its prices to signed-in members; the models, their limits and
// endpoints stay public, only the prices are left out. While a previous-release node, which cannot hide prices, is
// live, every node keeps them public so the same site does not answer with prices on one node and without on another.
func (c consoleAPI) ListPublicModels(ctx context.Context, request consoleapi.ListPublicModelsRequestObject) (consoleapi.ListPublicModelsResponseObject, error) {
	site := callOf(ctx).site
	prices := site.ModelPricesPublic || !site.ModelPricesPublicReady
	return consoleapi.ListPublicModels200JSONResponse(c.publicModels(request.Params, billing.Audience{Tier: site.DefaultTier}, prices)), nil
}

// ListAccountModels answers the console's models and prices page at the signed-in account's current tier. The account
// row is read in the session's own scope; a tier change shows on the next request. Prices are always part of this
// answer, whether or not the site publishes them to visitors: the caller is a member of the site.
func (c consoleAPI) ListAccountModels(ctx context.Context, request consoleapi.ListAccountModelsRequestObject) (consoleapi.ListAccountModelsResponseObject, error) {
	principal := callOf(ctx).principal
	tier, err := c.accounts.AccountTier(ctx, principal)
	if err != nil {
		return nil, err
	}
	audience := billing.Audience{Account: principal.AccountID(), Tier: tier}
	return consoleapi.ListAccountModels200JSONResponse(c.publicModels(consoleapi.ListPublicModelsParams(request.Params), audience, true)), nil
}

// publicModels filters the projection and writes it in the contract's shape. Filtering here, not in the gateway, keeps
// the pages' three query parameters together with the shape they narrow. prices says whether this caller may see the
// sale prices; when it is false, a model's and an endpoint's pricing is left out rather than sent as zero.
func (c consoleAPI) publicModels(params consoleapi.ListPublicModelsParams, audience billing.Audience, prices bool) consoleapi.PublicModelList {
	var models []gateway.PublicModel
	if c.models != nil {
		models = c.models.Public(audience)
	}
	list := consoleapi.PublicModelList{Data: make([]consoleapi.PublicModel, 0, len(models))}
	for _, m := range models {
		if params.Q != nil && !modelMatches(*params.Q, m) {
			continue
		}
		if params.Author != nil && !strings.EqualFold(*params.Author, m.Author) {
			continue
		}
		if params.Ability != nil && !slices.Contains(m.Abilities, string(*params.Ability)) {
			continue
		}
		list.Data = append(list.Data, publicModelBody(m, c.search, prices))
	}
	return list
}

func modelMatches(q string, m gateway.PublicModel) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	return q == "" || strings.Contains(strings.ToLower(m.ID), q) || strings.Contains(strings.ToLower(m.Name), q)
}

func publicModelBody(m gateway.PublicModel, engines []websearch.Engine, prices bool) consoleapi.PublicModel {
	abilities := modelAbilities(m.Abilities)
	var pricing *consoleapi.ModelPricing
	if prices {
		modelPricing := publicPricing(m.Pricing)
		if len(engines) > 0 {
			searchPrices := make([]consoleapi.ModelSearchEnginePrice, 0, len(engines))
			for _, e := range engines {
				searchPrices = append(searchPrices, consoleapi.ModelSearchEnginePrice{
					Name: e.Name, PricePerSearch: e.PriceSearch.String(),
					PricePerExtraResult: e.PriceResult.String(), Default: e.Default,
				})
			}
			modelPricing.SearchEngines = &searchPrices
		}
		pricing = &modelPricing
	}
	endpoints := make([]consoleapi.PublicEndpoint, len(m.Endpoints))
	for i, e := range m.Endpoints {
		endpoints[i] = consoleapi.PublicEndpoint{
			Name: e.Name, ContextLength: e.ContextLength, MaxOutput: e.MaxOutput, Streams: e.Streams,
			Abilities: modelAbilities(e.Abilities), Verified: e.Verified,
		}
		if prices {
			endpointPricing := publicPricing(e.Pricing)
			endpoints[i].Pricing = &endpointPricing
		}
		if e.Quantization != "" {
			quantization := e.Quantization
			endpoints[i].Quantization = &quantization
		}
	}
	return consoleapi.PublicModel{
		Id: m.ID, Name: m.Name, Author: m.Author,
		ContextLength: m.ContextLength, MaxOutput: m.MaxOutput, Streams: m.Streams,
		Abilities: abilities,
		Pricing:   pricing,
		Endpoints: &endpoints,
	}
}

func modelAbilities(abilities []string) []consoleapi.ModelAbility {
	out := make([]consoleapi.ModelAbility, len(abilities))
	for i, ability := range abilities {
		out[i] = consoleapi.ModelAbility(ability)
	}
	return out
}

// publicPricing writes a schedule view in the contract's shape. It is used for a model's cheapest endpoint and for each
// endpoint's own price, so the two can never describe the same schedule differently. It also names each item's
// headline rate and its display amount, so every frontend shows the same list price (#695).
func publicPricing(pricing catalog.ScheduleView) consoleapi.ModelPricing {
	basis := make([]consoleapi.ModelItem, len(pricing.Basis))
	for i, item := range pricing.Basis {
		basis[i] = consoleapi.ModelItem(item.String())
	}
	rates := make([]consoleapi.ModelRate, len(pricing.Rates))
	var headlined catalog.ItemSet
	for i, rate := range pricing.Rates {
		// The view lists service tiers in enumeration order with standard first, so the first base-band rate of an
		// item is the standard tier's, or the first offered tier's when standard leaves the item out.
		headline := rate.Base && !headlined.Has(rate.Item)
		if headline {
			headlined.Add(rate.Item)
		}
		per := consoleapi.N1
		if rate.Item.Unit() == catalog.UnitToken {
			per = consoleapi.N1e06
		}
		display := string(rate.Price.Mul(int64(per)).AppendDecimal(nil))
		body := consoleapi.ModelRate{
			ServiceTier:   consoleapi.ModelServiceTier(rate.ServiceTier.String()),
			Item:          consoleapi.ModelItem(rate.Item.String()),
			Unit:          consoleapi.ModelUnit(rate.Item.Unit().String()),
			Amount:        rate.Price.String(),
			Headline:      &headline,
			DisplayAmount: &display,
			DisplayPer:    &per,
		}
		if !rate.Base {
			above := rate.Above
			body.Above = &above
		}
		rates[i] = body
	}
	return consoleapi.ModelPricing{
		Currency: pricing.Currency.String(),
		Basis:    basis,
		Rates:    rates,
	}
}
