package main

import (
	"context"
	"errors"
	"net/http"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// The admin's tier operations (docs/domains/accounts.md「等级」). What a tier pays is a pricing rule for it, written
// through the pricing rules' own operations; these name, retire and choose the default tier.

func (a adminAPI) CreateTier(ctx context.Context, r adminapi.CreateTierRequestObject) (adminapi.CreateTierResponseObject, error) {
	site, ok := a.accounts.MainSite()
	if !ok {
		return nil, accounts.ErrNotFound
	}
	id := site.ID
	if r.Body.SiteId != nil {
		var err error
		if id, _, err = a.siteOf(*r.Body.SiteId); err != nil {
			return nil, err
		}
	}
	tier, replayed, err := idempotently(ctx, a.db, "createTier", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.TierRef, error) {
		t, err := a.accounts.CreateTier(ctx, tx, actor(ctx), id, r.Body.Name, r.Body.Reason)
		return tierView(t), tierProblem(err)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.CreateTier201JSONResponse{Body: tier, Headers: adminapi.CreateTier201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) UpdateTier(ctx context.Context, r adminapi.UpdateTierRequestObject) (adminapi.UpdateTierResponseObject, error) {
	id, err := accounts.ParseTierID(r.TierId)
	if err != nil {
		return nil, invalidField("tier_id", "not a tier's ID")
	}
	tier, replayed, err := idempotently(ctx, a.db, "updateTier", r.Params.IdempotencyKey, []any{r.TierId, r.Body}, func(tx pg.Tx) (adminapi.TierRef, error) {
		t, err := a.accounts.UpdateTier(ctx, tx, actor(ctx), id, accounts.TierChange{Name: r.Body.Name, Disabled: r.Body.Disabled}, r.Body.Reason)
		return tierView(t), tierProblem(err)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.UpdateTier200JSONResponse{Body: tier, Headers: adminapi.UpdateTier200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) SetDefaultTier(ctx context.Context, r adminapi.SetDefaultTierRequestObject) (adminapi.SetDefaultTierResponseObject, error) {
	id, err := accounts.ParseTierID(r.TierId)
	if err != nil {
		return nil, invalidField("tier_id", "not a tier's ID")
	}
	_, replayed, err := idempotently(ctx, a.db, "setDefaultTier", r.Params.IdempotencyKey, []any{r.TierId, r.Body}, func(tx pg.Tx) (struct{}, error) {
		return struct{}{}, tierProblem(a.accounts.SetDefaultTier(ctx, tx, actor(ctx), id, r.Body.Reason))
	})
	if err != nil {
		return nil, err
	}
	if !replayed {
		// The change is committed. A failure to reload is not its failure: every node reloads within its interval.
		if err := a.accounts.Refresh(context.WithoutCancel(ctx)); err != nil {
			a.logger.WarnContext(ctx, "default tier saved but this node did not reload it", "tier", r.TierId, "err", err)
		}
	}
	return adminapi.SetDefaultTier204Response{Headers: adminapi.SetDefaultTier204ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// tierProblem is a tier refusal as the contract answers it; other errors pass through to the shared tables.
func tierProblem(err error) error {
	if e, ok := errors.AsType[*accounts.SettingError](err); ok {
		if e.Field == "name" {
			return invalidField("/name", e.Reason)
		}
		return unprocessableFields("/"+e.Field, e.Reason)
	}
	switch {
	case errors.Is(err, accounts.ErrTierNameTaken):
		return &apiError{status: http.StatusConflict, code: codeNameTaken, message: "a tier of the site has that name"}
	case errors.Is(err, accounts.ErrDefaultTier):
		return &apiError{status: http.StatusConflict, code: codeConflict, message: "the site's default tier cannot be disabled; make another tier the default first"}
	}
	return err
}
