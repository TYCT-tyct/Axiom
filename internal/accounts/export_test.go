package accounts

import "context"

// WellFormedKey and NewKeyText expose the key format to its tests.
var (
	WellFormedKey = wellFormedKey
	NewKeyText    = newKeyText
)

// Reload reloads the snapshot now, as the reload loop does every 30 seconds.
func (s *Service) Reload(ctx context.Context) error { return s.reload(ctx) }
