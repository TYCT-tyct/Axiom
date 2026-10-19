package billing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"uuid"

	"axiom/internal/platform/pg"
)

// Cleanup takes over what no Settle will end (README.md「持有者与截止时间」「预留的状态」), up to max reservations,
// each in a transaction of its own under its balance row lock, and returns how many it changed. A pending reservation
// past its deadline or under a reaped holder is released: nothing can bring its usage later, so nothing is charged,
// and the request is left with suppliers as a lost one, with its route. One that fails does not stop the rest: its error
// is joined into the one returned, and the reservations after it are still taken over, since the failing one is first in
// line again on every sweep and would otherwise hold them all back.
func (b *Billing) Cleanup(ctx context.Context, max int32) (changed int, err error) {
	var due []DueReservationsRow
	err = b.db.Tx(ctx, b.platform, func(tx pg.Tx) error {
		due, err = queries.DueReservations(ctx, tx, max)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("billing: find what cleanup owns: %w", err)
	}
	var failed []error
	for _, d := range due {
		var released int64
		err := b.db.Tx(ctx, b.platform, func(tx pg.Tx) (err error) {
			released, err = b.clean(ctx, tx, d.RequestID, d.AccountID)
			return err
		})
		if err != nil {
			failed = append(failed, fmt.Errorf("billing: clean up %s: %w", d.RequestID, err))
			if ctx.Err() != nil {
				break // the rest would fail for the same reason
			}
			continue
		}
		changed++
		if released > 0 {
			b.logger.Warn("billing: cleanup released a reservation no settlement came for; any usage of the call is the platform's",
				"request", d.RequestID.String(), "released", released)
		}
	}
	return changed, errors.Join(failed...)
}

// clean takes over one reservation, and returns how much it released, 0 when a Settle got there first.
func (b *Billing) clean(ctx context.Context, tx pg.Tx, id, accountID uuid.UUID) (released int64, err error) {
	c, err := b.lock(ctx, tx, id, accountID)
	if err != nil {
		return 0, err
	}
	if c.row.State != "pending" {
		return 0, nil // a Settle got there first
	}
	return c.row.Amount, c.takeOver(ctx, tx)
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

// takeOver ends a pending reservation no Settle will: the request is recorded as lost, with the route it was frozen
// with, so that a charge the supplier reports later can be matched to it. Nothing can bring the call's usage
// afterwards, so the whole reservation goes back and nothing is charged.
func (c *locked) takeOver(ctx context.Context, tx pg.Tx) error {
	// The reservation marks an entry that was dropped at admission with the zero id; no charge can come from it.
	route := slices.DeleteFunc(slices.Clone(c.row.Endpoints), func(id uuid.UUID) bool { return id == uuid.UUID{} })
	if err := c.b.suppliers.RecordLostRequest(ctx, tx, c.id, c.row.SiteID, c.row.AccountID, route); err != nil {
		return err
	}
	return c.release(ctx, tx, "cleanup_released", c.row.Amount)
}
