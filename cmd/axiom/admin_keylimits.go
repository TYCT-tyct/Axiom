package main

import (
	"context"
	"errors"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// keyLimitsBody is the limits at one level as the contract writes them; a limit that is not set is left out.
func keyLimitsBody(own accounts.KeyLimits, effective accounts.EffectiveKeyLimits) adminapi.KeyLimitsView {
	optional := func(n *int32) *int {
		if n == nil {
			return nil
		}
		value := int(*n)
		return &value
	}
	return adminapi.KeyLimitsView{
		Limits:    adminapi.KeyLimits{MaxKeys: optional(own.MaxKeys), CreationsPerHour: optional(own.CreationsPerHour)},
		Effective: adminapi.EffectiveKeyLimits{MaxKeys: effective.MaxKeys, CreationsPerHour: effective.CreationsPerHour},
	}
}

// keyLimitsOf reads the limits a request gives. Their bounds are the contract's and the module's.
func keyLimitsOf(b adminapi.KeyLimits) accounts.KeyLimits {
	optional := func(n *int) *int32 {
		if n == nil {
			return nil
		}
		value := int32(*n) // #nosec G115 -- the contract bounds it to 10,000
		return &value
	}
	return accounts.KeyLimits{MaxKeys: optional(b.MaxKeys), CreationsPerHour: optional(b.CreationsPerHour)}
}

// keyLimitProblem is a limit the module found out of bounds as a 400 naming it.
func keyLimitProblem(err error) error {
	if e, ok := errors.AsType[*accounts.SettingError](err); ok {
		return invalidField("/limits/"+e.Field, e.Reason)
	}
	return err
}

// GetSiteKeyLimits tells a site's own limits on the keys of an account and what they come to.
func (a adminAPI) GetSiteKeyLimits(ctx context.Context, request adminapi.GetSiteKeyLimitsRequestObject) (adminapi.GetSiteKeyLimitsResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	var view adminapi.KeyLimitsView
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		own, effective, err := a.accounts.SiteKeyLimits(ctx, tx, id)
		view = keyLimitsBody(own, effective)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetSiteKeyLimits200JSONResponse(view), nil
}

// UpdateSiteKeyLimits replaces a site's own limits on the keys of an account, and leaves its settings as they are. This
// node reloads the sites at once, the others within their reload interval.
func (a adminAPI) UpdateSiteKeyLimits(ctx context.Context, request adminapi.UpdateSiteKeyLimitsRequestObject) (adminapi.UpdateSiteKeyLimitsResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	set := keyLimitsOf(request.Body.Limits)
	view, replayed, err := idempotently(ctx, a.db, "updateSiteKeyLimits", request.Params.IdempotencyKey, []any{request.SiteId, request.Body},
		func(tx pg.Tx) (adminapi.KeyLimitsView, error) {
			if err := a.accounts.SetSiteKeyLimits(ctx, tx, actor(ctx), id, set, request.Body.Reason); err != nil {
				return adminapi.KeyLimitsView{}, keyLimitProblem(err)
			}
			own, effective, err := a.accounts.SiteKeyLimits(ctx, tx, id)
			return keyLimitsBody(own, effective), err
		})
	if err != nil {
		return nil, err
	}
	if !replayed {
		// The change is committed. A failure to reload is not its failure: every node reloads within its interval.
		if err := a.accounts.Refresh(context.WithoutCancel(ctx)); err != nil {
			a.logger.WarnContext(ctx, "site key limits saved but this node did not reload them", "site", request.SiteId, "err", err)
		}
	}
	return adminapi.UpdateSiteKeyLimits200JSONResponse{Body: view, Headers: adminapi.UpdateSiteKeyLimits200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// UpdateAccountKeyLimits replaces an account's own limits on its keys. They apply to the next key made, on any node:
// the account's row is read in the transaction that makes the key.
func (a adminAPI) UpdateAccountKeyLimits(ctx context.Context, request adminapi.UpdateAccountKeyLimitsRequestObject) (adminapi.UpdateAccountKeyLimitsResponseObject, error) {
	id, err := accountID(request.AccountId)
	if err != nil {
		return nil, err
	}
	set := keyLimitsOf(request.Body.Limits)
	view, replayed, err := idempotently(ctx, a.db, "updateAccountKeyLimits", request.Params.IdempotencyKey, []any{request.AccountId, request.Body},
		func(tx pg.Tx) (adminapi.KeyLimitsView, error) {
			if err := a.accounts.SetAccountKeyLimits(ctx, tx, actor(ctx), id, set, request.Body.Reason); err != nil {
				return adminapi.KeyLimitsView{}, keyLimitProblem(err)
			}
			own, effective, err := a.accounts.AccountKeyLimits(ctx, tx, id)
			return keyLimitsBody(own, effective), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.UpdateAccountKeyLimits200JSONResponse{Body: view, Headers: adminapi.UpdateAccountKeyLimits200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}
