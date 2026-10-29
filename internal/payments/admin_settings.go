package payments

import (
	"context"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// SiteSettings is the system page's collection switch and accepted credit range, in billionths of a credit.
type SiteSettings struct {
	Open     bool
	Min, Max *int64
}

// ReadSiteSettings resolves the collection controls with the existing defaults.
func ReadSiteSettings(ctx context.Context, tx pg.Tx, site uuid.UUID) (SiteSettings, error) {
	s, err := readSettings(ctx, tx, site)
	return SiteSettings{Open: s.open, Min: s.creditsMin, Max: s.creditsMax}, err
}

// SetSiteSettings changes only collection controls and records their previous values atomically.
func SetSiteSettings(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, s SiteSettings, reason string) error {
	if s.Min != nil && *s.Min <= 0 || s.Max != nil && *s.Max <= 0 || s.Min != nil && s.Max != nil && *s.Max < *s.Min {
		return &FieldError{"/credits_min", "must be positive and no greater than credits_max"}
	}
	// Serialize this resource even before the first payments.settings row exists.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "payments.settings:"+site.String()); err != nil {
		return err
	}
	before, err := ReadSiteSettings(ctx, tx, site)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO payments.settings(site_id,open,credits_min,credits_max) VALUES($1,$2,$3,$4)
 ON CONFLICT(site_id) DO UPDATE SET open=excluded.open,credits_min=excluded.credits_min,credits_max=excluded.credits_max`, site, s.Open, s.Min, s.Max)
	if err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: "payment_settings.update", Reason: reason, Target: audit.Target{Module: "payments", Kind: "site", ID: site.String(), Site: site}, Detail: map[string]any{"before": before, "after": s}})
}

// TestMerchant performs the channel's authenticated, read-only probe outside a transaction.
func (p *Payments) TestMerchant(ctx context.Context, scope pg.Scope, id MerchantID) (string, error) {
	var ch Channel
	err := p.db.ReadTx(ctx, scope, func(tx pg.Tx) (err error) { ch, err = p.channel(ctx, tx, id); return err })
	if err != nil {
		return "", err
	}
	prober, ok := ch.(interface{ testConfig(context.Context) error })
	if !ok {
		return "unsupported", nil
	}
	status := "accepted"
	if prober.testConfig(ctx) != nil {
		status = "failed"
	}
	return status, nil
}
