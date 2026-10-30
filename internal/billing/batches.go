package billing

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
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

// AdvanceCreditBatches brings the account's allocation replay cache up to date, at most pages of the ledger, and says
// whether it caught up. The cache is shared with expiry and refunds, and every page saves its place, so a long history
// is advanced a little by each call instead of replayed from the start.
func AdvanceCreditBatches(ctx context.Context, db *pg.DB, scope pg.Scope, account uuid.UUID, pages int) (bool, error) {
	for range pages {
		more, err := advanceAllocation(ctx, db, scope, account, movementPage)
		if err != nil {
			return false, err
		}
		if !more {
			return true, nil
		}
	}
	return false, nil
}

// ReadCreditBatches reads the batches that still hold something in tx, oldest credit first. include is false when the
// caller could not advance the replay far enough, and ready is false when a transaction committed after the last
// advance: the batches then trail the snapshot of the balance, and the caller must leave them out rather than show a
// number from another point in time. It must run in the same transaction as the balance it is shown with.
func ReadCreditBatches(ctx context.Context, tx pg.Tx, account uuid.UUID, include bool) ([]CreditBatchView, bool, error) {
	if !include {
		return nil, false, nil
	}
	through, err := queries.ReplayThrough(ctx, tx, account)
	if err != nil {
		return nil, false, fmt.Errorf("billing: read the replay bound of %s: %w", account, err)
	}
	a, err := loadAllocation(ctx, tx, account)
	if err != nil {
		return nil, false, err
	}
	// ReplayThrough fixes the bound of the snapshot: every transaction of the account at or below it has committed.
	// A movement after it is newer than the balance read in this transaction and stays out of both.
	if a.at.seq < through {
		return nil, false, nil
	}
	batches := make([]CreditBatchView, 0, len(a.open))
	for _, b := range a.open {
		// LiveBatches keeps consumed batches so a correction can give their credit back; a page lists only credit that
		// still holds something.
		if b.remaining() == 0 && b.frozen() == 0 {
			continue
		}
		batches = append(batches, CreditBatchView{
			ID: b.id, Source: b.source, Amount: b.amount, Remaining: b.remaining(), Frozen: b.frozen(),
			CreatedAt: b.opened.at, ExpiresAt: b.expiresAt,
		})
	}
	// a.open is in spending order; the page lists credits as they were added, oldest first.
	slices.SortFunc(batches, func(x, y CreditBatchView) int {
		return cmp.Or(x.CreatedAt.Compare(y.CreatedAt), bytes.Compare(x.ID[:], y.ID[:]))
	})
	return batches, true, nil
}
