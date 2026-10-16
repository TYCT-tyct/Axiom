package billing

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
)

// Cleanup takes over what no Settle will end (README.md「持有者与截止时间」「预留的状态」), up to max reservations,
// each in a transaction of its own under its balance row lock, and returns how many it changed. A pending
// reservation past its deadline or under a reaped holder becomes cleanup_pending when the journal shows a committed
// attempt with a reconciliation key, keeping its reservation for the supplier record, or else cleanup_released, a lost
// request; a held state past its correction window releases what it holds.
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
	balance, err := lockAccount(ctx, tx, accountID)
	if err != nil {
		return err
	}
	r, err := queries.LockReservation(ctx, tx, id)
	if err != nil {
		return err
	}
	c := locked{id: id, row: r, balance: balance, charged: r.Charged}
	switch {
	case r.State == "pending":
		return b.takeOver(ctx, tx, &c)
	case r.State == "estimated" && r.CorrectionClosed: // the estimate stands; release what it held
		return c.release(ctx, tx, "settled", r.Amount-r.Charged)
	case r.State == "cleanup_pending" && r.CorrectionClosed: // no supplier record within the hour
		if err := b.suppliers.RecordLostRequest(ctx, tx, id); err != nil {
			return err
		}
		return c.release(ctx, tx, "cleanup_released", r.Amount)
	}
	return nil // a Settle got there first
}

// takeOver ends a pending reservation no Settle will: only an attempt the journal marks committed, with a key to
// match the supplier's record, is charged, later, at the record's usage; every other is a lost request.
func (b *Billing) takeOver(ctx context.Context, tx pg.Tx, c *locked) error {
	j, committed, err := b.suppliers.CommittedAttempt(ctx, tx, c.id)
	if err != nil {
		return fmt.Errorf("billing: read the journal: %w", err)
	}
	if !committed || j.Key == "" {
		if err := b.suppliers.RecordLostRequest(ctx, tx, c.id); err != nil {
			return err
		}
		return c.release(ctx, tx, "cleanup_released", c.row.Amount)
	}
	if err := b.suppliers.RecordAttempts(ctx, tx, c.id, []AttemptRecord{{Seq: j.Seq, Entry: j.Entry, Key: j.Key}}); err != nil {
		return err
	}
	return c.write(ctx, tx, "cleanup_pending", index(j.Entry), 0, b.config.CorrectionWindow.Seconds())
}

// release ends a reservation in state, giving back what it held, and keeps what was charged.
func (c *locked) release(ctx context.Context, tx pg.Tx, state string, held int64) error {
	if err := c.move(ctx, tx, -max(held, 0), 0); err != nil {
		return err
	}
	return c.write(ctx, tx, state, nil, c.row.Price, 0)
}

// Correction is a supplier record that fixes an attempt's usage and cost (ChargeSettled): Revision orders the record's
// versions, Usage is by sampling, all reported, and Cost is the attempt's cost in billionths of a credit.
type Correction struct {
	Request  uuid.UUID
	Seq      int
	Revision int64
	Usage    []catalog.Usage
	Cost     int64
}

// errStale is a correction applied already, or older than one applied: it succeeds and changes nothing.
var errStale = errors.New("billing: a stale correction")

// Correct applies c once (README.md「预留的状态」「校正」): only a revision newer than the last applied one changes
// anything. The attempt's cost always follows the record; the user's charge only within the correction window: an
// estimate is replaced, the makeup capped by what the reservation still holds, and a cleaned-up call is charged at
// the record's usage and locked price.
func (b *Billing) Correct(ctx context.Context, c Correction) error {
	err := b.db.Tx(ctx, b.platform, func(tx pg.Tx) error {
		accountID, err := queries.ReservationAccount(ctx, tx, c.Request)
		if err != nil {
			return err
		}
		balance, err := lockAccount(ctx, tx, accountID)
		if err != nil {
			return err
		}
		r, err := queries.LockReservation(ctx, tx, c.Request)
		if err != nil {
			return err
		}
		if n, err := queries.ApplyRevision(ctx, tx, ApplyRevisionParams{RequestID: c.Request, Revision: c.Revision}); err != nil || n == 0 {
			return errors.Join(err, errStale)
		}
		a := locked{id: c.Request, row: r, balance: balance, charged: r.Charged}
		if err := a.adjustCost(ctx, tx, c); err != nil || r.CorrectionClosed {
			return err
		}
		return a.recharge(ctx, tx, c)
	})
	if errors.Is(err, errStale) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("billing: correct %s: %w", c.Request, err)
	}
	return nil
}

func (c *locked) adjustCost(ctx context.Context, tx pg.Tx, k Correction) error {
	seq := int32(k.Seq) //nolint:gosec // G115: at most a few attempts
	recorded, err := queries.AttemptCost(ctx, tx, AttemptCostParams{RequestID: c.id, AttemptSeq: seq})
	if err != nil {
		return err
	}
	e := c.event("cost_adjust", k.Revision)
	e.seq = seq
	_, err = post(ctx, tx, e, line{bookSupplierCost, k.Cost - recorded}, line{bookSupplierPrepaid, recorded - k.Cost})
	return err
}

// recharge replaces an estimated or cleaned-up charge with the record's: the reservation's hold pays the makeup, which
// therefore never passes the floor; past the hold the platform bears it. An estimate keeps holding the rest until its
// window ends, for a later revision; a cleaned-up call releases it.
func (c *locked) recharge(ctx context.Context, tx pg.Tx, k Correction) error {
	if c.row.State != "estimated" && c.row.State != "cleanup_pending" {
		return nil
	}
	if c.row.Entry == nil {
		return fmt.Errorf("billing: %s has no charged entry", c.id)
	}
	s, err := schedule(c.row.Prices, int(*c.row.Entry))
	if err != nil {
		return err
	}
	segments := make([]Segment, len(k.Usage))
	for i, u := range k.Usage {
		segments[i] = Segment{Usage: u, Reported: ^catalog.ItemSet(0)}
	}
	price, _, err := priceSegments(s, segments, false)
	if err != nil {
		return err
	}
	held := max(c.row.Amount-c.row.Charged, 0)
	c.charged = min(price, c.row.Charged+held)
	state, keep := "cleanup_settled", int64(0)
	if c.row.State == "estimated" {
		state, keep = "estimated", max(c.row.Amount-c.charged, 0)
	}
	if _, err := post(ctx, tx, c.event("reverse", k.Revision), reversal(c.row.Price, c.row.Charged)...); err != nil {
		return err
	}
	if _, err := post(ctx, tx, c.event("settle", k.Revision), charge(price, c.charged)...); err != nil {
		return err
	}
	if err := c.move(ctx, tx, keep-held, c.charged-c.row.Charged); err != nil {
		return err
	}
	return c.write(ctx, tx, state, nil, price, 0)
}
