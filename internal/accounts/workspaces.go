package accounts

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// Workspace is the account's workspace id when it is not deleted, and the account's default workspace, where the
// console makes keys, when id is nil; another account's, or a deleted one, is ErrNotFound. A setting of the workspace's
// own, such as its default routing preset, is read and written through it: the tables that hold such settings are
// other modules' and keep no reference to the workspace.
func (s *Service) Workspace(ctx context.Context, tx pg.Tx, account AccountID, id *WorkspaceID) (WorkspaceID, error) {
	if id == nil {
		return s.defaultWorkspace(ctx, tx, account)
	}
	found, err := s.queries.LiveWorkspace(ctx, tx, LiveWorkspaceParams{AccountID: uuid.UUID(account), ID: uuid.UUID(*id)})
	switch {
	case err != nil:
		return WorkspaceID{}, fmt.Errorf("read a workspace: %w", err)
	case len(found) == 0:
		return WorkspaceID{}, ErrNotFound
	}
	return *id, nil
}

var errNoDefaultWorkspace = errors.New("accounts: no default workspace")

func (s *Service) defaultWorkspace(ctx context.Context, tx pg.Tx, account AccountID) (WorkspaceID, error) {
	found, err := s.queries.DefaultWorkspace(ctx, tx, uuid.UUID(account))
	if err != nil || len(found) == 0 { // none is an account the scope cannot read: each has one
		return WorkspaceID{}, fmt.Errorf("the account's default workspace: %w", errors.Join(err, errNoDefaultWorkspace))
	}
	return WorkspaceID(found[0]), nil
}
