package billing

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// The three statements must remain separate: the counters' snapshot starts after the balance lock is granted,
// even when another node held it. Sending them together saves round trips without moving that snapshot earlier.
func (b *Billing) reserveSources(ctx context.Context, tx pg.Tx, a Admitted) (balance LockBalanceRow, usage ReadUsageRow, err error) {
	err = tx.Batch(ctx, []pg.Statement{
		{SQL: lockBalance, Args: []any{a.Account}},
		{SQL: fenceHolder, Args: []any{b.holder}},
		{SQL: readUsage, Args: []any{a.Workspace, string(reset(a.WorkspaceBudget)), a.Key, string(reset(a.KeyLimit)), (*time.Time)(nil)}},
	}, func(batch pg.BatchResults) error {
		if err = batch.QueryRow().Scan(&balance.Balance, &balance.Floor, &balance.Reserved, &balance.Frozen); errors.Is(err, pg.ErrNoRows) {
			return errNoBalance
		} else if err != nil {
			return fmt.Errorf("billing: lock the balance: %w", err)
		}
		var holder uuid.UUID
		if err = batch.QueryRow().Scan(&holder); errors.Is(err, pg.ErrNoRows) {
			return ErrTerminated
		} else if err != nil {
			return fmt.Errorf("billing: fence the holder: %w", err)
		}
		if err = batch.QueryRow().Scan(&usage.Day, &usage.WorkspaceUsed, &usage.KeyUsed); err != nil {
			return fmt.Errorf("billing: read the counters: %w", err)
		}
		return nil
	})
	return balance, usage, err
}

// A reservation is read in a new statement after locking its balance, including on cleanup's path.
func settlementRows(ctx context.Context, tx pg.Tx, id, account uuid.UUID) (balance LockBalanceRow, r LockReservationRow, err error) {
	err = tx.Batch(ctx, []pg.Statement{
		{SQL: lockBalance, Args: []any{account}},
		{SQL: lockReservation, Args: []any{id}},
	}, func(batch pg.BatchResults) error {
		if err = batch.QueryRow().Scan(&balance.Balance, &balance.Floor, &balance.Reserved, &balance.Frozen); errors.Is(err, pg.ErrNoRows) {
			return errNoBalance
		} else if err != nil {
			return fmt.Errorf("billing: lock the balance: %w", err)
		}
		err = batch.QueryRow().Scan(
			&r.SiteID,
			&r.AccountID,
			&r.WorkspaceID,
			&r.KeyID,
			&r.WorkspaceWindow,
			&r.KeyWindow,
			&r.Amount,
			&r.State,
			&r.Prices,
			&r.ChargedSeq,
			&r.Entry,
			&r.Charged,
			&r.Price,
			&r.Waived,
			&r.Estimated,
			&r.FinalUsage,
			&r.Fallbacks,
			&r.Models,
			&r.AttemptEntries,
			&r.Endpoints,
			&r.Profit,
		)
		if err != nil {
			err = fmt.Errorf("billing: read the reservation: %w", err)
		}
		return err
	})
	return balance, r, err
}
