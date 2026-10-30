package main

import (
	"context"
	"slices"
	"strings"

	"axiom/internal/billing"
	"axiom/internal/gateway"
	"axiom/internal/platform/consoleapi"
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
		list.Data = append(list.Data, publicModelBody(m))
	}
	return list
}

func modelMatches(q string, m gateway.PublicModel) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	return q == "" || strings.Contains(strings.ToLower(m.ID), q) || strings.Contains(strings.ToLower(m.Name), q)
}

func publicModelBody(m gateway.PublicModel) consoleapi.PublicModel {
	abilities := make([]consoleapi.ModelAbility, len(m.Abilities))
	for i, ability := range m.Abilities {
		abilities[i] = consoleapi.ModelAbility(ability)
	}
	basis := make([]consoleapi.ModelItem, len(m.Pricing.Basis))
	for i, item := range m.Pricing.Basis {
		basis[i] = consoleapi.ModelItem(item.String())
	}
	rates := make([]consoleapi.ModelRate, len(m.Pricing.Rates))
	for i, rate := range m.Pricing.Rates {
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
	return consoleapi.PublicModel{
		Id: m.ID, Name: m.Name, Author: m.Author,
		ContextLength: m.ContextLength, MaxOutput: m.MaxOutput, Streams: m.Streams,
		Abilities: abilities,
		Pricing: consoleapi.ModelPricing{
			Currency: m.Pricing.Currency.String(),
			Basis:    basis,
			Rates:    rates,
		},
	}
}
