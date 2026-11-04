package main

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

// The admin API's routing presets are the platform's and the sites' layers (docs/domains/routing.md「接口、权限与审
// 计」): it runs in the platform scope, which the tables let write every layer, so these handlers keep it to those two;
// an account's and a workspace's presets are their owners' to manage in the console.

var platformLayer = routing.Layer{Scope: routing.ScopePlatform}

// siteLayer is the layer of the site with this ID, and the currency its presets' price limits are in.
func (a adminAPI) siteLayer(id string) (routing.Layer, platform.Currency, error) {
	site, found, err := a.siteOf(id)
	return routing.Layer{Scope: routing.ScopeSite, Site: routing.SiteID(site)}, found.Currency, err
}

func (a adminAPI) ListPlatformRoutingPresets(ctx context.Context, _ adminapi.ListPlatformRoutingPresetsRequestObject) (adminapi.ListPlatformRoutingPresetsResponseObject, error) {
	list, err := a.layerPresets(ctx, platformLayer)
	return adminapi.ListPlatformRoutingPresets200JSONResponse(list), err
}

func (a adminAPI) ListSiteRoutingPresets(ctx context.Context, r adminapi.ListSiteRoutingPresetsRequestObject) (adminapi.ListSiteRoutingPresetsResponseObject, error) {
	layer, _, err := a.siteLayer(r.SiteId)
	if err != nil {
		return nil, err
	}
	list, err := a.layerPresets(ctx, layer)
	return adminapi.ListSiteRoutingPresets200JSONResponse(list), err
}

func (a adminAPI) layerPresets(ctx context.Context, layer routing.Layer) (adminapi.RoutingPresetList, error) {
	var presets []routing.StoredPreset
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		presets, err = routing.LayerPresets(ctx, tx, layer)
		return err
	}); err != nil {
		return adminapi.RoutingPresetList{}, err
	}
	out := consoleapi.RoutingPresetList{Data: make([]consoleapi.RoutingPreset, len(presets))}
	for i, p := range presets {
		out.Data[i] = routingPresetBody(p)
	}
	return converted[adminapi.RoutingPresetList](out)
}

func (a adminAPI) CreatePlatformRoutingPreset(ctx context.Context, r adminapi.CreatePlatformRoutingPresetRequestObject) (adminapi.CreatePlatformRoutingPresetResponseObject, error) {
	body, replayed, err := a.createLayerPreset(ctx, "createPlatformRoutingPreset", r.Params.IdempotencyKey, platformLayer, r.Body, []any{r.Body})
	if err != nil {
		return nil, err
	}
	return adminapi.CreatePlatformRoutingPreset201JSONResponse{
		Body: body, Headers: adminapi.CreatePlatformRoutingPreset201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func (a adminAPI) CreateSiteRoutingPreset(ctx context.Context, r adminapi.CreateSiteRoutingPresetRequestObject) (adminapi.CreateSiteRoutingPresetResponseObject, error) {
	layer, _, err := a.siteLayer(r.SiteId)
	if err != nil {
		return nil, err
	}
	body, replayed, err := a.createLayerPreset(ctx, "createSiteRoutingPreset", r.Params.IdempotencyKey, layer, r.Body, []any{r.SiteId, r.Body})
	if err != nil {
		return nil, err
	}
	return adminapi.CreateSiteRoutingPreset201JSONResponse{
		Body: body, Headers: adminapi.CreateSiteRoutingPreset201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func (a adminAPI) createLayerPreset(ctx context.Context, operation, key string, layer routing.Layer, b *adminapi.LayerRoutingPresetCreate, request []any) (adminapi.RoutingPreset, bool, error) {
	if b == nil {
		return adminapi.RoutingPreset{}, false, invalidField("/", "a body is required")
	}
	currency, err := presetCurrency(b.Currency)
	if err != nil {
		return adminapi.RoutingPreset{}, false, err
	}
	return idempotently(ctx, a.db, operation, key, request, func(tx pg.Tx) (adminapi.RoutingPreset, error) {
		p, err := routing.CreatePreset(ctx, tx, actor(ctx), layer, b.Slug, routing.PresetContent{Name: b.Name, Currency: currency, Rules: b.Rules}, b.Reason)
		if err != nil {
			return adminapi.RoutingPreset{}, rulesProblem(err)
		}
		return adminRoutingPreset(p)
	})
}

func (a adminAPI) GetLayerRoutingPreset(ctx context.Context, r adminapi.GetLayerRoutingPresetRequestObject) (adminapi.GetLayerRoutingPresetResponseObject, error) {
	var p routing.StoredPreset
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		p, err = layerPreset(ctx, tx, r.PresetId)
		return err
	}); err != nil {
		return nil, err
	}
	body, err := adminRoutingPreset(p)
	return adminapi.GetLayerRoutingPreset200JSONResponse(body), err
}

func (a adminAPI) UpdateLayerRoutingPreset(ctx context.Context, r adminapi.UpdateLayerRoutingPresetRequestObject) (adminapi.UpdateLayerRoutingPresetResponseObject, error) {
	if r.Body == nil {
		return nil, invalidField("/", "a body is required")
	}
	currency, err := presetCurrency(r.Body.Currency)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "updateLayerRoutingPreset", r.Params.IdempotencyKey, []any{r.PresetId, r.Body},
		func(tx pg.Tx) (adminapi.RoutingPreset, error) {
			if _, err := layerPreset(ctx, tx, r.PresetId); err != nil {
				return adminapi.RoutingPreset{}, err
			}
			p, err := routing.UpdatePreset(ctx, tx, actor(ctx), routing.PresetID(r.PresetId), r.Body.Revision,
				routing.PresetContent{Name: r.Body.Name, Currency: currency, Rules: r.Body.Rules}, r.Body.Reason)
			if err != nil {
				return adminapi.RoutingPreset{}, rulesProblem(err)
			}
			return adminRoutingPreset(p)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.UpdateLayerRoutingPreset200JSONResponse{
		Body: body, Headers: adminapi.UpdateLayerRoutingPreset200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func (a adminAPI) DisableLayerRoutingPreset(ctx context.Context, r adminapi.DisableLayerRoutingPresetRequestObject) (adminapi.DisableLayerRoutingPresetResponseObject, error) {
	if r.Body == nil {
		return nil, invalidField("/", "a body is required")
	}
	_, replayed, err := idempotently(ctx, a.db, "disableLayerRoutingPreset", r.Params.IdempotencyKey, []any{r.PresetId, r.Body},
		func(tx pg.Tx) (struct{}, error) {
			if _, err := layerPreset(ctx, tx, r.PresetId); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, routing.DisablePreset(ctx, tx, actor(ctx), routing.PresetID(r.PresetId), r.Body.Revision, r.Body.Reason)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.DisableLayerRoutingPreset204Response{
		Headers: adminapi.DisableLayerRoutingPreset204ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

// layerPreset is the platform's or a site's preset with this ID, as tx reads it; an account's or a workspace's is not
// the admin API's, and is not found like an unknown one. A preset's layer never changes, so the write that follows in
// tx changes the preset read here.
func layerPreset(ctx context.Context, tx pg.Tx, id openapi_types.UUID) (routing.StoredPreset, error) {
	p, err := routing.GetPreset(ctx, tx, routing.PresetID(id))
	if err == nil && p.Scope > routing.ScopeSite {
		return routing.StoredPreset{}, routing.ErrPresetNotFound
	}
	return p, err
}

func (a adminAPI) GetPlatformRoutingDefault(ctx context.Context, _ adminapi.GetPlatformRoutingDefaultRequestObject) (adminapi.GetPlatformRoutingDefaultResponseObject, error) {
	d, err := a.readDefault(ctx, platformLayer)
	return adminapi.GetPlatformRoutingDefault200JSONResponse(d), err
}

func (a adminAPI) GetSiteRoutingDefault(ctx context.Context, r adminapi.GetSiteRoutingDefaultRequestObject) (adminapi.GetSiteRoutingDefaultResponseObject, error) {
	layer, _, err := a.siteLayer(r.SiteId)
	if err != nil {
		return nil, err
	}
	d, err := a.readDefault(ctx, layer)
	return adminapi.GetSiteRoutingDefault200JSONResponse(d), err
}

func (a adminAPI) readDefault(ctx context.Context, layer routing.Layer) (adminapi.RoutingDefault, error) {
	var d routing.Default
	err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		d, err = routing.ReadDefault(ctx, tx, layer)
		return err
	})
	return adminRoutingDefault(d), err
}

// SetPlatformRoutingDefault sets the platform's default in no currency: SetDefault then lets it point only at a
// platform preset that limits no price, since the platform's default serves sites of every currency.
func (a adminAPI) SetPlatformRoutingDefault(ctx context.Context, r adminapi.SetPlatformRoutingDefaultRequestObject) (adminapi.SetPlatformRoutingDefaultResponseObject, error) {
	body, replayed, err := a.setDefault(ctx, "setPlatformRoutingDefault", r.Params.IdempotencyKey, platformLayer, platform.Currency{}, r.Body, []any{r.Body})
	if err != nil {
		return nil, err
	}
	return adminapi.SetPlatformRoutingDefault200JSONResponse{
		Body: body, Headers: adminapi.SetPlatformRoutingDefault200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func (a adminAPI) SetSiteRoutingDefault(ctx context.Context, r adminapi.SetSiteRoutingDefaultRequestObject) (adminapi.SetSiteRoutingDefaultResponseObject, error) {
	layer, currency, err := a.siteLayer(r.SiteId)
	if err != nil {
		return nil, err
	}
	body, replayed, err := a.setDefault(ctx, "setSiteRoutingDefault", r.Params.IdempotencyKey, layer, currency, r.Body, []any{r.SiteId, r.Body})
	if err != nil {
		return nil, err
	}
	return adminapi.SetSiteRoutingDefault200JSONResponse{
		Body: body, Headers: adminapi.SetSiteRoutingDefault200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func (a adminAPI) setDefault(ctx context.Context, operation, key string, layer routing.Layer, currency platform.Currency, b *adminapi.LayerRoutingDefaultUpdate, request []any) (adminapi.RoutingDefault, bool, error) {
	if b == nil {
		return adminapi.RoutingDefault{}, false, invalidField("/", "a body is required")
	}
	return idempotently(ctx, a.db, operation, key, request, func(tx pg.Tx) (adminapi.RoutingDefault, error) {
		d, err := routing.SetDefault(ctx, tx, actor(ctx), layer, currency, (*routing.PresetID)(b.PresetId), b.Version, b.Reason)
		return adminRoutingDefault(d), err
	})
}

// presetCurrency is the currency a platform preset's price limits are in; none is the zero currency, which a site's
// preset and a platform preset that limits no price have.
func presetCurrency(code *string) (platform.Currency, error) {
	if code == nil {
		return platform.Currency{}, nil
	}
	c, err := platform.ParseCurrency(*code)
	if err != nil {
		return platform.Currency{}, invalidField("/currency", "must be a currency this platform knows")
	}
	return c, nil
}

// adminRoutingPreset is the preset as the console writes it: the two contracts define RoutingPreset alike.
func adminRoutingPreset(p routing.StoredPreset) (adminapi.RoutingPreset, error) {
	return converted[adminapi.RoutingPreset](routingPresetBody(p))
}

func adminRoutingDefault(d routing.Default) adminapi.RoutingDefault {
	return adminapi.RoutingDefault{PresetId: (*openapi_types.UUID)(d.Preset), Version: d.Version}
}
