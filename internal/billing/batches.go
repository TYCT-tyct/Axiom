package billing

import (
	"bytes"
	"cmp"
	"context"
	"slices"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// CreditBatchView is a credit batch as the console shows it: what a grant or payment added, what is left of it, how
// much of that is frozen for a refund, and when the rest stops counting.
type CreditBatchView struct {
	ID                uuid.UUID
	Source            string
	Amount, Remaining int64
	Frozen            int64
	CreatedAt         time.Time
	ExpiresAt         *time.Time
}

// ReadCreditBatches returns the batches that still hold something, oldest credit first, and whether the account's
// allocation was brought up to date within pages of the ledger. The allocation is a replay cache that expiry and
// refunds already advance; catching it up here is what makes a remaining figure exact instead of trailing the ledger.
// It is bounded so a page waits a predictable time: when the history is longer than pages allow, ready is false and
// the caller shows this part as unavailable rather than a stale number.
func ReadCreditBatches(ctx context.Context, db *pg.DB, scope pg.Scope, account uuid.UUID, pages int) ([]CreditBatchView, bool, error) {
	ready := false
	for range pages {
		more, err := advanceAllocation(ctx, db, scope, account, movementPage)
		if err != nil {
			return nil, false, err
		}
		if !more {
			ready = true
			break
		}
	}
	if !ready {
		return nil, false, nil
	}
	var batches []CreditBatchView
	err := db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		a, err := loadAllocation(ctx, tx, account)
		if err != nil {
			return err
		}
		batches = make([]CreditBatchView, 0, len(a.open))
		for _, b := range a.open {
			batches = append(batches, CreditBatchView{
				ID: b.id, Source: b.source, Amount: b.amount, Remaining: b.remaining(), Frozen: b.frozen(),
				CreatedAt: b.opened.at, ExpiresAt: b.expiresAt,
			})
		}
		// a.open is in spending order; the page lists credits as they were added, oldest first.
		slices.SortFunc(batches, func(x, y CreditBatchView) int {
			return cmp.Or(x.CreatedAt.Compare(y.CreatedAt), bytes.Compare(x.ID[:], y.ID[:]))
		})
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return batches, true, nil
}
