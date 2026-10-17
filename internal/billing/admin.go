package billing

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// CreditBatch is a credit as the admin shows a grant: when it was made, how much, and when what is left of it ends.
type CreditBatch struct {
	ID, Site, Account uuid.UUID
	Amount            int64 // billionths of a credit
	ExpiresAt         *time.Time
	CreatedAt         time.Time
}

// ReadCredit is the batch AddCredit made with id, read in tx, which must hold the platform scope (or the account's);
// ok is false when there is none.
func ReadCredit(ctx context.Context, tx pg.Tx, id uuid.UUID) (batch CreditBatch, ok bool, err error) {
	rows, err := queries.ReadCreditBatch(ctx, tx, id)
	if err != nil {
		return CreditBatch{}, false, fmt.Errorf("billing: read the credit batch: %w", err)
	}
	if len(rows) == 0 {
		return CreditBatch{}, false, nil
	}
	r := rows[0]
	return CreditBatch{ID: r.ID, Site: r.SiteID, Account: r.AccountID, Amount: r.Amount, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt}, true, nil
}
