package accounts

import (
	"context"
	"time"
)

// WellFormedKey and NewKeyText expose the key format to its tests.
var (
	WellFormedKey = wellFormedKey
	NewKeyText    = newKeyText
)

// Reload reloads the snapshot now, as the reload loop does every 30 seconds.
func (s *Service) Reload(ctx context.Context) error { return s.reload(ctx) }

// TOTPCode is the code an authenticator app shows for the base32 secret at the time.
func TOTPCode(secretText string, at time.Time) string {
	key, err := totpBase32.DecodeString(secretText)
	if err != nil {
		panic(err)
	}
	return totpCode(key, at.Unix()/totpPeriod)
}
