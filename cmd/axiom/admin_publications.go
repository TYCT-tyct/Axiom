package main

import (
	"context"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

func publicationBody(ctx context.Context, tx pg.Tx, site uuid.UUID) (adminapi.PricePublicationList, error) {
	policy, prices, err := billing.PriceState(ctx, tx, site)
	if err != nil {
		return adminapi.PricePublicationList{}, err
	}
	return converted[adminapi.PricePublicationList](struct {
		Policy billing.PricePolicy   `json:"policy"`
		Data   []billing.Publication `json:"data"`
	}{policy, prices})
}

func (a adminAPI) refreshPriceTx(ctx context.Context, tx pg.Tx, site accounts.Site) error {
	snap, err := a.catalog.Current()
	if err != nil {
		return err
	}
	endpoints, err := a.calls.Routable()
	if err != nil {
		return err
	}
	exchange, err := billing.ExchangeRates(ctx, tx)
	if err != nil {
		return err
	}
	rates := make([]platform.ExchangeRate, len(exchange))
	for i, r := range exchange {
		rates[i] = r.Basis()
	}
	_, err = billing.RefreshPrices(ctx, tx, uuid.UUID(site.ID), site.Currency, snap, priceCosts(endpoints, rates, site.Currency))
	return err
}

func (a adminAPI) ListPricePublications(ctx context.Context, _ adminapi.ListPricePublicationsRequestObject) (adminapi.ListPricePublicationsResponseObject, error) {
	site, ok := a.accounts.MainSite()
	if !ok {
		return nil, accounts.ErrNotFound
	}
	var body adminapi.PricePublicationList
	err := a.db.Tx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		if err = a.refreshPriceTx(ctx, tx, site); err != nil {
			return err
		}
		body, err = publicationBody(ctx, tx, uuid.UUID(site.ID))
		return err
	})
	return adminapi.ListPricePublications200JSONResponse(body), err
}

func (a adminAPI) PublishSalePrice(ctx context.Context, r adminapi.PublishSalePriceRequestObject) (adminapi.PublishSalePriceResponseObject, error) {
	site, ok := a.accounts.MainSite()
	if !ok {
		return nil, accounts.ErrNotFound
	}
	model, err := modelIDOf(r.ModelId)
	if err != nil {
		return nil, err
	}
	candidate, err := uuid.Parse(r.Body.CandidateId)
	if err != nil {
		return nil, err
	}
	var current *uuid.UUID
	if r.Body.CurrentId != nil {
		id, err := uuid.Parse(*r.Body.CurrentId)
		if err != nil {
			return nil, err
		}
		current = &id
	}
	body, replayed, err := idempotently(ctx, a.db, "publishSalePrice", r.Params.IdempotencyKey, []any{r.ModelId, r.Body}, func(tx pg.Tx) (adminapi.PricePublicationList, error) {
		if err := a.refreshPriceTx(ctx, tx, site); err != nil {
			return adminapi.PricePublicationList{}, err
		}
		_, err := billing.PublishPrice(ctx, tx, actor(ctx), uuid.UUID(site.ID), model, candidate, current, r.Body.Confirmed, r.Body.OverrideFloor, r.Body.Reason)
		if err != nil {
			return adminapi.PricePublicationList{}, pricingProblem(err, "/")
		}
		return publicationBody(ctx, tx, uuid.UUID(site.ID))
	})
	if err != nil {
		return nil, err
	}
	return adminapi.PublishSalePrice200JSONResponse{Body: body, Headers: adminapi.PublishSalePrice200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) UpdatePricePolicy(ctx context.Context, r adminapi.UpdatePricePolicyRequestObject) (adminapi.UpdatePricePolicyResponseObject, error) {
	site, ok := a.accounts.MainSite()
	if !ok {
		return nil, accounts.ErrNotFound
	}
	body, replayed, err := idempotently(ctx, a.db, "updatePricePolicy", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.PricePublicationList, error) {
		in := billing.PricePolicy{Rev: r.Body.Rev, FloorMultiplier: r.Body.FloorMultiplier, ChangeRatio: r.Body.ChangeRatio, MaxIncreaseRatio: r.Body.MaxIncreaseRatio}
		if err := billing.SetPricePolicy(ctx, tx, actor(ctx), uuid.UUID(site.ID), in, r.Body.Reason); err != nil {
			return adminapi.PricePublicationList{}, pricingProblem(err, "/")
		}
		return publicationBody(ctx, tx, uuid.UUID(site.ID))
	})
	if err != nil {
		return nil, err
	}
	return adminapi.UpdatePricePolicy200JSONResponse{Body: body, Headers: adminapi.UpdatePricePolicy200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}
