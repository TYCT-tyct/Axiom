package payments

import (
	"context"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
)

// SiteSettings is the system page's collection switch and accepted credit range, in billionths of a credit.
type SiteSettings struct {
	Open bool   `json:"open"`
	Min  *int64 `json:"credits_min"`
	Max  *int64 `json:"credits_max"`
}

// ReadSiteSettings resolves the collection controls with the existing defaults.
func ReadSiteSettings(ctx context.Context, tx pg.Tx, site uuid.UUID) (SiteSettings, error) {
	s, err := readSettings(ctx, tx, site)
	return SiteSettings{Open: s.open, Min: s.creditsMin, Max: s.creditsMax}, err
}

// SetSiteSettings changes only collection controls and records their previous values atomically.
func SetSiteSettings(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, s SiteSettings, reason string) error {
	switch {
	case s.Min != nil && *s.Min <= 0:
		return &FieldError{"/credits_min", "must be positive"}
	case s.Max != nil && *s.Max <= 0:
		return &FieldError{"/credits_max", "must be positive"}
	case s.Min != nil && s.Max != nil && *s.Max < *s.Min:
		return &FieldError{"/credits_max", "must be no less than credits_min"}
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

// TestMerchant runs in the caller's idempotency claim transaction, using its connection for the binding read.
// The outbound query has its own deadline; keeping the claim open prevents committed tests being repeated.
func (p *Payments) TestMerchant(ctx context.Context, tx pg.Tx, id MerchantID) (string, error) {
	ch, err := p.channel(ctx, tx, id)
	if err != nil {
		return "", err
	}
	prober, ok := ch.(interface{ testConfig(context.Context) error })
	if !ok {
		return "unsupported", nil
	}
	status := "accepted"
	if err := prober.testConfig(ctx); err != nil {
		p.logger.WarnContext(ctx, "merchant configuration test failed", "merchant", id.TypeID(), "request_id", httpserver.RequestID(ctx), "err", err)
		status = "failed"
	}
	return status, nil
}
