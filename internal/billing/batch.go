package billing

import (
	"context"
	"uuid"

	"axiom/internal/platform/pg"
)

// A reservation is read in a new statement after locking its balance, including on cleanup's path.
func settlementRows(ctx context.Context, tx pg.Tx, id, account uuid.UUID) (balance LockBalanceRow, r LockReservationRow, err error) {
	balance, err = queries.LockBalance(ctx, tx, account)
	if err != nil {
		return balance, r, balanceErr(err)
	}
	r, err = queries.LockReservation(ctx, tx, id)
	if err != nil {
		return balance, r, err
	}
	return balance, r, nil
}

// SettleIn settles h in tx, a transaction of the account's scope that may hold other settlements (internal/gateway's
// completion batch). It returns the reports to run after that transaction commits; the caller must run them only then,
// because they describe what the ledger now holds. A database error leaves the transaction to its caller, which rolls
// the whole batch back.
func (b *Billing) SettleIn(ctx context.Context, tx pg.Tx, h *Hold, o Outcome) (f Final, after func(), err error) {
	c, err := b.lock(ctx, tx, h.id, h.admitted.Account)
	if err != nil {
		return Final{}, nil, err
	}
	c.quotes = h.quotes
	f, err = c.settle(ctx, tx, o)
	if err != nil {
		return Final{}, nil, err
	}
	return f, func() {
		b.alert(c.seen)
		b.bear(ctx, c.settlement, c.owed, c.charged)
	}, nil
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
