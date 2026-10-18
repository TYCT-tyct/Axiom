package billing

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// Cleanup takes over what no Settle will end (README.md「持有者与截止时间」「预留的状态」), up to max reservations,
// each in a transaction of its own under its balance row lock, and returns how many it changed. A pending reservation
// past its deadline or under a reaped holder is released: nothing can bring its usage later, so nothing is charged,
// and the supplier's reconciliation keeps what the journal holds of it.
func (b *Billing) Cleanup(ctx context.Context, max int32) (changed int, err error) {
	var due []DueReservationsRow
	err = b.db.Tx(ctx, b.platform, func(tx pg.Tx) error {
		due, err = queries.DueReservations(ctx, tx, max)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("billing: find what cleanup owns: %w", err)
	}
	for _, d := range due {
		if err := b.db.Tx(ctx, b.platform, func(tx pg.Tx) error { return b.clean(ctx, tx, d.RequestID, d.AccountID) }); err != nil {
			return changed, fmt.Errorf("billing: clean up %s: %w", d.RequestID, err)
		}
		changed++
	}
	return changed, nil
}

func (b *Billing) clean(ctx context.Context, tx pg.Tx, id, accountID uuid.UUID) error {
	c, err := b.lock(ctx, tx, id, accountID)
	if err != nil {
		return err
	}
	if c.row.State == "pending" {
		return c.takeOver(ctx, tx)
	}
	return nil // a Settle got there first
}

// lock locks the account's balance row and reads the reservation under it.
func (b *Billing) lock(ctx context.Context, tx pg.Tx, id, accountID uuid.UUID) (*locked, error) {
	balance, err := lockAccount(ctx, tx, accountID)
	if err != nil {
		return nil, err
	}
	r, err := queries.LockReservation(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("billing: read the reservation: %w", err)
	}
	return &locked{b: b, id: id, row: r, balance: balance, charged: r.Charged}, nil
}

// takeOver ends a pending reservation no Settle will, charging nothing: the journal's committed attempt is recorded
// with its reconciliation key, so the supplier's charge matches it, and every other request is recorded as lost.
// Nothing can bring the call's usage afterwards, so the whole reservation goes back.
func (c *locked) takeOver(ctx context.Context, tx pg.Tx) error {
	j, committed, err := c.b.suppliers.CommittedAttempt(ctx, tx, c.id)
	if err != nil {
		return fmt.Errorf("billing: read the journal: %w", err)
	}
	if !committed || j.Key == "" {
		if err := c.b.suppliers.RecordLostRequest(ctx, tx, c.id); err != nil {
			return err
		}
		return c.release(ctx, tx, "cleanup_released", c.row.Amount)
	}
	records := []AttemptRecord{{Seq: j.Seq, Entry: j.Entry, Key: j.Key}}
	if err := c.b.suppliers.RecordAttempts(ctx, tx, c.id, records); err != nil {
		return err
	}
	c.entries = attemptEntries(records)
	return c.release(ctx, tx, "cleanup_released", c.row.Amount)
}
