package main

import (
	"context"
	"errors"
	"strings"
	"uuid"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"axiom/internal/accounts"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

// The console's routing presets are the account layer's (docs/domains/routing.md「接口、权限与审计」): an account
// reads the presets its keys may use, writes its own, and sets its own default. The tables' policies keep it to its
// tenant; these handlers only name the layer.

// accountLayer is the layer of the session's account.
func accountLayer(cl call) routing.Layer {
	return routing.Layer{Scope: routing.ScopeAccount, Site: routing.SiteID(cl.site.ID), Account: routing.AccountID(cl.principal.AccountID())}
}

func (c consoleAPI) ListRoutingPresets(ctx context.Context, _ consoleapi.ListRoutingPresetsRequestObject) (consoleapi.ListRoutingPresetsResponseObject, error) {
	cl := callOf(ctx)
	layer := accountLayer(cl)
	var presets []routing.StoredPreset
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		presets, err = routing.ListPresets(ctx, tx, routing.Admission{Site: layer.Site, Account: layer.Account, Currency: cl.site.Currency})
		return err
	}); err != nil {
		return nil, err
	}
	out := consoleapi.RoutingPresetList{Data: make([]consoleapi.RoutingPreset, len(presets))}
	for i, p := range presets {
		out.Data[i] = routingPresetBody(p)
	}
	return consoleapi.ListRoutingPresets200JSONResponse(out), nil
}

func (c consoleAPI) GetRoutingPreset(ctx context.Context, r consoleapi.GetRoutingPresetRequestObject) (consoleapi.GetRoutingPresetResponseObject, error) {
	cl := callOf(ctx)
	var p routing.StoredPreset
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		p, err = routing.GetPreset(ctx, tx, routing.PresetID(r.PresetId))
		return err
	}); err != nil {
		return nil, err
	}
	return consoleapi.GetRoutingPreset200JSONResponse(routingPresetBody(p)), nil
}

func (c consoleAPI) CreateRoutingPreset(ctx context.Context, r consoleapi.CreateRoutingPresetRequestObject) (consoleapi.CreateRoutingPresetResponseObject, error) {
	cl := callOf(ctx)
	var p routing.StoredPreset
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		p, err = routing.CreatePreset(ctx, tx, actor(ctx), accountLayer(cl), r.Body.Slug,
			routing.PresetContent{Name: r.Body.Name, Rules: r.Body.Rules})
		return rulesProblem(err)
	}); err != nil {
		return nil, err
	}
	return consoleapi.CreateRoutingPreset201JSONResponse(routingPresetBody(p)), nil
}

func (c consoleAPI) UpdateRoutingPreset(ctx context.Context, r consoleapi.UpdateRoutingPresetRequestObject) (consoleapi.UpdateRoutingPresetResponseObject, error) {
	cl := callOf(ctx)
	var p routing.StoredPreset
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		p, err = routing.UpdatePreset(ctx, tx, actor(ctx), routing.PresetID(r.PresetId), r.Body.Revision,
			routing.PresetContent{Name: r.Body.Name, Rules: r.Body.Rules})
		return rulesProblem(err)
	}); err != nil {
		return nil, err
	}
	return consoleapi.UpdateRoutingPreset200JSONResponse(routingPresetBody(p)), nil
}

func (c consoleAPI) DisableRoutingPreset(ctx context.Context, r consoleapi.DisableRoutingPresetRequestObject) (consoleapi.DisableRoutingPresetResponseObject, error) {
	cl := callOf(ctx)
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) error {
		return routing.DisablePreset(ctx, tx, actor(ctx), routing.PresetID(r.PresetId), r.Params.Revision)
	}); err != nil {
		return nil, err
	}
	return consoleapi.DisableRoutingPreset204Response{}, nil
}

func (c consoleAPI) GetRoutingDefault(ctx context.Context, _ consoleapi.GetRoutingDefaultRequestObject) (consoleapi.GetRoutingDefaultResponseObject, error) {
	cl := callOf(ctx)
	var d routing.Default
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		d, err = routing.ReadDefault(ctx, tx, accountLayer(cl))
		return err
	}); err != nil {
		return nil, err
	}
	return consoleapi.GetRoutingDefault200JSONResponse(routingDefaultBody(d)), nil
}

func (c consoleAPI) SetRoutingDefault(ctx context.Context, r consoleapi.SetRoutingDefaultRequestObject) (consoleapi.SetRoutingDefaultResponseObject, error) {
	cl := callOf(ctx)
	var d routing.Default
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		d, err = routing.SetDefault(ctx, tx, actor(ctx), accountLayer(cl), cl.site.Currency, (*routing.PresetID)(r.Body.PresetId), r.Body.Version)
		return err
	}); err != nil {
		return nil, err
	}
	return consoleapi.SetRoutingDefault200JSONResponse(routingDefaultBody(d)), nil
}

// rulesProblem is a refusal of a preset's rules as the 400 that names them, with what is wrong; every other error is
// itself.
func rulesProblem(err error) error {
	if !errors.Is(err, routing.ErrRules) {
		return err
	}
	return invalidField("/rules", strings.TrimPrefix(err.Error(), routing.ErrRules.Error()+": "))
}

func routingPresetBody(p routing.StoredPreset) consoleapi.RoutingPreset {
	body := consoleapi.RoutingPreset{
		Id: openapi_types.UUID(p.ID), Scope: consoleapi.RoutingPresetScope(p.Scope.String()), Slug: p.Slug, Name: p.Name,
		Revision: p.Revision, Version: p.Version, Rules: p.Rules, Disabled: p.Disabled, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	if p.Currency != (platform.Currency{}) {
		code := p.Currency.String()
		body.Currency = &code
	}
	return body
}

func routingDefaultBody(d routing.Default) consoleapi.RoutingDefault {
	return consoleapi.RoutingDefault{PresetId: (*openapi_types.UUID)(d.Preset), Version: d.Version}
}

// keyPresets are the presets the keys are bound to, by ID, as the account can read them: a preset it can no longer
// read, which no key can be bound to but data may still name, is left out, and its keys show the ID alone. A page of
// keys names a few presets at most, the ones its account may choose from.
func keyPresets(ctx context.Context, tx pg.Tx, keys []accounts.Key) (map[uuid.UUID]routing.StoredPreset, error) {
	ids := map[uuid.UUID]bool{}
	for _, k := range keys {
		if k.Preset != nil {
			ids[*k.Preset] = true
		}
	}
	out := make(map[uuid.UUID]routing.StoredPreset, len(ids))
	for id := range ids {
		p, err := routing.GetPreset(ctx, tx, routing.PresetID(id))
		if errors.Is(err, routing.ErrPresetNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, nil
}

// keyPresetBody sets the key's routing preset on its body: its ID, and what the account can read of it.
func keyPresetBody(body *consoleapi.Key, k accounts.Key, presets map[uuid.UUID]routing.StoredPreset) {
	if k.Preset == nil {
		return
	}
	id := openapi_types.UUID(*k.Preset)
	body.PresetId = &id
	if p, ok := presets[*k.Preset]; ok {
		body.Preset = &consoleapi.RoutingPresetSummary{
			Id: id, Name: p.Name, Scope: consoleapi.RoutingPresetScope(p.Scope.String()), Version: p.Version, Disabled: &p.Disabled,
		}
	}
}
