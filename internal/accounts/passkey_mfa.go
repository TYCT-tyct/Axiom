package accounts

import (
	"context"
	"errors"
	"uuid"

	"axiom/internal/platform/pg"
)

// mfaAttemptLimit is the site's budget of assertions for one pending session.
func (s *Service) mfaAttemptLimit(p Principal) int32 {
	return int32(s.snapshot.Load().sites[p.site].limits.challengeAttempts) //nolint:gosec // the setting is at most 100
}

// lockMFAAttempt locks the pending session in the caller's transaction and refuses it when its budget is spent. A
// session that is no longer pending is a ceremony failure; a pending session out of attempts needs a new sign-in, not
// a wait, so it is not a rate limit.
func (s *Service) lockMFAAttempt(ctx context.Context, tx pg.Tx, p Principal) (PendingSessionRow, error) {
	pending, _, err := s.lockPendingID(ctx, tx, uuid.UUID(p.user), uuid.UUID(p.session))
	if errors.Is(err, ErrNoSession) {
		return PendingSessionRow{}, ErrPasskeyFailed
	} else if err != nil {
		return PendingSessionRow{}, err
	}
	if pending.MfaAttempts >= s.mfaAttemptLimit(p) {
		return PendingSessionRow{}, ErrMFAExhausted
	}
	return pending, nil
}

// consumeMFAAttempt checks the session's budget, checks and consumes its live flow, reserves the attempt, and reads the
// user in one transaction. A request that loses the race sees the attempt already spent before it can consume a flow.
func (s *Service) consumeMFAAttempt(ctx context.Context, site Site, p Principal, flowID string) (FindWebAuthnFlowRow, passkeyUser, error) {
	var flow FindWebAuthnFlowRow
	var user passkeyUser
	err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		pending, err := s.lockMFAAttempt(ctx, tx, p)
		if err != nil {
			return err
		}
		if flow, err = s.findWebAuthnFlow(ctx, tx, site, flowID, "mfa", p); err != nil {
			return err
		}
		spent, err := s.queries.ReserveMFAAttempt(ctx, tx, ReserveMFAAttemptParams{ID: pending.ID, Attempts: s.mfaAttemptLimit(p)})
		if err != nil {
			return err
		}
		if spent != 1 {
			return ErrMFAExhausted
		}
		consumed, err := s.queries.ConsumeWebAuthnFlow(ctx, tx, flow.ID)
		if err != nil {
			return err
		}
		if consumed != 1 {
			return ErrPasskeyFailed
		}
		user, err = s.passkeyUser(ctx, tx, uuid.UUID(p.user))
		return err
	})
	return flow, user, err
}
