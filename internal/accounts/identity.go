package accounts

import (
	"context"
	"fmt"
	"net/netip"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// findIdentity reads the identity a provider subject names, if one is linked.
func (s *Service) findIdentity(ctx context.Context, site Site, provider, subject string) ([]FindIdentityRow, error) {
	var found []FindIdentityRow
	err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		var err error
		found, err = s.queries.FindIdentity(ctx, tx, FindIdentityParams{SiteID: uuid.UUID(site.ID), Provider: provider, Subject: subject})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("find the linked identity: %w", err)
	}
	return found, nil
}

// linkIdentity attaches the provider account to the signed-in user and raises the factor version, which signs every
// other session out; this browser gets a new session with the same level so the person stays signed in.
func (s *Service) linkIdentity(ctx context.Context, p Principal, site Site, provider string, identity oauthIdentity, ip netip.Addr, userAgent string) (Issued, error) {
	var issued Issued
	var err error
	err = s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		issued, err = s.attachIdentity(ctx, tx, p, site, provider, identity, ip, userAgent)
		return err
	})
	if err != nil {
		return Issued{}, err
	}
	return issued, nil
}

// attachIdentity inserts the identity under the user's lock and issues the replacement session.
func (s *Service) attachIdentity(ctx context.Context, tx pg.Tx, p Principal, site Site, provider string, identity oauthIdentity, ip netip.Addr, userAgent string) (Issued, error) {
	locked, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion)
	if err != nil {
		return Issued{}, err
	}
	if secondFactorUnproved(locked, p) {
		return Issued{}, ErrMFARequired
	}
	if err := s.queries.InsertIdentity(ctx, tx, InsertIdentityParams{
		ID: uuid.NewV7(), SiteID: uuid.UUID(site.ID), AccountID: uuid.UUID(p.account), UserID: uuid.UUID(p.user),
		Provider: provider, Subject: identity.subject, DisplayName: truncate(identity.display, 100),
	}); err != nil {
		return Issued{}, identityInsertError(err)
	}
	// The callback answers with a redirect, so the codes the link makes are sealed on the new session for the console
	// to read once (TakePendingRecoveryCodes).
	issued, _, err := s.finishFactorChange(ctx, tx, locked, factorFinish{
		session: newSession{
			user: uuid.UUID(p.user), account: uuid.UUID(p.account), site: site, surface: Console, level: resumedLevel(p),
			ip: ip, userAgent: userAgent,
		},
		totpOn:        locked.Mfa,
		sealOnSession: true,
	})
	if err != nil {
		return Issued{}, err
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: audit.User(uuid.UUID(p.user)), Action: "user.identity_link",
		Target: audit.Target{Module: "accounts", Kind: "user", ID: p.user.TypeID(), Site: uuid.UUID(site.ID), Account: uuid.UUID(p.account)},
		Detail: map[string]any{"provider": provider, "subject": identity.subject},
	}); err != nil {
		return Issued{}, err
	}
	return issued, nil
}

// identityInsertError maps a unique violation on a new identity to the refusal the page shows.
func identityInsertError(err error) error {
	constraint, duplicate := pg.Duplicate(err)
	switch {
	case !duplicate:
		return err
	case constraint == "user_identities_user_id_provider_key":
		return ErrIdentityAlreadyLinked
	default:
		return ErrIdentityLinked
	}
}

// UnlinkIdentity detaches a provider account from the signed-in user. It is a credential change: the user's factor
// version rises, every other session ends, and this browser gets a new session with the same level and, with TOTP on,
// new recovery codes.
func (s *Service) UnlinkIdentity(ctx context.Context, p Principal, provider string, ip netip.Addr, userAgent string) (Issued, []string, error) {
	if _, ok := s.providers[provider]; !ok {
		return Issued{}, nil, ErrNotFound
	}
	if p.Pending() {
		return Issued{}, nil, ErrMFARequired
	}
	if err := s.requireVerifiedForChange(ctx, p); err != nil {
		return Issued{}, nil, err
	}
	var issued Issued
	var codes []string
	err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		issued, codes, err = s.detachIdentity(ctx, tx, p, provider, ip, userAgent)
		return err
	})
	if err != nil {
		return Issued{}, nil, err
	}
	return issued, codes, nil
}

// detachIdentity deletes the identity under the user's lock and finishes the change.
func (s *Service) detachIdentity(ctx context.Context, tx pg.Tx, p Principal, provider string, ip netip.Addr, userAgent string) (Issued, []string, error) {
	locked, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion)
	if err != nil {
		return Issued{}, nil, err
	}
	changed, err := s.queries.DeleteIdentity(ctx, tx, DeleteIdentityParams{UserID: uuid.UUID(p.user), Provider: provider})
	if err != nil {
		return Issued{}, nil, err
	}
	if changed != 1 {
		return Issued{}, nil, ErrNotFound
	}
	issued, codes, err := s.finishFactorChange(ctx, tx, locked, factorFinish{
		session: newSession{
			user: uuid.UUID(p.user), account: uuid.UUID(p.account), site: s.snapshot.Load().sites[p.site],
			surface: p.surface, level: resumedLevel(p), ip: ip, userAgent: userAgent,
		},
		totpOn: locked.Mfa,
	})
	if err != nil {
		return Issued{}, nil, err
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: audit.User(uuid.UUID(p.user)), Action: "user.identity_unlink",
		Target: audit.Target{Module: "accounts", Kind: "user", ID: p.user.TypeID(), Site: uuid.UUID(p.site), Account: uuid.UUID(p.account)},
		Detail: map[string]any{"provider": provider},
	}); err != nil {
		return Issued{}, nil, err
	}
	return issued, codes, nil
}

// Identity is one provider account linked to a user, as the account page lists it.
type Identity struct {
	Provider    string
	DisplayName string
	CreatedAt   time.Time
}

// Identities lists a user's linked provider accounts.
func (s *Service) Identities(ctx context.Context, p Principal) ([]Identity, error) {
	var rows []ListIdentitiesRow
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		rows, err = s.queries.ListIdentities(ctx, tx, uuid.UUID(p.user))
		return err
	}); err != nil {
		return nil, fmt.Errorf("list linked identities: %w", err)
	}
	out := make([]Identity, 0, len(rows))
	for _, r := range rows {
		out = append(out, Identity{Provider: r.Provider, DisplayName: r.DisplayName, CreatedAt: r.CreatedAt.Time})
	}
	return out, nil
}
