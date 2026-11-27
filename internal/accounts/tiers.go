package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// A tier is a set of account defaults a site's operator names, adds, renames, disables and makes the default
// (docs/domains/accounts.md「等级」). What a tier pays is not here: a pricing rule for the tier sets it.
//
// Invariant: the site's default tier is never disabled. CreateTier, UpdateTier and SetDefaultTier lock the site row
// before the tier's, so a disable and a change of default queue, and each refuses what would break it after the wait.
var (
	// ErrTierNameTaken is a name another tier of the site has, ignoring case.
	ErrTierNameTaken = errors.New("accounts: a tier of the site has that name")
	// ErrDefaultTier is disabling the tier new accounts join: another tier must be made the default first.
	ErrDefaultTier = errors.New("accounts: the site's default tier cannot be disabled; make another tier the default first")
)

// maxTierName is the longest tier name, as the table's check has it.
const maxTierName = 100

// TierChange is what UpdateTier changes; a nil member stays as it is.
type TierChange struct {
	Name     *string
	Disabled *bool
}

// tierName is name trimmed, or a *SettingError naming the field when it is empty or too long.
func tierName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > maxTierName {
		return "", &SettingError{"name", fmt.Sprintf("is 1 to %d characters", maxTierName)}
	}
	return name, nil
}

// CreateTier adds an enabled tier to the site. Accounts join it only when moved there or when it becomes the default.
func (*Service) CreateTier(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, name, reason string) (Tier, error) {
	name, err := tierName(name)
	if err != nil {
		return Tier{}, err
	}
	if err := lockTierSite(ctx, tx, site); err != nil {
		return Tier{}, err
	}
	tier := Tier{ID: TierID(uuid.NewV7()), Name: name}
	if _, err := tx.Exec(ctx, `INSERT INTO accounts.tiers (id, site_id, name) VALUES ($1, $2, $3)`,
		uuid.UUID(tier.ID), uuid.UUID(site), name); err != nil {
		return Tier{}, tierErr(err)
	}
	return tier, recordTier(ctx, tx, actor, "tier.create", tier.ID, site, reason, map[string]any{"name": name})
}

// UpdateTier renames, disables or enables a tier. Disabling the site's default tier is ErrDefaultTier; a change to
// what the tier already is changes nothing and writes no audit entry.
func (*Service) UpdateTier(ctx context.Context, tx pg.Tx, actor audit.Actor, id TierID, c TierChange, reason string) (Tier, error) {
	site, err := tierSite(ctx, tx, id)
	if err != nil {
		return Tier{}, err
	}
	if err := lockTierSite(ctx, tx, site); err != nil {
		return Tier{}, err
	}
	before, err := lockTier(ctx, tx, site, id)
	if err != nil {
		return Tier{}, err
	}
	after := before
	if c.Name != nil {
		if after.Name, err = tierName(*c.Name); err != nil {
			return Tier{}, err
		}
	}
	if c.Disabled != nil {
		after.Disabled = *c.Disabled
	}
	if after == before {
		return after, nil
	}
	if after.Disabled && after.Default {
		return Tier{}, ErrDefaultTier
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts.tiers SET name = $2, disabled = $3 WHERE id = $1`,
		uuid.UUID(id), after.Name, after.Disabled); err != nil {
		return Tier{}, tierErr(err)
	}
	return after, recordTier(ctx, tx, actor, "tier.update", id, site, reason, map[string]any{
		"before": map[string]any{"name": before.Name, "disabled": before.Disabled},
		"after":  map[string]any{"name": after.Name, "disabled": after.Disabled},
	})
}

// SetDefaultTier makes tier the one its site's new accounts join, read from the database at sign-up. Accounts already
// on another tier stay there. A disabled tier is a *SettingError naming tier_id; the current default changes nothing
// and writes no audit entry.
func (*Service) SetDefaultTier(ctx context.Context, tx pg.Tx, actor audit.Actor, tier TierID, reason string) error {
	site, err := tierSite(ctx, tx, tier)
	if err != nil {
		return err
	}
	if err := lockTierSite(ctx, tx, site); err != nil {
		return err
	}
	target, err := lockTier(ctx, tx, site, tier)
	switch {
	case err != nil:
		return err
	case target.Default:
		return nil
	case target.Disabled:
		return &SettingError{"tier_id", "is disabled"}
	}
	var before string
	if err := tx.QueryRow(ctx, `SELECT t.name FROM accounts.sites s JOIN accounts.tiers t ON t.site_id = s.id AND t.id = s.default_tier_id
		WHERE s.id = $1`, uuid.UUID(site)).Scan(&before); err != nil {
		return fmt.Errorf("read the default tier: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts.sites SET default_tier_id = $2 WHERE id = $1`, uuid.UUID(site), uuid.UUID(tier)); err != nil {
		return fmt.Errorf("set the default tier: %w", err)
	}
	return recordTier(ctx, tx, actor, "site.default_tier_change", tier, site, reason, map[string]any{"before": before, "after": target.Name})
}

// tierSite is the site of a tier, which never changes; ErrNotFound for a tier the scope cannot see.
func tierSite(ctx context.Context, tx pg.Tx, id TierID) (SiteID, error) {
	var site uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT site_id FROM accounts.tiers WHERE id = $1`, uuid.UUID(id)).Scan(&site); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return SiteID{}, ErrNotFound
		}
		return SiteID{}, fmt.Errorf("read the tier: %w", err)
	}
	return SiteID(site), nil
}

// lockTierSite takes the site's turn (LockSite), which every change to its tiers takes before the tier's row.
func lockTierSite(ctx context.Context, tx pg.Tx, site SiteID) error {
	rows, err := New().LockSite(ctx, tx, uuid.UUID(site))
	if err != nil {
		return fmt.Errorf("lock the site: %w", err)
	}
	if len(rows) == 0 {
		return ErrNotFound
	}
	return nil
}

// lockTier locks a tier of site, after the site's turn.
func lockTier(ctx context.Context, tx pg.Tx, site SiteID, id TierID) (Tier, error) {
	t := Tier{ID: id}
	err := tx.QueryRow(ctx, `SELECT t.name, t.disabled, s.default_tier_id = t.id FROM accounts.tiers t
		JOIN accounts.sites s ON s.id = t.site_id WHERE t.site_id = $1 AND t.id = $2 FOR NO KEY UPDATE OF t`,
		uuid.UUID(site), uuid.UUID(id)).Scan(&t.Name, &t.Disabled, &t.Default)
	if errors.Is(err, pg.ErrNoRows) {
		return Tier{}, ErrNotFound
	}
	if err != nil {
		return Tier{}, fmt.Errorf("lock the tier: %w", err)
	}
	return t, nil
}

// tierErr is ErrTierNameTaken for the unique index on a site's tier names (migration 00050), and err otherwise.
func tierErr(err error) error {
	if constraint, ok := pg.Duplicate(err); ok && constraint == "tiers_site_name" {
		return ErrTierNameTaken
	}
	return fmt.Errorf("write the tier: %w", err)
}

func recordTier(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, id TierID, site SiteID, reason string, detail map[string]any) error {
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: action, Reason: reason, Detail: detail, Target: audit.Target{
		Module: "accounts", Kind: "tier", ID: id.TypeID(), Site: uuid.UUID(site),
	}})
}
