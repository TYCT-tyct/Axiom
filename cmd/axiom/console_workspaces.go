package main

import (
	"context"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

// The console's workspaces are its account's (docs/domains/accounts.md「工作区」): it lists, makes, renames, disables and
// enables them; the guard has authorized the permission the contract names on the session's account.

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
