package main

import (
	"context"
	"errors"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

func rateLimitBody(v accounts.RateLimit) adminapi.RateLimitView {
	var own *int
	if v.Requests != nil {
		own = new(int(*v.Requests))
	}
	return adminapi.RateLimitView{Requests: own, EffectiveRequests: new(v.Effective)}
}

// GetRateLimit returns the administrator's setting separately from its inherited effective allowance.
func (a adminAPI) GetRateLimit(ctx context.Context, r adminapi.GetRateLimitRequestObject) (adminapi.GetRateLimitResponseObject, error) {
	id, err := rateTargetID(r.Target, r.Id)
	if err != nil {
		return nil, accounts.ErrNotFound
	}
	var view adminapi.RateLimitView
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		v, err := a.accounts.RateLimit(ctx, tx, r.Target, id)
		view = rateLimitBody(v)
		return err
	})
	return adminapi.GetRateLimit200JSONResponse(view), err
}

// SetRateLimit leaves user-editable key settings alone; replay never writes a second audit entry.
func (a adminAPI) SetRateLimit(ctx context.Context, r adminapi.SetRateLimitRequestObject) (adminapi.SetRateLimitResponseObject, error) {
	id, err := rateTargetID(r.Target, r.Id)
	if err != nil {
		return nil, accounts.ErrNotFound
	}
	var requests *int32
	if r.Body.Requests != nil {
		requests = new(int32(*r.Body.Requests))
	} // #nosec G115 -- contract bounds to 1,000,000,000
	view, replayed, err := idempotently(ctx, a.db, "setRateLimit", r.Params.IdempotencyKey, []any{r.Target, r.Id, r.Body}, func(tx pg.Tx) (adminapi.RateLimitView, error) {
		if err := a.accounts.SetRateLimit(ctx, tx, actor(ctx), r.Target, id, requests, r.Body.Reason); err != nil {
			if setting, ok := errors.AsType[*accounts.SettingError](err); ok {
				return adminapi.RateLimitView{}, invalidField("/"+setting.Field, setting.Reason)
			}
			return adminapi.RateLimitView{}, err
		}
		v, err := a.accounts.RateLimit(ctx, tx, r.Target, id)
		return rateLimitBody(v), err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetRateLimit200JSONResponse{Body: view, Headers: adminapi.SetRateLimit200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func rateTargetID(target, text string) (uuid.UUID, error) {
	switch target {
	case "tier":
		id, err := accounts.ParseTierID(text)
		return uuid.UUID(id), err
	case "account":
		id, err := accounts.ParseAccountID(text)
		return uuid.UUID(id), err
	case "key":
		id, err := accounts.ParseKeyID(text)
		return uuid.UUID(id), err
	default:
		return uuid.UUID{}, accounts.ErrNotFound
	}
}
