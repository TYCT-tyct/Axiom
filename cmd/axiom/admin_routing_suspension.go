package main

import (
	"context"
	"time"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

func (a adminAPI) GetRoutingPresetSuspension(ctx context.Context, _ adminapi.GetRoutingPresetSuspensionRequestObject) (adminapi.GetRoutingPresetSuspensionResponseObject, error) {
	var since time.Time
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		since, err = routing.ReadSuspension(ctx, tx)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetRoutingPresetSuspension200JSONResponse(routingSuspension(since)), nil
}

func (a adminAPI) SetRoutingPresetSuspension(ctx context.Context, r adminapi.SetRoutingPresetSuspensionRequestObject) (adminapi.SetRoutingPresetSuspensionResponseObject, error) {
	if r.Body == nil {
		return nil, invalidField("/", "a body is required")
	}
	body, replayed, err := idempotently(ctx, a.db, "setRoutingPresetSuspension", r.Params.IdempotencyKey, []any{r.Body}, func(tx pg.Tx) (adminapi.RoutingPresetSuspension, error) {
		since, err := routing.SetSuspension(ctx, tx, actor(ctx), r.Body.Suspended, r.Body.Reason)
		return routingSuspension(since), err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetRoutingPresetSuspension200JSONResponse{
		Body: body, Headers: adminapi.SetRoutingPresetSuspension200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func routingSuspension(since time.Time) adminapi.RoutingPresetSuspension {
	if since.IsZero() {
		return adminapi.RoutingPresetSuspension{}
	}
	return adminapi.RoutingPresetSuspension{Suspended: true, Since: &since}
}
