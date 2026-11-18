package accounts

import (
	"context"
	"fmt"
	"slices"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// SessionInfo is what a page shows of the session it is in: who is signed in, the CSRF token it must send back, how
// long the session lasts if it is not used again, and what it may do on its own scope: the user's account on the
// console, the platform on the admin.
type SessionInfo struct {
	User        UserID
	Account     AccountID
	Email       string
	InviteCode  string
	Level       string // pending_mfa, password or mfa
	MFAEnabled  bool   // two-step verification, and on the console a passkey too
	TOTPEnabled bool
	Passkeys    int
	CSRFToken   string
	ExpiresAt   time.Time
	Permissions []string // sorted; none while the session waits for its second factor
}

// SessionInfo reads p's session. The permissions are those of p's roles in its own account that the session's level
// allows: a sensitive one needs two-step verification, and a pending session has none. The server decides again on
// every request (Authorize); this list only tells the page what to show.
func (s *Service) SessionInfo(ctx context.Context, p Principal) (SessionInfo, error) {
	var rows []SessionInfoRow
	life := s.snapshot.Load().sites[p.site].sessions[p.surface]
	maxAge, idle := life.seconds()
	var roles []uuid.UUID
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) (err error) {
		if rows, err = s.queries.SessionInfo(ctx, tx, SessionInfoParams{ID: uuid.UUID(p.session), MaxAgeSeconds: maxAge, IdleSeconds: idle}); err != nil {
			return err
		}
		if p.surface == Admin { // the admin session acts on the platform: its roles are the user's platform grants
			roles, err = s.queries.PlatformRoles(ctx, tx, uuid.UUID(p.user))
			return err
		}
		roles, err = s.queries.MemberRoles(ctx, tx, MemberRolesParams{AccountID: uuid.UUID(p.account), UserID: uuid.UUID(p.user)})
		return err
	}); err != nil {
		return SessionInfo{}, fmt.Errorf("read the session: %w", err)
	}
	if len(rows) == 0 {
		return SessionInfo{}, ErrNoSession
	}
	r := rows[0]
	return SessionInfo{
		User: p.user, Account: p.account, Email: r.Email, InviteCode: r.InviteCode, Level: p.level,
		// A passkey protects the console but not the admin listener, which cannot run WebAuthn; an admin session
		// reports only TOTP as its second factor so the page does not claim a protection it cannot use.
		MFAEnabled: r.Mfa || (r.Passkeys > 0 && p.surface == Console), TOTPEnabled: r.Mfa, Passkeys: int(r.Passkeys),
		CSRFToken: r.CsrfToken, ExpiresAt: r.ExpiresAt.Time, Permissions: s.permissionsOf(p, roles),
	}, nil
}

// AccountTier is the tier whose sale prices the session's account pays. It reads the account row itself, not the site
// snapshot: a tier change takes effect on the next read, as it does for admission.
func (s *Service) AccountTier(ctx context.Context, p Principal) (TierID, error) {
	var tier uuid.UUID
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) (err error) {
		tier, err = s.queries.AccountTier(ctx, tx, uuid.UUID(p.account))
		return err
	}); err != nil {
		return TierID{}, fmt.Errorf("read the account's tier: %w", err)
	}
	return TierID(tier), nil
}

// permissionsOf is the names of the permissions roles hold that p's session level allows, sorted.
func (s *Service) permissionsOf(p Principal, roles []uuid.UUID) []string {
	names := []string{}
	if p.level == levelPending {
		return names
	}
	held := s.snapshot.Load().roles
	for name, declared := range permissions {
		if declared.sensitive && p.level != levelMFA {
			continue
		}
		if slices.ContainsFunc(roles, func(role uuid.UUID) bool { return held[role][name] }) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
