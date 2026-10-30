package accounts

import (
	"context"
	"crypto/sha256"
	"fmt"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// SetSiteLogo replaces the site's logo with data, or clears it when data is nil, and records the change. An image that
// does not pass ParseLogo is a *SettingError, and an unknown site ErrNotFound. The site row is locked so two uploads
// cannot interleave their current hash and audit entries.
func (s *Service) SetSiteLogo(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, contentType string, data []byte, reason string) (*Logo, error) {
	var logo *Logo
	if data != nil {
		parsed, err := ParseLogo(contentType, data)
		if err != nil {
			return nil, err
		}
		logo = &parsed
	}
	locked, err := s.queries.LockSite(ctx, tx, uuid.UUID(site))
	if err != nil {
		return nil, fmt.Errorf("lock the site: %w", err)
	}
	if len(locked) == 0 {
		return nil, ErrNotFound
	}
	before, err := s.CurrentSiteLogo(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	var hash []byte
	if logo != nil {
		hash = logo.Hash[:]
		if err := s.queries.WriteSiteLogo(ctx, tx, WriteSiteLogoParams{
			SiteID: uuid.UUID(site), Hash: hash, ContentType: logo.ContentType,
			Width: int32(logo.Width), Height: int32(logo.Height), Bytes: logo.Bytes, // #nosec G115 -- ParseLogo bounds both to 1024
		}); err != nil {
			return nil, fmt.Errorf("write the site's logo: %w", err)
		}
	}
	if err := s.queries.SetSiteLogoHash(ctx, tx, SetSiteLogoHashParams{SiteID: uuid.UUID(site), Hash: hash}); err != nil {
		return nil, fmt.Errorf("set the site's logo hash: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "site_logo.update", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)},
		Detail: map[string]any{"before": logoDetail(before), "after": logoDetail(logo)},
	}); err != nil {
		return nil, err
	}
	return logo, nil
}

// CurrentSiteLogo reads the logo the site currently names; nil when it has none. The caller's transaction must hold the
// site's scope or the platform's, as every admin read does.
func (s *Service) CurrentSiteLogo(ctx context.Context, tx pg.Tx, site SiteID) (*Logo, error) {
	rows, err := s.queries.CurrentSiteLogo(ctx, tx, uuid.UUID(site))
	if err != nil {
		return nil, fmt.Errorf("read the site's logo: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil //nolint:nilnil // a site with no logo is not a failure
	}
	r := rows[0]
	logo := &Logo{ContentType: r.ContentType, Width: int(r.Width), Height: int(r.Height), Bytes: r.Bytes}
	copy(logo.Hash[:], r.Hash)
	return logo, nil
}

// SiteLogoByHash reads a logo the site has served at hash, current or earlier. The request's site comes from the
// snapshot, which found it by the request's Host; the hash is the only other input. A logo is public site
// configuration, so this read runs in the platform scope with both values bounded, and the bytes it returns are the
// ones the URL names.
func (s *Service) SiteLogoByHash(ctx context.Context, site SiteID, hash [sha256.Size]byte) (*Logo, error) {
	var logo *Logo
	err := s.db.ReadTx(ctx, s.platform, func(tx pg.Tx) error {
		rows, err := s.queries.ReadSiteLogo(ctx, tx, ReadSiteLogoParams{SiteID: uuid.UUID(site), Hash: hash[:]})
		if err != nil {
			return fmt.Errorf("read the site's logo: %w", err)
		}
		if len(rows) == 0 {
			return ErrNotFound
		}
		r := rows[0]
		logo = &Logo{ContentType: r.ContentType, Width: int(r.Width), Height: int(r.Height), Hash: hash, Bytes: r.Bytes}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return logo, nil
}
