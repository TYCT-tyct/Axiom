package main

import (
	"context"
	"errors"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

func loginLimitsBody(v accounts.LoginLimits) adminapi.LoginLimits {
	optional := func(n *int32) *int {
		if n == nil {
			return nil
		}
		return new(int(*n))
	}
	return adminapi.LoginLimits{IpAttemptBurst: optional(v.IPAttemptBurst), IpAttemptsPerMinute: optional(v.IPAttemptsPerMinute), EmailSignInFailures: optional(v.EmailSignInFailures), UserCodeFailures: optional(v.UserCodeFailures), ChallengeCodeAttempts: optional(v.ChallengeCodeAttempts)}
}

func loginLimitsOf(v adminapi.LoginLimits) accounts.LoginLimits {
	optional := func(n *int) *int32 {
		if n == nil {
			return nil
		}
		return new(int32(*n))
	} // #nosec G115 -- the contract bounds each override to at most 10,000
	return accounts.LoginLimits{IPAttemptBurst: optional(v.IpAttemptBurst), IPAttemptsPerMinute: optional(v.IpAttemptsPerMinute), EmailSignInFailures: optional(v.EmailSignInFailures), UserCodeFailures: optional(v.UserCodeFailures), ChallengeCodeAttempts: optional(v.ChallengeCodeAttempts)}
}

func loginLimitsView(v accounts.LoginLimits) adminapi.LoginLimitsView {
	return adminapi.LoginLimitsView{Limits: new(loginLimitsBody(v)), Defaults: new(loginLimitsBody(accounts.DefaultLoginLimits()))}
}

func (a adminAPI) GetSiteLoginLimits(ctx context.Context, r adminapi.GetSiteLoginLimitsRequestObject) (adminapi.GetSiteLoginLimitsResponseObject, error) {
	id, _, err := a.siteOf(r.SiteId)
	if err != nil {
		return nil, err
	}
	var own accounts.LoginLimits
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var err error
		own, err = a.accounts.SiteLoginLimits(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.GetSiteLoginLimits200JSONResponse(loginLimitsView(own)), nil
}

func (a adminAPI) UpdateSiteLoginLimits(ctx context.Context, r adminapi.UpdateSiteLoginLimitsRequestObject) (adminapi.UpdateSiteLoginLimitsResponseObject, error) {
	id, _, err := a.siteOf(r.SiteId)
	if err != nil {
		return nil, err
	}
	view, replayed, err := idempotently(ctx, a.db, "updateSiteLoginLimits", r.Params.IdempotencyKey, []any{r.SiteId, r.Body}, func(tx pg.Tx) (adminapi.LoginLimitsView, error) {
		set := loginLimitsOf(r.Body.Limits)
		if err := a.accounts.SetSiteLoginLimits(ctx, tx, actor(ctx), id, set, r.Body.Reason); err != nil {
			if e, ok := errors.AsType[*accounts.SettingError](err); ok {
				return adminapi.LoginLimitsView{}, invalidField("/limits/"+e.Field, e.Reason)
			}
			return adminapi.LoginLimitsView{}, err
		}
		return loginLimitsView(set), nil
	})
	if err != nil {
		return nil, err
	}
	if !replayed {
		if err := a.accounts.Refresh(context.WithoutCancel(ctx)); err != nil {
			a.logger.WarnContext(ctx, "login limits saved but reload failed", "site", r.SiteId, "err", err)
		}
	}
	return adminapi.UpdateSiteLoginLimits200JSONResponse{Body: view, Headers: adminapi.UpdateSiteLoginLimits200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) SetAccountSuspension(ctx context.Context, r adminapi.SetAccountSuspensionRequestObject) (adminapi.SetAccountSuspensionResponseObject, error) {
	id, err := accountID(r.AccountId)
	if err != nil {
		return nil, err
	}
	_, replayed, err := idempotently(ctx, a.db, "setAccountSuspension", r.Params.IdempotencyKey, []any{r.AccountId, r.Body}, func(tx pg.Tx) (struct{}, error) {
		return struct{}{}, a.accounts.SetAccountSuspended(ctx, tx, actor(ctx), id, r.Body.Suspended, r.Body.Reason)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetAccountSuspension204Response{Headers: adminapi.SetAccountSuspension204ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) SetAccountTier(ctx context.Context, r adminapi.SetAccountTierRequestObject) (adminapi.SetAccountTierResponseObject, error) {
	id, err := accountID(r.AccountId)
	if err != nil {
		return nil, err
	}
	tier, err := accounts.ParseTierID(r.Body.TierId)
	if err != nil {
		return nil, invalidField("/tier_id", "not a tier's ID")
	}
	_, replayed, err := idempotently(ctx, a.db, "setAccountTier", r.Params.IdempotencyKey, []any{r.AccountId, r.Body}, func(tx pg.Tx) (struct{}, error) {
		if err := a.accounts.SetAccountTier(ctx, tx, actor(ctx), id, tier, r.Body.Reason); err != nil {
			if e, ok := errors.AsType[*accounts.SettingError](err); ok {
				return struct{}{}, unprocessableFields("/"+e.Field, e.Reason)
			}
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetAccountTier204Response{Headers: adminapi.SetAccountTier204ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) SetUserSignIn(ctx context.Context, r adminapi.SetUserSignInRequestObject) (adminapi.SetUserSignInResponseObject, error) {
	id, err := accounts.ParseUserID(r.UserId)
	if err != nil {
		return nil, accounts.ErrNotFound
	}
	_, replayed, err := idempotently(ctx, a.db, "setUserSignIn", r.Params.IdempotencyKey, []any{r.UserId, r.Body}, func(tx pg.Tx) (struct{}, error) {
		return struct{}{}, a.accounts.SetUserSignInDisabled(ctx, tx, actor(ctx), id, r.Body.Disabled, r.Body.Reason)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetUserSignIn204Response{Headers: adminapi.SetUserSignIn204ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) SetPlatformGrant(ctx context.Context, r adminapi.SetPlatformGrantRequestObject) (adminapi.SetPlatformGrantResponseObject, error) {
	id, err := accounts.ParseUserID(r.UserId)
	if err != nil {
		return nil, accounts.ErrNotFound
	}
	_, replayed, err := idempotently(ctx, a.db, "setPlatformGrant", r.Params.IdempotencyKey, []any{r.UserId, r.Role, r.Body}, func(tx pg.Tx) (struct{}, error) {
		return struct{}{}, a.accounts.SetPlatformGrantRevoked(ctx, tx, callOf(ctx).principal, id, r.Role, r.Body.Revoked, r.Body.Reason)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetPlatformGrant204Response{Headers: adminapi.SetPlatformGrant204ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) ListUserPlatformGrants(ctx context.Context, r adminapi.ListUserPlatformGrantsRequestObject) (adminapi.ListUserPlatformGrantsResponseObject, error) {
	id, err := accounts.ParseUserID(r.UserId)
	if err != nil {
		return nil, accounts.ErrNotFound
	}
	grants := []adminapi.PlatformGrant{}
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		rows, err := a.accounts.UserPlatformGrants(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, r := range rows {
			grants = append(grants, adminapi.PlatformGrant{Role: new(r.Role), Revoked: new(r.Revoked)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return adminapi.ListUserPlatformGrants200JSONResponse{Data: &grants}, nil
}
