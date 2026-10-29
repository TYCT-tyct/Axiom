package accounts

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// DefaultRateRequests is a business allowance over the gateway's rate window, one minute by default.
const DefaultRateRequests = 6000

// RateLimit is an administrator's allowance: nil inherits, zero is unlimited.
type RateLimit struct {
	Requests  *int32
	Effective int
}

// RateLimit reads the target's own and inherited business allowance in tx's scope.
func (s *Service) RateLimit(ctx context.Context, tx pg.Tx, target string, id uuid.UUID) (RateLimit, error) {
	rows, err := s.queries.ReadRateLimit(ctx, tx, ReadRateLimitParams{Target: target, ID: id})
	if err != nil {
		return RateLimit{}, fmt.Errorf("read business rate limit: %w", err)
	}
	if len(rows) == 0 {
		return RateLimit{}, ErrNotFound
	}
	return RateLimit{Requests: rows[0].Requests, Effective: rateDefault(int(rows[0].Effective))}, nil
}

func rateDefault(n int) int {
	if n < 0 {
		return DefaultRateRequests
	}
	return n
}

// SetRateLimit replaces only the administrator's rate setting, with its audit entry in the same locked transaction.
func (s *Service) SetRateLimit(ctx context.Context, tx pg.Tx, actor audit.Actor, target string, id uuid.UUID, requests *int32, reason string) error {
	if requests != nil && (*requests < 0 || *requests > 1_000_000_000) {
		return &SettingError{"requests", "must be from 0 to 1000000000"}
	}
	var err error
	object := audit.Target{Module: "accounts", Kind: target}
	switch target {
	case "tier":
		object.ID = TierID(id).TypeID()
		err = s.queries.LockRateTier(ctx, tx, id)
	case "account":
		object.ID = AccountID(id).TypeID()
		err = s.queries.LockRateAccount(ctx, tx, id)
	case "key":
		object.ID = KeyID(id).TypeID()
		err = s.queries.LockRateKey(ctx, tx, id)
	default:
		return &SettingError{"target", "must be tier, account or key"}
	}
	if err != nil {
		return fmt.Errorf("lock business rate target: %w", err)
	}
	rows, err := s.queries.ReadRateLimit(ctx, tx, ReadRateLimitParams{Target: target, ID: id})
	if err != nil {
		return fmt.Errorf("read business rate target: %w", err)
	}
	if len(rows) == 0 {
		return ErrNotFound
	}
	before := rows[0]
	if err := s.queries.WriteRateLimit(ctx, tx, WriteRateLimitParams{Target: target, ID: id, Requests: requests}); err != nil {
		return fmt.Errorf("write business rate limit: %w", err)
	}
	object.Site, object.Account = before.SiteID, before.AccountID
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "rate_limit.update", Reason: reason,
		Target: object,
		Detail: map[string]any{"before": before.Requests, "after": requests},
	})
}
