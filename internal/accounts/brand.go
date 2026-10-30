package accounts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// SiteBrand is a site's brand colour: the #rrggbb the console derives both colour schemes from, or empty for the
// default palette (docs/frontend.md「主题与自由度」).
type SiteBrand struct {
	Color string `json:"brand_color,omitempty"`
}

var brandColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Check reports a colour that is neither empty nor #rrggbb and normalizes a valid one to the table's lower case.
func (b *SiteBrand) Check() error {
	if b.Color == "" {
		return nil
	}
	if !brandColorPattern.MatchString(b.Color) {
		return &SettingError{"brand_color", "must be #rrggbb or empty"}
	}
	b.Color = strings.ToLower(b.Color)
	return nil
}

func (b SiteBrand) detail() map[string]any { return map[string]any{"brand_color": b.Color} }

// ReadSiteBrand reads the site's brand colour; ErrNotFound when there is no such site.
func ReadSiteBrand(ctx context.Context, tx pg.Tx, site SiteID) (SiteBrand, error) {
	var color *string
	err := tx.QueryRow(ctx, `SELECT brand_color FROM accounts.sites WHERE id=$1`, uuid.UUID(site)).Scan(&color)
	if errors.Is(err, pg.ErrNoRows) {
		return SiteBrand{}, ErrNotFound
	}
	if err != nil {
		return SiteBrand{}, fmt.Errorf("read the site's brand colour: %w", err)
	}
	if color != nil {
		return SiteBrand{Color: *color}, nil
	}
	return SiteBrand{}, nil
}

// SetSiteBrand replaces the site's brand colour and records the change, in the site's turn so that the entry names the
// colour it replaced. A value that is not #rrggbb is a *SettingError, and no site ErrNotFound.
func (s *Service) SetSiteBrand(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, set SiteBrand, reason string) error {
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
	before, err := ReadSiteBrand(ctx, tx, site)
	if err != nil {
		return err
	}
	var color *string
	if set.Color != "" {
		color = &set.Color
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts.sites SET brand_color=$2 WHERE id=$1`, uuid.UUID(site), color); err != nil {
		return fmt.Errorf("write the site's brand colour: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "site_brand.update", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)},
		Detail: map[string]any{"before": before.detail(), "after": set.detail()},
	})
}
