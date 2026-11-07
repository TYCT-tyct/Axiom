package main

import (
	"context"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

// The console's workspaces are its account's (docs/domains/accounts.md「工作区」): it lists, makes, renames, disables and
// enables them, and sets their budgets; the guard has authorized the permission the contract names on the session's account.

func workspaceBody(w accounts.WorkspaceInfo) consoleapi.Workspace {
	return consoleapi.Workspace{Id: w.ID.TypeID(), Name: w.Name, IsDefault: w.Default, Disabled: w.Disabled}
}

// workspacePath is the workspace a path names; an ID the contract's pattern let through but that does not decode is as
// unknown as another account's.
func workspacePath(s string) (accounts.WorkspaceID, error) {
	id, err := accounts.ParseWorkspaceID(s)
	if err != nil {
		return accounts.WorkspaceID{}, accounts.ErrNotFound
	}
	return id, nil
}

// ListWorkspaces lists every live workspace of the account, the default first.
func (c consoleAPI) ListWorkspaces(ctx context.Context, _ consoleapi.ListWorkspacesRequestObject) (consoleapi.ListWorkspacesResponseObject, error) {
	cl := callOf(ctx)
	var list []accounts.WorkspaceInfo
	err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		list, err = c.accounts.ListWorkspaces(ctx, tx, cl.principal.AccountID())
		return err
	})
	if err != nil {
		return nil, err
	}
	body := consoleapi.WorkspaceList{Data: make([]consoleapi.Workspace, len(list))}
	for i, w := range list {
		body.Data[i] = workspaceBody(w)
	}
	return consoleapi.ListWorkspaces200JSONResponse(body), nil
}

// CreateWorkspace makes an enabled workspace of the account.
func (c consoleAPI) CreateWorkspace(ctx context.Context, r consoleapi.CreateWorkspaceRequestObject) (consoleapi.CreateWorkspaceResponseObject, error) {
	cl := callOf(ctx)
	var w accounts.WorkspaceInfo
	err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		w, err = c.accounts.CreateWorkspace(ctx, tx, audit.User(uuid.UUID(cl.principal.UserID())), cl.site.ID, cl.principal.AccountID(), r.Body.Name)
		return err
	})
	if err != nil {
		return nil, err
	}
	return consoleapi.CreateWorkspace201JSONResponse(workspaceBody(w)), nil
}

// GetWorkspace reads one workspace of the account.
func (c consoleAPI) GetWorkspace(ctx context.Context, r consoleapi.GetWorkspaceRequestObject) (consoleapi.GetWorkspaceResponseObject, error) {
	cl := callOf(ctx)
	id, err := workspacePath(r.WorkspaceId)
	if err != nil {
		return nil, err
	}
	var w accounts.WorkspaceInfo
	err = c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		w, err = c.accounts.GetWorkspace(ctx, tx, cl.principal.AccountID(), id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return consoleapi.GetWorkspace200JSONResponse(workspaceBody(w)), nil
}

// UpdateWorkspace renames, disables or enables a workspace of the account.
func (c consoleAPI) UpdateWorkspace(ctx context.Context, r consoleapi.UpdateWorkspaceRequestObject) (consoleapi.UpdateWorkspaceResponseObject, error) {
	cl := callOf(ctx)
	id, err := workspacePath(r.WorkspaceId)
	if err != nil {
		return nil, err
	}
	var w accounts.WorkspaceInfo
	err = c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		w, err = c.accounts.UpdateWorkspace(ctx, tx, audit.User(uuid.UUID(cl.principal.UserID())), cl.principal.AccountID(), id, r.Body.Name, r.Body.Disabled)
		return err
	})
	if err != nil {
		return nil, err
	}
	return consoleapi.UpdateWorkspace200JSONResponse(workspaceBody(w)), nil
}

// GetWorkspaceBudget reads a workspace's budget and what its keys have used of it.
func (c consoleAPI) GetWorkspaceBudget(ctx context.Context, r consoleapi.GetWorkspaceBudgetRequestObject) (consoleapi.GetWorkspaceBudgetResponseObject, error) {
	cl := callOf(ctx)
	id, err := workspacePath(r.WorkspaceId)
	if err != nil {
		return nil, err
	}
	var body consoleapi.WorkspaceBudget
	err = c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) error {
		w, err := c.accounts.GetWorkspace(ctx, tx, cl.principal.AccountID(), id)
		if err != nil {
			return err
		}
		body, err = budgetBody(ctx, tx, w, cl.site.Currency)
		return err
	})
	if err != nil {
		return nil, err
	}
	return consoleapi.GetWorkspaceBudget200JSONResponse(body), nil
}

// SetWorkspaceBudget replaces a workspace's budget, and answers it with the window's use read in the same transaction.
func (c consoleAPI) SetWorkspaceBudget(ctx context.Context, r consoleapi.SetWorkspaceBudgetRequestObject) (consoleapi.SetWorkspaceBudgetResponseObject, error) {
	cl := callOf(ctx)
	id, err := workspacePath(r.WorkspaceId)
	if err != nil {
		return nil, err
	}
	var p problems
	budget := accounts.Budget{Amount: spendLimit(r.Body.Limit, cl.site.Currency, "/limit", &p)}
	if r.Body.LimitReset != nil {
		if r.Body.Limit == nil {
			p.add("/limit_reset", "needs a limit")
		}
		budget.Reset = string(*r.Body.LimitReset)
	}
	if err := p.err(); err != nil {
		return nil, err
	}
	var body consoleapi.WorkspaceBudget
	err = c.db.Tx(ctx, cl.scope, func(tx pg.Tx) error {
		w, err := c.accounts.SetWorkspaceBudget(ctx, tx, audit.User(uuid.UUID(cl.principal.UserID())), cl.principal.AccountID(), id, budget)
		if err != nil {
			return err
		}
		body, err = budgetBody(ctx, tx, w, cl.site.Currency)
		return err
	})
	if err != nil {
		return nil, err
	}
	return consoleapi.SetWorkspaceBudget200JSONResponse(body), nil
}

// budgetBody is a workspace's budget as the contract gives it, with its current window's use read as admission reads it:
// spent and in flight, on the database's clock. No budget is an empty body, read without a query.
func budgetBody(ctx context.Context, tx pg.Tx, w accounts.WorkspaceInfo, currency platform.Currency) (consoleapi.WorkspaceBudget, error) {
	if w.Budget.Amount == nil {
		return consoleapi.WorkspaceBudget{}, nil
	}
	use, err := billing.ReadBudgetUsage(ctx, tx, uuid.UUID(w.ID), billing.Reset(w.Budget.Reset))
	if err != nil {
		return consoleapi.WorkspaceBudget{}, err
	}
	limit, used := *w.Budget.Amount, use.Spent+use.Reserved
	body := consoleapi.WorkspaceBudget{
		Limit: new(moneyOf(limit, currency)), Used: new(moneyOf(used, currency)), LimitRemaining: new(moneyOf(max(0, limit-used), currency)),
	}
	if w.Budget.Reset != "" {
		body.LimitReset, body.ResetsAt = new(consoleapi.LimitReset(w.Budget.Reset)), &use.ResetsAt
	}
	return body, nil
}
