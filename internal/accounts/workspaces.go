package accounts

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// maxWorkspaces is how many workspaces an account may hold that are not deleted, disabled ones included since they can
// be enabled again: it bounds the list, which is read whole (docs/domains/accounts.md「工作区」).
const maxWorkspaces = 50

var (
	// ErrWorkspaceNotFound is a workspace named for a new key that is another account's, deleted or unknown.
	ErrWorkspaceNotFound = errors.New("accounts: no such workspace")
	// ErrWorkspaceDisabled is a disabled workspace named for a new key: its keys are refused on every call.
	ErrWorkspaceDisabled = errors.New("accounts: the workspace is disabled")
	// ErrWorkspaceNameTaken is a name another live workspace of the account has, ignoring case.
	ErrWorkspaceNameTaken = errors.New("accounts: a workspace of the account has that name")
	// ErrTooManyWorkspaces is a workspace made by an account that holds maxWorkspaces.
	ErrTooManyWorkspaces = errors.New("accounts: the account holds the most workspaces it may")
	// ErrDefaultWorkspace is the default workspace disabled: new keys go there when no workspace is named.
	ErrDefaultWorkspace = errors.New("accounts: the default workspace cannot be disabled")
)

// WorkspaceInfo is a workspace as the console shows it.
type WorkspaceInfo struct {
	ID       WorkspaceID
	Name     string
	Default  bool
	Disabled bool
}

// Workspace is the account's workspace id when it is not deleted, and the account's default workspace, where the
// console makes keys, when id is nil; another account's, or a deleted one, is ErrNotFound. A setting of the workspace's
// own, such as its default routing preset, is read and written through it: the tables that hold such settings are
// other modules' and keep no reference to the workspace.
func (s *Service) Workspace(ctx context.Context, tx pg.Tx, account AccountID, id *WorkspaceID) (WorkspaceID, error) {
	if id == nil {
		return s.defaultWorkspace(ctx, tx, account)
	}
	if _, err := s.GetWorkspace(ctx, tx, account, *id); err != nil {
		return WorkspaceID{}, err
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

// keyWorkspace is the workspace a new key of the account goes to: id, which must be usable, or the default workspace
// when id is nil. A named ID is never taken for the default, the zero one included: it names no workspace.
func (s *Service) keyWorkspace(ctx context.Context, tx pg.Tx, account AccountID, id *WorkspaceID) (WorkspaceID, error) {
	if id == nil {
		found, err := s.defaultWorkspace(ctx, tx, account)
		if err != nil {
			return WorkspaceID{}, errors.Join(err, ErrKeyNotFound)
		}
		return found, nil
	}
	w, err := s.GetWorkspace(ctx, tx, account, *id)
	switch {
	case errors.Is(err, ErrNotFound):
		return WorkspaceID{}, ErrWorkspaceNotFound
	case err != nil:
		return WorkspaceID{}, err
	case w.Disabled:
		return WorkspaceID{}, ErrWorkspaceDisabled
	}
	return *id, nil
}

// ListWorkspaces is every workspace of the account that is not deleted: its default first, then in the order they
// were made.
func (s *Service) ListWorkspaces(ctx context.Context, tx pg.Tx, account AccountID) ([]WorkspaceInfo, error) {
	rows, err := s.queries.ListWorkspaces(ctx, tx, uuid.UUID(account))
	if err != nil {
		return nil, fmt.Errorf("list the workspaces: %w", err)
	}
	list := make([]WorkspaceInfo, len(rows))
	for i, r := range rows {
		list[i] = WorkspaceInfo{ID: WorkspaceID(r.ID), Name: r.Name, Default: r.IsDefault, Disabled: r.Status == "disabled"}
	}
	return list, nil
}

// GetWorkspace is the account's workspace id; another account's, a deleted one and an unknown one are ErrNotFound.
func (s *Service) GetWorkspace(ctx context.Context, tx pg.Tx, account AccountID, id WorkspaceID) (WorkspaceInfo, error) {
	rows, err := s.queries.GetWorkspace(ctx, tx, GetWorkspaceParams{AccountID: uuid.UUID(account), ID: uuid.UUID(id)})
	switch {
	case err != nil:
		return WorkspaceInfo{}, fmt.Errorf("read a workspace: %w", err)
	case len(rows) == 0:
		return WorkspaceInfo{}, ErrNotFound
	}
	r := rows[0]
	return WorkspaceInfo{ID: id, Name: r.Name, Default: r.IsDefault, Disabled: r.Status == "disabled"}, nil
}

// CreateWorkspace makes an enabled workspace of the account named name, and records it in the audit log in the same
// transaction. It takes the account's turn, as making a key does, so two made at once are counted one after the other.
func (s *Service) CreateWorkspace(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, account AccountID, name string) (WorkspaceInfo, error) {
	if _, err := s.queries.LockAccount(ctx, tx, uuid.UUID(account)); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return WorkspaceInfo{}, ErrNotFound
		}
		return WorkspaceInfo{}, fmt.Errorf("make a workspace: lock the account: %w", err)
	}
	held, err := s.queries.CountWorkspaces(ctx, tx, uuid.UUID(account))
	if err != nil {
		return WorkspaceInfo{}, fmt.Errorf("make a workspace: count the account's: %w", err)
	}
	if held >= maxWorkspaces {
		return WorkspaceInfo{}, ErrTooManyWorkspaces
	}
	id := WorkspaceID(uuid.NewV7())
	err = s.queries.InsertWorkspace(ctx, tx, InsertWorkspaceParams{ID: uuid.UUID(id), SiteID: uuid.UUID(site), AccountID: uuid.UUID(account), Name: name})
	if err != nil {
		return WorkspaceInfo{}, fmt.Errorf("make a workspace: %w", workspaceErr(err))
	}
	if err := recordWorkspace(ctx, tx, actor, "workspace.create", id, uuid.UUID(site), account, map[string]any{"name": name}); err != nil {
		return WorkspaceInfo{}, err
	}
	return WorkspaceInfo{ID: id, Name: name}, nil
}

// UpdateWorkspace replaces the name of the account's workspace id and whether it is disabled, and records what
// changed in the audit log in the same transaction, in the shape a key's change has; a change that changes nothing is
// not recorded. Disabling refuses the next call of each of its keys on every node, because admission reads the
// workspace's state; the default workspace cannot be disabled (ErrDefaultWorkspace).
func (s *Service) UpdateWorkspace(ctx context.Context, tx pg.Tx, actor audit.Actor, account AccountID, id WorkspaceID, name string, disabled bool) (WorkspaceInfo, error) {
	rows, err := s.queries.LockWorkspace(ctx, tx, LockWorkspaceParams{AccountID: uuid.UUID(account), ID: uuid.UUID(id)})
	switch {
	case err != nil:
		return WorkspaceInfo{}, fmt.Errorf("change a workspace: %w", err)
	case len(rows) == 0:
		return WorkspaceInfo{}, ErrNotFound
	}
	before := rows[0]
	after := WorkspaceInfo{ID: id, Name: name, Default: before.IsDefault, Disabled: disabled}
	if disabled && before.IsDefault {
		return WorkspaceInfo{}, ErrDefaultWorkspace
	}
	changed := []string{}
	was, now := map[string]any{}, map[string]any{}
	if name != before.Name {
		changed, was["name"], now["name"] = append(changed, "name"), before.Name, name
	}
	if wasDisabled := before.Status == "disabled"; disabled != wasDisabled {
		changed, was["disabled"], now["disabled"] = append(changed, "disabled"), wasDisabled, disabled
	}
	if len(changed) == 0 {
		return after, nil
	}
	status := "active"
	if disabled {
		status = "disabled"
	}
	if err := s.queries.UpdateWorkspace(ctx, tx, UpdateWorkspaceParams{ID: uuid.UUID(id), Name: name, Status: status}); err != nil {
		return WorkspaceInfo{}, fmt.Errorf("change a workspace: %w", workspaceErr(err))
	}
	detail := map[string]any{"changed": changed, "before": was, "after": now}
	if err := recordWorkspace(ctx, tx, actor, "workspace.update", id, before.SiteID, account, detail); err != nil {
		return WorkspaceInfo{}, err
	}
	return after, nil
}

// workspaceErr is ErrWorkspaceNameTaken for the unique index on live workspaces' names (migration 00038), and err
// otherwise.
func workspaceErr(err error) error {
	if constraint, ok := pg.Duplicate(err); ok && constraint == "workspace_names" {
		return ErrWorkspaceNameTaken
	}
	return err
}

func recordWorkspace(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, id WorkspaceID, site uuid.UUID, account AccountID, detail map[string]any) error {
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: action, Detail: detail, Target: audit.Target{
		Module: "accounts", Kind: "workspace", ID: id.TypeID(), Site: site, Account: uuid.UUID(account),
	}})
}
