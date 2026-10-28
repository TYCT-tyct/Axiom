package accounts

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// KeyLimits are limits on the call keys of an account: how many it may hold that are not deleted, and how many it may
// make in an hour. A site has its own and an account may have its own over the site's; nil is not set, and the level
// above applies (README.md「API key」).
type KeyLimits struct{ MaxKeys, CreationsPerHour *int32 }

// EffectiveKeyLimits are the limits in force: every one is set.
type EffectiveKeyLimits struct{ MaxKeys, CreationsPerHour int }

// DefaultKeyLimits are the limits of a site that sets none: room for the integrations and the rotation a customer
// runs, and far from the rate a script makes keys at.
func DefaultKeyLimits() EffectiveKeyLimits {
	return EffectiveKeyLimits{MaxKeys: defaultMaxKeysPerAccount, CreationsPerHour: defaultKeyCreationsPerHour}
}

// The bounds the tables keep (00007_key_limits.sql), the same the contract states.
const (
	minKeyLimit = 1
	maxKeyLimit = 10_000
)

// Check reports the first limit outside its bounds, as a *SettingError.
func (v KeyLimits) Check() error {
	for _, c := range []struct {
		field string
		n     *int32
	}{{"max_keys", v.MaxKeys}, {"creations_per_hour", v.CreationsPerHour}} {
		if c.n != nil && (*c.n < minKeyLimit || *c.n > maxKeyLimit) {
			return &SettingError{c.field, fmt.Sprintf("must be from %d to %d", minKeyLimit, maxKeyLimit)}
		}
	}
	return nil
}

// over is the limits set here, and the ones of base where they are not.
func (v KeyLimits) over(base EffectiveKeyLimits) EffectiveKeyLimits {
	return EffectiveKeyLimits{MaxKeys: setting(v.MaxKeys, base.MaxKeys), CreationsPerHour: setting(v.CreationsPerHour, base.CreationsPerHour)}
}

func (v KeyLimits) detail() map[string]any {
	return map[string]any{"max_keys": v.MaxKeys, "creations_per_hour": v.CreationsPerHour}
}

// SiteKeyLimits reads the site's own limits and what they come to over the defaults; ErrNotFound when there is no
// such site.
func (s *Service) SiteKeyLimits(ctx context.Context, tx pg.Tx, site SiteID) (KeyLimits, EffectiveKeyLimits, error) {
	r, err := s.queries.ReadSiteKeyLimits(ctx, tx, uuid.UUID(site))
	if errors.Is(err, pg.ErrNoRows) {
		return KeyLimits{}, EffectiveKeyLimits{}, ErrNotFound
	}
	if err != nil {
		return KeyLimits{}, EffectiveKeyLimits{}, fmt.Errorf("read the site's key limits: %w", err)
	}
	own := KeyLimits{r.MaxKeysPerAccount, r.KeyCreationsPerHour}
	return own, own.over(DefaultKeyLimits()), nil
}

// SetSiteKeyLimits replaces the site's own key limits with set, which leaves its other settings alone, and records
// the change in the site's turn so that the entry names the values it replaced. A limit outside its bounds is a
// *SettingError, and no site ErrNotFound. The other nodes apply it when they reload the sites.
func (s *Service) SetSiteKeyLimits(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, set KeyLimits, reason string) error {
	if err := set.Check(); err != nil {
		return err
	}
	locked, err := s.queries.LockSite(ctx, tx, uuid.UUID(site))
	if err != nil {
		return fmt.Errorf("lock the site: %w", err)
	}
	if len(locked) == 0 {
		return ErrNotFound
	}
	before, _, err := s.SiteKeyLimits(ctx, tx, site)
	if err != nil {
		return err
	}
	if err := s.queries.WriteSiteKeyLimits(ctx, tx, WriteSiteKeyLimitsParams{
		SiteID: uuid.UUID(site), MaxKeys: set.MaxKeys, CreationsPerHour: set.CreationsPerHour,
	}); err != nil {
		return fmt.Errorf("write the site's key limits: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "site_key_limits.update", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)},
		Detail: map[string]any{"before": before.detail(), "after": set.detail()},
	})
}

// AccountKeyLimits reads the account's own limits and what they come to over its site's; ErrNotFound when there is no
// such account.
func (s *Service) AccountKeyLimits(ctx context.Context, tx pg.Tx, account AccountID) (KeyLimits, EffectiveKeyLimits, error) {
	r, err := s.queries.ReadAccountKeyLimits(ctx, tx, uuid.UUID(account))
	if errors.Is(err, pg.ErrNoRows) {
		return KeyLimits{}, EffectiveKeyLimits{}, ErrNotFound
	}
	if err != nil {
		return KeyLimits{}, EffectiveKeyLimits{}, fmt.Errorf("read the account's key limits: %w", err)
	}
	site := KeyLimits{r.SiteMaxKeys, r.SiteCreationsPerHour}.over(DefaultKeyLimits())
	own := KeyLimits{r.MaxKeys, r.KeyCreationsPerHour}
	return own, own.over(site), nil
}

// SetAccountKeyLimits replaces the account's own key limits with set and records the change, in the account's turn, the
// one a key is made in, so that the entry names the values it replaced and a key being made meets the limits whole,
// the new or the old. A limit outside its bounds is a *SettingError, and no account ErrNotFound.
func (s *Service) SetAccountKeyLimits(ctx context.Context, tx pg.Tx, actor audit.Actor, account AccountID, set KeyLimits, reason string) error {
	if err := set.Check(); err != nil {
		return err
	}
	before, err := s.queries.LockAccount(ctx, tx, uuid.UUID(account))
	if errors.Is(err, pg.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock the account: %w", err)
	}
	row, err := s.queries.ReadAccountKeyLimits(ctx, tx, uuid.UUID(account))
	if err != nil {
		return fmt.Errorf("read the account: %w", err)
	}
	if err := s.queries.WriteAccountKeyLimits(ctx, tx, WriteAccountKeyLimitsParams{
		AccountID: uuid.UUID(account), MaxKeys: set.MaxKeys, CreationsPerHour: set.CreationsPerHour,
	}); err != nil {
		return fmt.Errorf("write the account's key limits: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "account_key_limits.update", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "account", ID: account.TypeID(), Site: row.SiteID, Account: uuid.UUID(account)},
		Detail: map[string]any{"before": KeyLimits{before.MaxKeys, before.KeyCreationsPerHour}.detail(), "after": set.detail()},
	})
}
