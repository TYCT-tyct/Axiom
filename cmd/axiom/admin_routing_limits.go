package main

import (
	"context"
	"errors"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

func (a adminAPI) GetRoutingPresetLimits(ctx context.Context, _ adminapi.GetRoutingPresetLimitsRequestObject) (adminapi.GetRoutingPresetLimitsResponseObject, error) {
	var l routing.Limits
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		l, err = routing.ReadLimits(ctx, tx)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetRoutingPresetLimits200JSONResponse(routingLimitsView(l)), nil
}

func (a adminAPI) SetRoutingPresetLimits(ctx context.Context, r adminapi.SetRoutingPresetLimitsRequestObject) (adminapi.SetRoutingPresetLimitsResponseObject, error) {
	if r.Body == nil {
		return nil, invalidField("/", "a body is required")
	}
	l := routingLimitsInput(r.Body.Limits)
	body, replayed, err := idempotently(ctx, a.db, "setRoutingPresetLimits", r.Params.IdempotencyKey, []any{r.Body}, func(tx pg.Tx) (adminapi.RoutingPresetLimitsView, error) {
		if err := routing.SetLimits(ctx, tx, actor(ctx), l, r.Body.Reason); err != nil {
			if e, ok := errors.AsType[*routing.LimitError](err); ok {
				return adminapi.RoutingPresetLimitsView{}, invalidField("/limits/"+e.Field, e.Reason)
			}
			return adminapi.RoutingPresetLimitsView{}, err
		}
		return routingLimitsView(l), nil
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetRoutingPresetLimits200JSONResponse{
		Body: body, Headers: adminapi.SetRoutingPresetLimits200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func routingLimitsView(l routing.Limits) adminapi.RoutingPresetLimitsView {
	return adminapi.RoutingPresetLimitsView{Limits: routingLimitsBody(l), Defaults: routingLimitsBody(routing.DefaultLimits())}
}

// routingLimitsBody is l as the contract writes it: a zero value, the default in code, is absent, and so is a layer
// with neither value.
func routingLimitsBody(l routing.Limits) adminapi.RoutingPresetLimits {
	value := func(v int64) *int64 {
		if v == 0 {
			return nil
		}
		return &v
	}
	layer := func(v routing.LayerLimits) *adminapi.RoutingLayerLimits {
		if v == (routing.LayerLimits{}) {
			return nil
		}
		return &adminapi.RoutingLayerLimits{Active: value(v.Active), Total: value(v.Total)}
	}
	return adminapi.RoutingPresetLimits{
		Platform: layer(l.Platform), Site: layer(l.Site), Account: layer(l.Account), Workspace: layer(l.Workspace),
		KeptVersions: value(l.KeptVersions),
	}
}

func routingLimitsInput(b adminapi.RoutingPresetLimits) routing.Limits {
	value := func(v *int64) int64 {
		if v == nil {
			return 0
		}
		return *v
	}
	layer := func(v *adminapi.RoutingLayerLimits) routing.LayerLimits {
		if v == nil {
			return routing.LayerLimits{}
		}
		return routing.LayerLimits{Active: value(v.Active), Total: value(v.Total)}
	}
	return routing.Limits{
		Platform: layer(b.Platform), Site: layer(b.Site), Account: layer(b.Account), Workspace: layer(b.Workspace),
		KeptVersions: value(b.KeptVersions),
	}
}
