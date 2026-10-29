package accounts

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// LoginLimits are the site's overrides; nil selects the corresponding code default.
type LoginLimits struct {
	IPAttemptBurst        *int32 `json:"ip_attempt_burst,omitempty"`
	IPAttemptsPerMinute   *int32 `json:"ip_attempts_per_minute,omitempty"`
	EmailSignInFailures   *int32 `json:"email_sign_in_failures,omitempty"`
	UserCodeFailures      *int32 `json:"user_code_failures,omitempty"`
	ChallengeCodeAttempts *int32 `json:"challenge_code_attempts,omitempty"`
}

// DefaultLoginLimits returns the same defaults that the login buckets use.
func DefaultLoginLimits() LoginLimits {
	// #nosec G115 -- all five code defaults are at most 20, within the database bounds.
	return LoginLimits{new(int32(defaultLimits.ipBurst)), new(int32(defaultLimits.ipPerMinute)), new(int32(defaultLimits.emailFailures)), new(int32(defaultLimits.codeFailures)), new(int32(defaultLimits.challengeAttempts))}
}

// Check rejects overrides outside the database and API bounds.
func (v LoginLimits) Check() error {
	for _, c := range []struct {
		name string
		n    *int32
		max  int32
	}{
		{"ip_attempt_burst", v.IPAttemptBurst, 10000},
		{"ip_attempts_per_minute", v.IPAttemptsPerMinute, 10000},
		{"email_sign_in_failures", v.EmailSignInFailures, 1000},
		{"user_code_failures", v.UserCodeFailures, 1000},
		{"challenge_code_attempts", v.ChallengeCodeAttempts, 100},
	} {
		if c.n != nil && (*c.n < 1 || *c.n > c.max) {
			return &SettingError{c.name, fmt.Sprintf("must be from 1 to %d", c.max)}
		}
	}
	return nil
}

// SiteLoginLimits reads the site's own overrides in the caller's platform transaction.
func (s *Service) SiteLoginLimits(ctx context.Context, tx pg.Tx, site SiteID) (LoginLimits, error) {
	r, err := s.queries.ReadLoginLimits(ctx, tx, uuid.UUID(site))
	if errors.Is(err, pg.ErrNoRows) {
		return LoginLimits{}, ErrNotFound
	}
	if err != nil {
		return LoginLimits{}, fmt.Errorf("read login limits: %w", err)
	}
	return LoginLimits{r.IpAttemptBurst, r.IpAttemptsPerMinute, r.EmailSignInFailures, r.UserCodeFailures, r.ChallengeCodeAttempts}, nil
}

// SetSiteLoginLimits serializes replacement and audit without touching other site settings.
func (s *Service) SetSiteLoginLimits(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, set LoginLimits, reason string) error {
	if err := set.Check(); err != nil {
		return err
	}
	rows, err := s.queries.LockSite(ctx, tx, uuid.UUID(site))
	if err != nil {
		return fmt.Errorf("lock login limits site: %w", err)
	}
	if len(rows) == 0 {
		return ErrNotFound
	}
	before, err := s.SiteLoginLimits(ctx, tx, site)
	if err != nil {
		return err
	}
	if err = s.queries.WriteLoginLimits(ctx, tx, WriteLoginLimitsParams{SiteID: uuid.UUID(site), IpAttemptBurst: set.IPAttemptBurst, IpAttemptsPerMinute: set.IPAttemptsPerMinute, EmailSignInFailures: set.EmailSignInFailures, UserCodeFailures: set.UserCodeFailures, ChallengeCodeAttempts: set.ChallengeCodeAttempts}); err != nil {
		return fmt.Errorf("write login limits: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: "site_login_limits.update", Reason: reason, Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)}, Detail: map[string]any{"before": before, "after": set}})
}
