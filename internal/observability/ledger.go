package observability

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"uuid"

	"axiom/internal/platform/pg"
)

// Ledger is the Amounts of the named calls whose settlement committed, keyed by request ID; a call it has no settled
// charge for is absent. An error is a read that failed: the batch is retried.
type Ledger func(ctx context.Context, tx pg.Tx, ids []uuid.UUID) (map[uuid.UUID]Amounts, error)

// settlePending gives the batch's pending records the amounts the ledger holds for them. It runs only when the batch
// has one, which is rare: the settlement was left to the background and then lost its record. The batch keeps what it
// is given, so a retry after a failed insert does not read the ledger again; the ledger's settled charge only moves by
// a correction, and the call log keeps one snapshot of it either way.
func settlePending(ctx context.Context, tx pg.Tx, ledger Ledger, batch []queued) error {
	if ledger == nil {
		return nil
	}
	pending, err := pendingRows(batch)
	if err != nil || len(pending) == 0 {
		return err
	}
	settled, err := ledger(ctx, tx, slices.Collect(maps.Keys(pending)))
	if err != nil {
		return fmt.Errorf("read the ledger for %d pending call records: %w", len(pending), err)
	}
	for id, a := range settled {
		for _, call := range pending[id] {
			if err := call.settle(a); err != nil {
				return fmt.Errorf("%w: the ledger's amounts of %s: %w", errEncoding, call.ID, err)
			}
		}
	}
	return nil
}

// pendingRows is the batch's pending records by request ID, nil when it has none. One ID can hold several: a start that
// died after moving a parked record into the spill and before removing its file moves it again on the next start, and
// the first copy inserted is the one the call log keeps.
func pendingRows(batch []queued) (map[uuid.UUID][]*row, error) {
	var pending map[uuid.UUID][]*row
	for i := range batch {
		call := &batch[i].call
		if !call.BillingPending {
			continue
		}
		id, err := uuid.Parse(call.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: request id %q: %w", errEncoding, call.ID, err)
		}
		if pending == nil {
			pending = map[uuid.UUID][]*row{}
		}
		pending[id] = append(pending[id], call)
	}
	return pending, nil
}
