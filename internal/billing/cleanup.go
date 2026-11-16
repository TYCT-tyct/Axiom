package billing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"uuid"

	"axiom/internal/platform/pg"
)

// Cleanup takes over what no Settle will end (README.md「持有者与截止时间」「预留的状态」), trying at most max
// reservations from the start of the due set, and returns how many it changed. See cleanFrom for the rules.
func (b *Billing) Cleanup(ctx context.Context, max int32) (changed int, err error) {
	changed, _, _, err = b.cleanFrom(ctx, uuid.UUID{}, max)
	return changed, err
}

// reportedFailures caps the failures one cleanFrom call keeps in its error; the rest are only counted.
const reportedFailures = 5

// cleanFrom tries at most budget due reservations in request order, those after the request id after, each in a
// transaction of its own under its balance row lock, and returns how many it changed. A pending reservation past its
// deadline or under a reaped holder is released: nothing can bring its usage later, so nothing is charged, and the
// request is left with suppliers as a lost one, with its route. One that fails does not stop the rest: it counts as an
// attempt, and its error is joined into the one returned (the first reportedFailures, then a count).
//
// The budget bounds the work of a call, failures included. next is where the following call must start: a cursor that
// moves forward past the failures, so a backlog of reservations that always fail, which stay first in line, cannot hold
// back the ones behind them; done says the scan reached the end of the due set, and next is then the start again. A
// reservation at rank r among the due ones past the cursor is reached within ceil(r/budget) calls. A call cut short by
// ctx returns its error, done false and the count so far.
func (b *Billing) cleanFrom(ctx context.Context, after uuid.UUID, budget int32) (changed int, next uuid.UUID, done bool, err error) {
	var report failureReport
	next = after
	for left := budget; left > 0; {
		if ctx.Err() != nil {
			report.add(fmt.Errorf("billing: cleanup stopped: %w", context.Cause(ctx)))
			break
		}
		var due []DueReservationsRow
		if err := b.db.Tx(ctx, b.platform, func(tx pg.Tx) (err error) {
			due, err = queries.DueReservations(ctx, tx, DueReservationsParams{After: next, Max: left})
			return err
		}); err != nil {
			report.add(fmt.Errorf("billing: find what cleanup owns: %w", err))
			break
		}
		n, last := b.cleanPage(ctx, due, &report)
		changed += n
		if last != (uuid.UUID{}) {
			next = last
		}
		if len(due) < int(left) && ctx.Err() == nil { // a short page held the end of the due set
			return changed, uuid.UUID{}, true, report.err()
		}
		left -= int32(min(len(due), int(left))) //nolint:gosec // at most left, which is an int32
	}
	return changed, next, false, report.err()
}

// cleanPage takes over the reservations of a page until ctx ends, and returns how many it changed and the last request
// it tried (the zero id when none).
func (b *Billing) cleanPage(ctx context.Context, due []DueReservationsRow, report *failureReport) (changed int, last uuid.UUID) {
	for _, d := range due {
		if ctx.Err() != nil {
			break // cleanFrom reports it
		}
		last = d.RequestID
		if err := b.cleanOne(ctx, d); err != nil {
			report.add(fmt.Errorf("billing: clean up %s: %w", d.RequestID, err))
		} else {
			changed++
		}
	}
	return changed, last
}

// failureReport keeps the first reportedFailures errors and counts the rest.
type failureReport struct {
	kept   []error
	others int
}

func (r *failureReport) add(err error) {
	if len(r.kept) < reportedFailures {
		r.kept = append(r.kept, err)
	} else {
		r.others++
	}
}

func (r *failureReport) err() error {
	if r.others > 0 {
		return errors.Join(append(r.kept, fmt.Errorf("billing: %d more failures not shown", r.others))...)
	}
	return errors.Join(r.kept...)
}

// cleanOne takes over one due reservation in a transaction of its own, and logs what it released.
func (b *Billing) cleanOne(ctx context.Context, d DueReservationsRow) error {
	var released int64
	err := b.db.Tx(ctx, b.platform, func(tx pg.Tx) (err error) {
		released, err = b.clean(ctx, tx, d.RequestID, d.AccountID)
		return err
	})
	if err == nil && released > 0 {
		b.logger.Warn("billing: cleanup released a reservation no settlement came for; any usage of the call is the platform's",
			"request", d.RequestID.String(), "released", released)
	}
	return err
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
	balance, err := queries.LockBalance(ctx, tx, accountID)
	if err != nil {
		return nil, balanceErr(err)
	}
	// Read in a fresh statement after waiting for the balance lock.
	r, err := queries.LockReservation(ctx, tx, id)
	if err != nil {
		return nil, err
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
