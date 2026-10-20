package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
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
	// ErrBadEmail is an address the table would refuse: not 3 to 254 printable ASCII characters around an @.
	ErrBadEmail = errors.New("accounts: not an email address")
)

// OpenedFunc runs in the transaction that opens an account, once the account exists, so that a module above accounts
// can give the account what it needs to exist there too (billing's balance row) and the account is never seen
// without it. cmd/axiom supplies it: accounts may not import the modules above it.
type OpenedFunc func(ctx context.Context, tx pg.Tx, site, account uuid.UUID) error

// RegisterRequest is a registration with an invite code at a console domain.
type RegisterRequest struct {
	Host, InviteCode, Email, Password string
	ClientIP                          netip.Addr
}

// Register makes a user with an invite code: the invitation is checked in the database (site enabled, registering by
// invitation, the inviter enabled), then, in a transaction scoped to the new account alone, its personal account in
// the site's default tier, registered as a tenant, its default workspace, the user and the user as the account's
// owner. A taken email fails the whole transaction. It spends one of the IP's attempts, and the hash one of the
// node's turns. The attempt is recorded as a security event, refused or not.
func (s *Service) Register(ctx context.Context, r RegisterRequest) (registered UserID, err error) {
	d, snap, err := s.domain(r.Host)
	if err != nil {
		return UserID{}, err
	}
	event := securityEvent{kind: eventSignUp, site: d.site, ip: r.ClientIP}
	defer func() {
		if event.outcome(err) {
			s.record(ctx, event)
		}
	}()
	if d.surface != Console {
		return UserID{}, ErrUnknownHost
	}
	if err := s.fromIP(d.site, r.ClientIP); err != nil {
		return UserID{}, err
	}
	email := normalEmail(r.Email)
	if !emailAllowed(email) {
		return UserID{}, ErrBadEmail
	}
	if !passwordAllowed(r.Password) {
		return UserID{}, ErrWeakPassword
	}
	invite, err := s.invite(ctx, r.Host, r.InviteCode)
	if err != nil {
		return UserID{}, err
	}
	var hash string
	if err := s.hash(ctx, func() { hash = hashPassword(r.Password) }); err != nil {
		return UserID{}, err
	}
	user, account := uuid.NewV7(), uuid.NewV7()
	err = s.db.Tx(ctx, pg.AccountScope(account), func(tx pg.Tx) error {
		return createUser(ctx, tx, s.queries, s.opened, newUser{
			id: user, account: account, site: invite.SiteID, tier: invite.DefaultTierID, role: snap.roleIDs["owner"],
			email: email, passwordHash: hash, inviter: &invite.InviterUserID,
		})
	})
	if constraint, ok := pg.Duplicate(err); ok && constraint == "users_site_id_email_key" {
		return UserID{}, ErrEmailTaken
	}
	if err != nil {
		return UserID{}, fmt.Errorf("register: %w", err)
	}
	registered = UserID(user)
	event.user, event.account = registered, AccountID(account)
	return registered, nil
}

// newUser is a user and the personal account that comes with them. inviter is nil for a user the command line makes.
type newUser struct {
	id, account, site, tier, role uuid.UUID
	email, passwordHash           string
	inviter                       *uuid.UUID
}

// createUser inserts n's account, its tenant registration, its default workspace, the user and the user as the
// account's owner in tx, which must hold the new account's scope, then runs opened. Registration and the command line
// make users through this one body.
func createUser(ctx context.Context, tx pg.Tx, q *Queries, opened OpenedFunc, n newUser) error {
	for _, step := range []func() error{
		func() error {
			return q.InsertAccount(ctx, tx, InsertAccountParams{ID: n.account, SiteID: n.site, Name: truncate(n.email, 100), TierID: n.tier})
		},
		func() error { return audit.RegisterAccount(ctx, tx, n.site, n.account) },
		func() error {
			return q.InsertDefaultWorkspace(ctx, tx, InsertDefaultWorkspaceParams{ID: uuid.NewV7(), SiteID: n.site, AccountID: n.account})
		},
		func() error {
			return q.InsertUser(ctx, tx, InsertUserParams{
				ID: n.id, SiteID: n.site, AccountID: n.account, Email: n.email, PasswordHash: n.passwordHash,
				InviteCode: newInviteCode(), InviterUserID: n.inviter,
			})
		},
		func() error {
			return q.InsertOwner(ctx, tx, InsertOwnerParams{SiteID: n.site, AccountID: n.account, UserID: n.id, RoleID: n.role})
		},
		func() error {
			if opened == nil {
				return nil
			}
			return opened(ctx, tx, n.site, n.account)
		},
	} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// emailAllowed reports whether email, already normalized, is one the users table takes: 3 to 254 printable ASCII
// characters (what lower() and the length check agree on in every locale) with an @ that has a character on each side.
func emailAllowed(email string) bool {
	if len(email) < 3 || len(email) > 254 {
		return false
	}
	for i := range len(email) {
		if email[i] <= ' ' || email[i] > '~' {
			return false
		}
	}
	return strings.Contains(email[1:len(email)-1], "@")
}

// invite is what code admits at host's site, or ErrInvalidInvite.
func (s *Service) invite(ctx context.Context, host, code string) (InviteTenantRow, error) {
	var invites []InviteTenantRow
	if err := s.db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		var err error
		invites, err = s.queries.InviteTenant(ctx, tx, InviteTenantParams{Domain: strings.ToLower(host), Code: code})
		return err
	}); err != nil {
		return InviteTenantRow{}, fmt.Errorf("register: %w", err)
	}
	if len(invites) == 0 {
		return InviteTenantRow{}, ErrInvalidInvite
	}
	return invites[0], nil
}

// newInviteCode is a user's own invite code: 16 characters, 80 random bits.
func newInviteCode() string {
	return strings.ReplaceAll(newRecoveryCode(), "-", "")
}
