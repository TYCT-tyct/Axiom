package main

import (
	"context"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

func (c consoleAPI) ListPresets(ctx context.Context, r consoleapi.ListPresetsRequestObject) (consoleapi.ListPresetsResponseObject, error) {
	cl := callOf(ctx)
	limit, offset := 50, 0
	if r.Params.Limit != nil {
		limit = *r.Params.Limit
	}
	if r.Params.Offset != nil {
		offset = *r.Params.Offset
	}
	var data []accounts.Preset
	var total int
	err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		data, total, err = accounts.ListPresets(ctx, tx, cl.principal.AccountID(), limit, offset)
		return err
	})
	if err != nil {
		return nil, err
	}
	return adminJSON[consoleapi.ListPresets200JSONResponse](map[string]any{"data": data, "total_count": total})
}

func (c consoleAPI) GetPreset(ctx context.Context, r consoleapi.GetPresetRequestObject) (consoleapi.GetPresetResponseObject, error) {
	cl := callOf(ctx)
	var p accounts.Preset
	err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		p, err = accounts.GetPreset(ctx, tx, cl.principal.AccountID(), r.Slug)
		return err
	})
	if err != nil {
		return nil, err
	}
	return adminJSON[consoleapi.GetPreset200JSONResponse](p)
}

func (c consoleAPI) CreatePreset(ctx context.Context, r consoleapi.CreatePresetRequestObject) (consoleapi.CreatePresetResponseObject, error) {
	p, err := c.savePreset(ctx, accounts.Preset{Slug: r.Body.Slug, Name: r.Body.Name, Config: r.Body.Config}, true)
	if err != nil {
		return nil, err
	}
	return adminJSON[consoleapi.CreatePreset201JSONResponse](p)
}

func (c consoleAPI) UpdatePreset(ctx context.Context, r consoleapi.UpdatePresetRequestObject) (consoleapi.UpdatePresetResponseObject, error) {
	p, err := c.savePreset(ctx, accounts.Preset{Slug: r.Slug, Name: r.Body.Name, Config: r.Body.Config}, false)
	if err != nil {
		return nil, err
	}
	return adminJSON[consoleapi.UpdatePreset200JSONResponse](p)
}

func (c consoleAPI) savePreset(ctx context.Context, p accounts.Preset, create bool) (accounts.Preset, error) {
	cl := callOf(ctx)
	err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		p, err = accounts.SavePreset(ctx, tx, audit.User(uuid.UUID(cl.principal.UserID())), cl.site.ID, cl.principal.AccountID(), p, create)
		return err
	})
	return p, err
}

func (c consoleAPI) DeletePreset(ctx context.Context, r consoleapi.DeletePresetRequestObject) (consoleapi.DeletePresetResponseObject, error) {
	cl := callOf(ctx)
	err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) error {
		return accounts.DeletePreset(ctx, tx, audit.User(uuid.UUID(cl.principal.UserID())), cl.site.ID, cl.principal.AccountID(), r.Slug)
	})
	if err != nil {
		return nil, err
	}
	return consoleapi.DeletePreset204Response{}, nil
}
