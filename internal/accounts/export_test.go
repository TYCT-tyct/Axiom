package accounts

import (
	"context"
	"time"
	"uuid"
)

// WellFormedKey and NewKeyText expose the key format to its tests.
var (
	WellFormedKey = wellFormedKey
	NewKeyText    = newKeyText
	TOTPStep      = totpStep
)

// Reload reloads the snapshot now, as the reload loop does every 30 seconds, within the same time limit.
func (s *Service) Reload(ctx context.Context) error { return s.reloadWithin(ctx) }

// TOTPCode is the code an authenticator app shows for the base32 secret at the time.
func TOTPCode(secretText string, at time.Time) string {
	key, err := totpBase32.DecodeString(secretText)
	if err != nil {
		panic(err)
	}
	return totpCode(key, at.Unix()/totpPeriod)
}

// HashTurn takes one of the node's turns at password hashing until release.
func (s *Service) HashTurn(ctx context.Context) (release func(), err error) {
	if err := s.guards.hashing.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	return func() { s.guards.hashing.Release(1) }, nil
}

// Hash runs compute in one of the node's turns at password hashing, as every argon2id computation does.
func (s *Service) Hash(ctx context.Context, compute func()) error { return s.hash(ctx, compute) }

// UsePasswordRecovery wires a test's queue without replacing the node's other dependencies.
func (s *Service) UsePasswordRecovery(r *PasswordRecovery) { s.recovery = r }

// ProcessPasswordReset runs one durable request under explicit test synchronization.
func (r *PasswordRecovery) ProcessPasswordReset(ctx context.Context, id uuid.UUID) error {
	return r.process(ctx, passwordResetArgs{ID: id})
}
