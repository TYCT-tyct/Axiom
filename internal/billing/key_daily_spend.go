package billing

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// DailySpend is settled spend in billionths for one UTC day; reservations are excluded.
type DailySpend struct {
	Day   time.Time
	Spent int64
}

// KeyDailySpend reads a visible page's last fourteen UTC days in one bounded query.
// Missing days have zero spend and are omitted. Callers pass only keys they have authorized.
func KeyDailySpend(ctx context.Context, tx pg.Tx, keys []uuid.UUID) (map[uuid.UUID][]DailySpend, error) {
	rows, err := queries.ReadKeyDailySpend(ctx, tx, keys)
	if err != nil {
		return nil, fmt.Errorf("billing: read key daily spend: %w", err)
	}
	result := make(map[uuid.UUID][]DailySpend)
	for _, r := range rows {
		result[r.Subject] = append(result[r.Subject], DailySpend{r.WindowStart.UTC(), r.Spent})
	}
	return result, nil
}
