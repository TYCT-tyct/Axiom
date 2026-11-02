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
// visitor pays after registering.
func (c consoleAPI) ListPublicModels(ctx context.Context, request consoleapi.ListPublicModelsRequestObject) (consoleapi.ListPublicModelsResponseObject, error) {
	site := callOf(ctx).site
	return consoleapi.ListPublicModels200JSONResponse(c.publicModels(request.Params, billing.Audience{Tier: site.DefaultTier})), nil
}

// ListAccountModels answers the console's models and prices page at the signed-in account's current tier. The account
// row is read in the session's own scope; a tier change shows on the next request.
func (c consoleAPI) ListAccountModels(ctx context.Context, request consoleapi.ListAccountModelsRequestObject) (consoleapi.ListAccountModelsResponseObject, error) {
	principal := callOf(ctx).principal
	tier, err := c.accounts.AccountTier(ctx, principal)
	if err != nil {
		return nil, err
	}
	audience := billing.Audience{Account: principal.AccountID(), Tier: tier}
	return consoleapi.ListAccountModels200JSONResponse(c.publicModels(consoleapi.ListPublicModelsParams(request.Params), audience)), nil
}

// publicModels filters the projection and writes it in the contract's shape. Filtering here, not in the gateway, keeps
// the pages' three query parameters together with the shape they narrow.
func (c consoleAPI) publicModels(params consoleapi.ListPublicModelsParams, audience billing.Audience) consoleapi.PublicModelList {
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
		list.Data = append(list.Data, publicModelBody(m, c.search))
	}
	return list
}

func modelMatches(q string, m gateway.PublicModel) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	return q == "" || strings.Contains(strings.ToLower(m.ID), q) || strings.Contains(strings.ToLower(m.Name), q)
}

func publicModelBody(m gateway.PublicModel, engines []websearch.Engine) consoleapi.PublicModel {
	abilities := modelAbilities(m.Abilities)
	pricing := publicPricing(m.Pricing)
	if len(engines) > 0 {
		prices := make([]consoleapi.ModelSearchEnginePrice, 0, len(engines))
		for _, e := range engines {
			prices = append(prices, consoleapi.ModelSearchEnginePrice{
				Name: e.Name, PricePerSearch: e.PriceSearch.String(),
				PricePerExtraResult: e.PriceResult.String(), Default: e.Default,
			})
		}
		pricing.SearchEngines = &prices
	}
	endpoints := make([]consoleapi.PublicEndpoint, len(m.Endpoints))
	for i, e := range m.Endpoints {
		endpoints[i] = consoleapi.PublicEndpoint{
			Name: e.Name, ContextLength: e.ContextLength, MaxOutput: e.MaxOutput, Streams: e.Streams,
			Abilities: modelAbilities(e.Abilities), Verified: e.Verified, Pricing: publicPricing(e.Pricing),
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
// endpoint's own price, so the two can never describe the same schedule differently.
func publicPricing(pricing catalog.ScheduleView) consoleapi.ModelPricing {
	basis := make([]consoleapi.ModelItem, len(pricing.Basis))
	for i, item := range pricing.Basis {
		basis[i] = consoleapi.ModelItem(item.String())
	}
	rates := make([]consoleapi.ModelRate, len(pricing.Rates))
	for i, rate := range pricing.Rates {
		body := consoleapi.ModelRate{
			ServiceTier: consoleapi.ModelServiceTier(rate.ServiceTier.String()),
			Item:        consoleapi.ModelItem(rate.Item.String()),
			Unit:        consoleapi.ModelUnit(rate.Item.Unit().String()),
			Amount:      rate.Price.String(),
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
