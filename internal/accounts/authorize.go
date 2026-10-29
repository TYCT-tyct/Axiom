package accounts

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// Permission is one thing an operator may do. Sensitive ones need a session that passed two-step verification.
type Permission struct {
	name      string
	sensitive bool
}

// The permissions the code declares (README.md「授权与租户范围」). Roles name them; snapshots ignore names introduced by newer nodes. A migration gives the built-in roles theirs.
var (
	ViewKeys             = Permission{"api_keys.view", false}
	ManageKeys           = Permission{"api_keys.manage", false}
	ManageWorkspaces     = Permission{"workspaces.manage", false}
	ViewCalls            = Permission{"calls.view", false}
	TopUpBalance         = Permission{"balance.top_up", false}
	ViewBalance          = Permission{"balance.view", false}
	ManageSites          = Permission{"sites.manage", true}
	ManagePlatformGrants = Permission{"platform_grants.manage", true}

	// The admin API's, asked of the platform. The ones that change money, credentials, prices or the catalog are sensitive.
	ManageUsers     = Permission{"users.manage", true}
	SearchCalls     = Permission{"calls.search", true}
	InspectCalls    = Permission{"calls.inspect", true}
	ViewAudit       = Permission{"audit.view", true}
	ViewUsers       = Permission{"users.view", false}
	GrantCredits    = Permission{"credits.grant", true}
	ViewSuppliers   = Permission{"suppliers.view", false}
	ManageSuppliers = Permission{"suppliers.manage", true}
	ViewCatalog     = Permission{"catalog.view", false}
	ManageCatalog   = Permission{"catalog.manage", true}
	ViewPricing     = Permission{"pricing.view", false}
	ManagePricing   = Permission{"pricing.manage", true}
)

var permissions = func() map[string]Permission {
	declared := map[string]Permission{}
	for _, p := range []Permission{
		ViewKeys, ManageKeys, ManageWorkspaces, ViewCalls, ViewBalance, TopUpBalance, ManageUsers, ManageSites, ManagePlatformGrants,
		SearchCalls, InspectCalls, ViewAudit, ViewUsers, GrantCredits, ViewSuppliers, ManageSuppliers, ViewCatalog, ManageCatalog, ViewPricing, ManagePricing,
	} {
		declared[p.name] = p
	}
	return declared
}()

// Name is the permission's name, as roles and interface contracts spell it.
func (p Permission) Name() string { return p.name }

// PermissionNamed is the declared permission called name, for an interface contract that names the permission an
// operation needs.
func PermissionNamed(name string) (Permission, bool) {
	p, ok := permissions[name]
	return p, ok
}

var (
	// ErrForbidden is an operator who may see the target but not do this to it.
	ErrForbidden = errors.New("accounts: forbidden")
	// ErrNotFound is a target the operator has no part in, answered as if it did not exist.
	ErrNotFound = errors.New("accounts: not found")
)

// Target is what an operation acts on: an account, or the platform.
type Target struct {
	account  AccountID
	platform bool
}

// AccountTarget is account, resolved by the server from the request's URL.
func AccountTarget(account AccountID) Target { return Target{account: account} }

// PlatformTarget is the platform, which only the admin acts on.
func PlatformTarget() Target { return Target{platform: true} }

// Authorize returns the scope of exactly the target if p may do perm to it. A sensitive permission needs a session
// that passed two-step verification; the platform needs, besides, a session on the admin surface and an unrevoked
// platform grant whose role holds perm, read from the database on every call. An account needs p's membership in it
// whose role holds perm; in M1 that is only p's own personal account, so any other is ErrNotFound.
func (s *Service) Authorize(ctx context.Context, p Principal, perm Permission, t Target) (pg.Scope, error) {
	if p.user == (UserID{}) {
		return pg.Scope{}, ErrForbidden
	}
	if (perm.sensitive || t.platform) && p.level != levelMFA {
		return pg.Scope{}, ErrForbidden
	}
	if t.platform && p.surface != Admin {
		return pg.Scope{}, ErrNotFound
	}
	roles, err := s.rolesFor(ctx, p, t)
	if err != nil {
		return pg.Scope{}, err
	}
	if len(roles) == 0 {
		return pg.Scope{}, ErrNotFound
	}
	held := s.snapshot.Load().roles
	for _, role := range roles {
		if held[role][perm.name] {
			if t.platform {
				return s.platform, nil
			}
			// The site is the one the session was issued at, which is where its account lives: it lets the scope read that
			// site's public configuration and shared counters, such as the payment methods of its top-up page.
			return pg.AccountScopeIn(uuid.UUID(p.site), uuid.UUID(t.account)), nil
		}
	}
	return pg.Scope{}, ErrForbidden
}

// rolesFor reads, in p's own account scope, the roles p holds on t: its unrevoked platform grants, or its membership
// in the account.
func (s *Service) rolesFor(ctx context.Context, p Principal, t Target) ([]uuid.UUID, error) {
	var roles []uuid.UUID
	err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		if t.platform {
			roles, err = s.queries.PlatformRoles(ctx, tx, uuid.UUID(p.user))
		} else {
			roles, err = s.queries.MemberRoles(ctx, tx, MemberRolesParams{AccountID: uuid.UUID(t.account), UserID: uuid.UUID(p.user)})
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("authorize: %w", err)
	}
	return roles, nil
}
