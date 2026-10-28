package billing

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// advanceAllocation replays one page of the account's history in a transaction of its own and without the lock, and reports
// whether there may be more. A stale page is discarded and retried from the newer checkpoint, so a competing replay cannot leave a long tail
// for the subsequent locked step.
//
// It is safe without the lock because every transaction of an account takes its seq under its balance row lock (post),
// so any page it reads is a prefix of what the lock will see: nothing can commit later with a place before it.
// Fix that prefix before loading batches: a later credit does not move the allocation's place, but its batch would
// be missing from this replay (#361). The next replay loads it along with its batch. The order is what makes the bound
// work, so it must not be swapped: a bound read after the batches would take in a credit committed while they were
// read, whose batch is not among them, and the replay would fail on that credit instead.
func advanceAllocation(ctx context.Context, db *pg.DB, scope pg.Scope, account uuid.UUID, size int32) (more bool, err error) {
	err = db.Tx(ctx, scope, func(tx pg.Tx) error {
		through, err := queries.ReplayThrough(ctx, tx, account)
		if err != nil {
			return fmt.Errorf("billing: read the replay bound of %s: %w", account, err)
		}
		a, err := loadAllocation(ctx, tx, account)
		if err != nil {
			return err
		}
		if more, err = a.catchUpPage(ctx, tx, account, size, through); err != nil {
			return err
		}
		return a.save(ctx, tx, account)
	})
	if errors.Is(err, errStale) {
		return true, nil
	}
	return more, err
}
