package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

var (
	// ErrInvalidInvite is an invite code that does not admit a registration at the request's Host: unknown, of a
	// disabled user, or on a site that does not register by invitation.
	ErrInvalidInvite = errors.New("accounts: invalid invite code")
	// ErrEmailTaken is an email already registered on the site.
	ErrEmailTaken = errors.New("accounts: email already registered")
	// ErrWeakPassword is a password shorter than 12 characters or longer than 256.
	ErrWeakPassword = errors.New("accounts: password must be 12 to 256 characters")
)

// RegisterRequest is a registration with an invite code at a console domain.
type RegisterRequest struct {
	Host, InviteCode, Email, Password string
}

// Register makes a user with an invite code: the invitation is checked in the database (site enabled, registering by
// invitation, the inviter enabled), then, in a transaction scoped to the new account alone, its personal account in
// the site's default tier, registered as a tenant, its default workspace, the user and the user as the account's
// owner. A taken email fails the whole transaction.
func (s *Service) Register(ctx context.Context, r RegisterRequest) (UserID, error) {
	d, snap, err := s.domain(r.Host)
	if err != nil {
		return UserID{}, err
	}
	if d.surface != Console {
		return UserID{}, ErrUnknownHost
	}
	if !passwordAllowed(r.Password) {
		return UserID{}, ErrWeakPassword
	}
	var invites []InviteTenantRow
	if err := s.db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		invites, err = s.queries.InviteTenant(ctx, tx, InviteTenantParams{Domain: strings.ToLower(r.Host), Code: r.InviteCode})
		return err
	}); err != nil {
		return UserID{}, fmt.Errorf("register: %w", err)
	}
	if len(invites) == 0 {
		return UserID{}, ErrInvalidInvite
	}
	invite := invites[0]
	email := normalEmail(r.Email)
	hash := hashPassword(r.Password)
	user, account := uuid.NewV7(), uuid.NewV7()
	site := invite.SiteID
	err = s.db.Tx(ctx, pg.AccountScope(account), func(tx pg.Tx) error {
		for _, step := range []func() error{
			func() error {
				return s.queries.InsertAccount(ctx, tx, InsertAccountParams{ID: account, SiteID: site, Name: truncate(email, 100), TierID: invite.DefaultTierID})
			},
			func() error { return audit.RegisterAccount(ctx, tx, site, account) },
			func() error {
				return s.queries.InsertDefaultWorkspace(ctx, tx, InsertDefaultWorkspaceParams{ID: uuid.NewV7(), SiteID: site, AccountID: account})
			},
			func() error {
				return s.queries.InsertUser(ctx, tx, InsertUserParams{
					ID: user, SiteID: site, AccountID: account, Email: email, PasswordHash: hash,
					InviteCode: newInviteCode(), InviterUserID: &invite.InviterUserID,
				})
			},
			func() error {
				return s.queries.InsertOwner(ctx, tx, InsertOwnerParams{SiteID: site, AccountID: account, UserID: user, RoleID: snap.roleIDs["owner"]})
			},
		} {
			if err := step(); err != nil {
				return err
			}
		}
		return nil
	})
	if constraint, ok := pg.Duplicate(err); ok && constraint == "users_site_id_email_key" {
		return UserID{}, ErrEmailTaken
	}
	if err != nil {
		return UserID{}, fmt.Errorf("register: %w", err)
	}
	return UserID(user), nil
}

// newInviteCode is a user's own invite code: 16 characters, 80 random bits.
func newInviteCode() string {
	return strings.ReplaceAll(newRecoveryCode(), "-", "")
}
