package billing

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// The three statements must remain separate: the counters' snapshot starts after the balance lock is granted,
// even when another node held it. Sending them together saves round trips without moving that snapshot earlier.
func (b *Billing) reserveSources(ctx context.Context, tx pg.Tx, a Admitted) (balance LockBalanceRow, usage ReadUsageRow, err error) {
	p := a.usageParams(nil)
	err = tx.Batch(ctx, []pg.Statement{
		{SQL: lockBalance, Args: []any{a.Account}},
		{SQL: fenceHolder, Args: []any{b.holder}},
		{SQL: readUsage, Args: []any{p.WorkspaceID, p.WorkspaceReset, p.KeyID, p.KeyReset, p.Day}},
	}, func(batch pg.BatchResults) error {
		if err = balanceErr(batch.QueryRow().Scan(&balance.Balance, &balance.Floor, &balance.Reserved, &balance.Frozen)); err != nil {
			return err
		}
		var holder uuid.UUID
		if err = fenceErr(batch.QueryRow().Scan(&holder)); err != nil {
			return err
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
		if err = balanceErr(batch.QueryRow().Scan(&balance.Balance, &balance.Floor, &balance.Reserved, &balance.Frozen)); err != nil {
			return err
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
			&r.PriceVersions,
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
			&r.PricingTiers,
		)
		if err != nil {
			err = fmt.Errorf("billing: read the reservation: %w", err)
		}
		return err
	})
	return balance, r, err
}

// BatchWorkError names the work of a RunBatch that failed. The transaction rolled back, so no work's writes
// committed; the gateway gives the error only to that work's request and retries the others in a new transaction.
// An error without this type came from the transaction itself (begin, commit or the connection), so it applies to
// every work.
type BatchWorkError struct {
	Index int
	Err   error
}

func (e *BatchWorkError) Error() string { return e.Err.Error() }
func (e *BatchWorkError) Unwrap() error { return e.Err }

// RunBatch runs works, in order, in one transaction of scope, and commits after the last one. It is the gateway's
// completion batch: a settlement or an extension writes its statements on the transaction it is given, and the batch
// pays one begin and one commit for all of them. One work's error rolls the whole transaction back, so no work's writes
// commit; it comes back as a BatchWorkError with the work's index, so the caller can retry the other works and give the
// error only to the work that failed. The reservation and ledger rows make a retry charge at most once.
func (b *Billing) RunBatch(ctx context.Context, scope pg.Scope, works []func(pg.Tx) error) error {
	return b.db.Tx(ctx, scope, func(tx pg.Tx) error {
		for i, work := range works {
			if err := work(tx); err != nil {
				return &BatchWorkError{Index: i, Err: err}
			}
		}
		return nil
	})
}
