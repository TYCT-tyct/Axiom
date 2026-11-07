package main

import (
	"context"
	"errors"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// workspaceLimitBody is a workspace limit as the contract writes it; a limit not set at its level is left out.
func workspaceLimitBody(l accounts.WorkspaceLimit) adminapi.WorkspaceLimitView {
	view := adminapi.WorkspaceLimitView{Effective: l.Effective}
	if l.Own != nil {
		view.MaxWorkspaces = new(int(*l.Own))
	}
	return view
}

// workspaceLimitOf is the limit a request sets, nil to clear it. The contract bounds it to 1,000.
func workspaceLimitOf(b adminapi.WorkspaceLimitUpdate) *int32 {
	if b.MaxWorkspaces == nil {
		return nil
	}
	return new(int32(*b.MaxWorkspaces)) // #nosec G115 -- the contract bounds it to 1,000
}

// workspaceLimitProblem is a limit the module found out of bounds as a 400 naming it.
func workspaceLimitProblem(err error) error {
	if e, ok := errors.AsType[*accounts.SettingError](err); ok {
		return invalidField("/"+e.Field, e.Reason)
	}
	return err
}

// GetSiteWorkspaceLimit tells a site's own limit on the workspaces of an account and what it comes to.
func (a adminAPI) GetSiteWorkspaceLimit(ctx context.Context, request adminapi.GetSiteWorkspaceLimitRequestObject) (adminapi.GetSiteWorkspaceLimitResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	var limit accounts.WorkspaceLimit
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		limit, err = a.accounts.SiteWorkspaceLimit(ctx, tx, id)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetSiteWorkspaceLimit200JSONResponse(workspaceLimitBody(limit)), nil
}

// UpdateSiteWorkspaceLimit replaces a site's own limit on the workspaces of an account. This node reloads the sites at
// once, the others within their reload interval.
func (a adminAPI) UpdateSiteWorkspaceLimit(ctx context.Context, request adminapi.UpdateSiteWorkspaceLimitRequestObject) (adminapi.UpdateSiteWorkspaceLimitResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	view, replayed, err := idempotently(ctx, a.db, "updateSiteWorkspaceLimit", request.Params.IdempotencyKey, []any{request.SiteId, request.Body},
		func(tx pg.Tx) (adminapi.WorkspaceLimitView, error) {
			if err := a.accounts.SetSiteWorkspaceLimit(ctx, tx, actor(ctx), id, workspaceLimitOf(*request.Body), request.Body.Reason); err != nil {
				return adminapi.WorkspaceLimitView{}, workspaceLimitProblem(err)
			}
			limit, err := a.accounts.SiteWorkspaceLimit(ctx, tx, id)
			return workspaceLimitBody(limit), err
		})
	if err != nil {
		return nil, err
	}
	if !replayed {
		// The change is committed. A failure to reload is not its failure: every node reloads within its interval.
		if err := a.accounts.Refresh(context.WithoutCancel(ctx)); err != nil {
			a.logger.WarnContext(ctx, "site workspace limit saved but this node did not reload it", "site", request.SiteId, "err", err)
		}
	}
	return adminapi.UpdateSiteWorkspaceLimit200JSONResponse{Body: view, Headers: adminapi.UpdateSiteWorkspaceLimit200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// UpdateAccountWorkspaceLimit replaces an account's own limit on its workspaces. It applies to the next workspace made,
// on any node: the account's row is read in the transaction that makes it.
func (a adminAPI) UpdateAccountWorkspaceLimit(ctx context.Context, request adminapi.UpdateAccountWorkspaceLimitRequestObject) (adminapi.UpdateAccountWorkspaceLimitResponseObject, error) {
	id, err := accountID(request.AccountId)
	if err != nil {
		return nil, err
	}
	view, replayed, err := idempotently(ctx, a.db, "updateAccountWorkspaceLimit", request.Params.IdempotencyKey, []any{request.AccountId, request.Body},
		func(tx pg.Tx) (adminapi.WorkspaceLimitView, error) {
			if err := a.accounts.SetAccountWorkspaceLimit(ctx, tx, actor(ctx), id, workspaceLimitOf(*request.Body), request.Body.Reason); err != nil {
				return adminapi.WorkspaceLimitView{}, workspaceLimitProblem(err)
			}
			limit, err := a.accounts.AccountWorkspaceLimit(ctx, tx, id)
			return workspaceLimitBody(limit), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.UpdateAccountWorkspaceLimit200JSONResponse{Body: view, Headers: adminapi.UpdateAccountWorkspaceLimit200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}
