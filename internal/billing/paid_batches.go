package billing

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
)

// PaidBatch is one paid credit batch: the record behind the dashboard's paid users and monthly revenue. The amount is
// the credit the batch granted in the site's accounting currency; the time is the batch's created_at, the same axis
// PlatformPaid counts.
type PaidBatch struct {
	ID, Account    uuid.UUID
	Amount         string
	CreatedAt      time.Time
	Online         bool
	OfflineChannel string
}

// PaidBatchQuery is one page of a site's paid batches in [since, until), newest first.
type PaidBatchQuery struct {
	Site         uuid.UUID
	Since, Until time.Time
	After        page.Cursor
	Limit        int
}

// ListPaidBatches reads the paid batches behind the dashboard's commercial figures. It starts after the page cursor,
// which is the (created_at, id) of the last batch of the previous page.
func ListPaidBatches(ctx context.Context, tx pg.Tx, q PaidBatchQuery) ([]PaidBatch, *page.Cursor, error) {
	rows, err := queries.ListPaidBatches(ctx, tx, ListPaidBatchesParams{
		SiteID: q.Site, Since: q.Since, Until: q.Until, AfterAt: q.After.At, AfterID: q.After.ID,
		Max: int32(q.Limit + 1), // #nosec G115 -- the admin contract bounds the page size at 200
	})
	if err != nil {
		return nil, nil, fmt.Errorf("billing: read paid batches: %w", err)
	}
	out := make([]PaidBatch, 0, len(rows))
	for _, r := range rows {
		out = append(out, PaidBatch{
			ID: r.ID, Account: r.AccountID, Amount: r.Amount, CreatedAt: r.CreatedAt,
			Online: r.Online, OfflineChannel: r.OfflineChannel,
		})
	}
	var next *page.Cursor
	if len(out) > q.Limit {
		out = out[:q.Limit]
		last := out[len(out)-1]
		next = &page.Cursor{At: last.CreatedAt, ID: last.ID}
	}
	return out, next, nil
}
