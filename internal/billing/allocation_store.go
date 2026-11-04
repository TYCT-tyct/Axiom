package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"uuid"

	"axiom/internal/platform/pg"
)

const movementPage = 5000

// loadAllocation reads the stored state of an account: where the replay stood, then the batches, then the place again,
// and returns errStale when it moved. The batches' replay state is written only by save, in the transaction
// that moves the place, and the place only moves forward; so a place that did not move across the batches' read means
// they are the ones stored with it. A replay that started from batches newer than its place would miss a batch another
// replay spent up in between, and fail on its credit, or on an expiry that no longer fits, before save's check of the
// place could drop it (#357). A caller that holds the account's balance row lock reads the latest state, and no other
// transaction can move it.
func loadAllocation(ctx context.Context, tx pg.Tx, account uuid.UUID) (*allocation, error) {
	if err := planLedgerPage(ctx, tx); err != nil {
		return nil, err
	}
	state, err := queries.AllocationState(ctx, tx, account)
	if err != nil {
		return nil, fmt.Errorf("billing: read the allocation of %s: %w", account, err)
	}
	loaded, err := queries.LiveBatches(ctx, tx, account)
	if err != nil {
		return nil, fmt.Errorf("billing: read the batches of %s: %w", account, err)
	}
	again, err := queries.AllocationState(ctx, tx, account)
	if err != nil {
		return nil, fmt.Errorf("billing: read the allocation of %s: %w", account, err)
	}
	if placeOf(again).compare(placeOf(state)) != 0 {
		return nil, errStale
	}
	return newAllocation(state, loaded), nil
}

// catchUpPage replays the next page of at most size transactions, no later than through, and reports whether
// the page was full, so that there may be more. The transactions from before the sequence come first; the page that
// ends them goes on into the numbered ones. A page is bounded by the index, not by the history.
func (a *allocation) catchUpPage(ctx context.Context, tx pg.Tx, account uuid.UUID, size int32, through int64) (full bool, err error) {
	// Read one row beyond the page so a page never ends on a reverse whose settle is the next row: the two must be
	// netted at the settle's place, and a page boundary between them would refund first and spend again, possibly
	// from a batch that expires at another time (docs/domains/billing.md「额度批次」).
	fetch := size + 1
	if a.at.seq == 0 {
		rows, err := queries.LegacyMovements(ctx, tx, LegacyMovementsParams{AccountID: account, AfterAt: a.at.at, AfterID: a.at.id, Max: fetch})
		if err != nil {
			return false, fmt.Errorf("billing: read the movements of %s: %w", account, err)
		}
		moves := legacyMovementsOf(rows)
		if len(rows) == int(fetch) {
			return true, a.applyPage(account, moves[:size], true)
		}
		if err := a.applyPage(account, moves, false); err != nil {
			return false, err
		}
	}
	rows, err := queries.Movements(ctx, tx, MovementsParams{AccountID: account, AfterSeq: a.at.seq, ThroughSeq: through, Max: fetch})
	if err != nil {
		return false, fmt.Errorf("billing: read the movements of %s: %w", account, err)
	}
	moves := movementsOf(rows)
	if len(rows) == int(fetch) {
		return true, a.applyPage(account, moves[:size], true)
	}
	return false, a.applyPage(account, moves, false)
}

// applyPage replays a page, holding back a trailing reverse when another row follows. The next page starts with it and
// its settle, so applyAll can net them.
func (a *allocation) applyPage(account uuid.UUID, moves []movement, more bool) error {
	if more && len(moves) > 1 {
		last := moves[len(moves)-1]
		if last.kind == "reverse" && last.revision != 0 {
			moves = moves[:len(moves)-1]
		}
	}
	return a.applyAll(account, moves)
}

func legacyMovementsOf(rows []LegacyMovementsRow) []movement {
	moves := make([]movement, len(rows))
	for i, r := range rows {
		moves[i] = movement{key: orderKey{seq: r.Seq, at: r.CreatedAt, id: r.ID}, kind: r.Kind, request: r.RequestID, debit: r.Debit, revision: r.Revision}
	}
	return moves
}

func movementsOf(rows []MovementsRow) []movement {
	moves := make([]movement, len(rows))
	for i, r := range rows {
		moves[i] = movement{key: orderKey{seq: r.Seq, at: r.CreatedAt, id: r.ID}, kind: r.Kind, request: r.RequestID, debit: r.Debit, revision: r.Revision}
	}
	return moves
}

// catchUp replays every transaction posted since the last one replayed.
func (a *allocation) catchUp(ctx context.Context, tx pg.Tx, account uuid.UUID, size int32) error {
	for {
		full, err := a.catchUpPage(ctx, tx, account, size, math.MaxInt64)
		if err != nil || !full {
			return err
		}
	}
}

// errStale is a replay dropped because another transaction moved the stored allocation while it ran.
var errStale = errors.New("billing: the allocation moved while it was replayed")

// save stores what the replay changed, if the stored allocation still stands where the replay began.
func (a *allocation) save(ctx context.Context, tx pg.Tx, account uuid.UUID) error {
	if !a.changed {
		return nil
	}
	n, err := queries.SaveAllocation(ctx, tx, SaveAllocationParams{
		AccountID: account, AllocatedSeq: a.at.seq, AllocatedAt: a.at.at, AllocatedID: a.at.id, Unfunded: a.unfunded,
		WasSeq: a.was.seq, WasAt: a.was.at, WasID: a.was.id,
	})
	if err != nil {
		return fmt.Errorf("billing: save the allocation of %s: %w", account, err)
	}
	if n == 0 {
		return errStale
	}
	var batches SaveBatchesParams
	for _, b := range a.batches {
		if b.touched {
			batches.Ids, batches.Consumed, batches.Expired = append(batches.Ids, b.id), append(batches.Consumed, b.consumed), append(batches.Expired, b.expired)
			batches.FreezeTotal = append(batches.FreezeTotal, b.freezeTotal)
			batches.UnfreezeTotal = append(batches.UnfreezeTotal, b.unfreezeTotal)
			batches.Refunded = append(batches.Refunded, b.refunded)
			batches.ChargedBack = append(batches.ChargedBack, b.chargedBack)
		}
	}
	if len(batches.Ids) == 0 {
		return nil
	}
	if err := queries.SaveBatches(ctx, tx, batches); err != nil {
		return fmt.Errorf("billing: save the batches of %s: %w", account, err)
	}
	return nil
}

// replayAccount takes the money lock and checks both ledger-derived totals before a freeze or expiry can move money.
func replayAccount(ctx context.Context, tx pg.Tx, account uuid.UUID, size int32) (LockBalanceRow, *allocation, error) {
	balance, err := lockAccount(ctx, tx, account)
	if err != nil {
		return balance, nil, err
	}
	a, err := loadAllocation(ctx, tx, account)
	if err != nil {
		return balance, nil, err
	}
	if err := a.catchUp(ctx, tx, account, size); err != nil {
		return balance, nil, err
	}
	if a.funded() != balance.Balance || a.frozen() != balance.Frozen {
		return balance, nil, fmt.Errorf("billing: allocation of %s funds %d frozen %d, balance %d frozen %d", account,
			a.funded(), a.frozen(), balance.Balance, balance.Frozen)
	}
	return balance, a, nil
}
