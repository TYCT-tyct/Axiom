package main

import (
	"context"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/operations"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// ListOperationsPriceEnvelopes lists every site's envelope for the agent's sale prices.
func (a adminAPI) ListOperationsPriceEnvelopes(ctx context.Context, _ adminapi.ListOperationsPriceEnvelopesRequestObject) (adminapi.ListOperationsPriceEnvelopesResponseObject, error) {
	var list adminapi.OperationsPriceEnvelopeList
	err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		envelopes, err := operations.PriceEnvelopes(ctx, tx)
		list.Data = make([]adminapi.OperationsPriceEnvelopeSetting, len(envelopes))
		for i, e := range envelopes {
			list.Data[i] = envelopeBody(e)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.ListOperationsPriceEnvelopes200JSONResponse(list), nil
}

// SetOperationsPriceEnvelope makes or changes a site's envelope from the revision the owner read.
func (a adminAPI) SetOperationsPriceEnvelope(ctx context.Context, r adminapi.SetOperationsPriceEnvelopeRequestObject) (adminapi.SetOperationsPriceEnvelopeResponseObject, error) {
	site, err := accounts.ParseSiteID(r.SiteId)
	if err != nil {
		return nil, invalidField("site_id", "not a site ID")
	}
	l := r.Body.Limits
	e := operations.PriceEnvelope{
		Site: uuid.UUID(site), MinMargin: l.MinMarginBps, ModelDailyChange: l.ModelDailyChangeBps, DailyChanges: l.DailyChanges,
		DailyModels: l.DailyModels,
	}
	body, replayed, err := idempotently(ctx, a.db, "setOperationsPriceEnvelope", r.Params.IdempotencyKey, r, func(tx pg.Tx) (adminapi.OperationsPriceEnvelopeSetting, error) {
		set, err := operations.SetPriceEnvelope(ctx, tx, actor(ctx), e, r.Body.Rev, r.Body.Reason)
		return envelopeBody(set), err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetOperationsPriceEnvelope200JSONResponse{Body: body, Headers: adminapi.SetOperationsPriceEnvelope200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func envelopeBody(e operations.PriceEnvelope) adminapi.OperationsPriceEnvelopeSetting {
	return adminapi.OperationsPriceEnvelopeSetting{
		SiteId: accounts.SiteID(e.Site).TypeID(), Rev: e.Rev, UpdatedAt: e.Updated, Limits: adminapi.OperationsPriceEnvelope{
			MinMarginBps: e.MinMargin, ModelDailyChangeBps: e.ModelDailyChange, DailyChanges: e.DailyChanges, DailyModels: e.DailyModels,
		},
	}
}
