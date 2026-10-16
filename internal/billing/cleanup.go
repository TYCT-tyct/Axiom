package billing

import (
	"context"
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
	c, err := b.lock(ctx, tx, id, accountID)
	if err != nil {
		return err
	}
	switch r := c.row; {
	case r.State == "pending":
		return c.takeOver(ctx, tx)
	case r.State == "estimated" && r.CorrectionClosed: // the estimate stands; release what it held
		return c.release(ctx, tx, "settled", r.Amount-r.Charged)
	case r.State == "cleanup_pending" && r.CorrectionClosed: // no supplier record within the hour
		if err := c.b.suppliers.RecordLostRequest(ctx, tx, id); err != nil {
			return err
		}
		return c.release(ctx, tx, "cleanup_released", r.Amount)
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

// takeOver ends a pending reservation no Settle will: only an attempt the journal marks committed, with a key to
// match the supplier's record, is charged, later, at the record's usage; every other is a lost request.
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
	if err := c.b.suppliers.RecordAttempts(ctx, tx, c.id, []AttemptRecord{{Seq: j.Seq, Entry: j.Entry, Key: j.Key}}); err != nil {
		return err
	}
	w := c.kept("cleanup_pending")
	w.seq, w.entry, w.estimated, w.holdSeconds = int32p(j.Seq), int32p(j.Entry), true, c.b.config.CorrectionWindow.Seconds()
	return c.write(ctx, tx, w)
}

// Correction is a supplier record that fixes an attempt's usage and cost (ChargeSettled): Revision orders the
// record's versions for its attempt, Usage is by sampling, all reported, and Cost is the attempt's cost in
// billionths of a credit.
type Correction struct {
	Request  uuid.UUID
	Seq      int
	Revision int64
	Usage    []catalog.Usage
	Cost     int64
}

// Correct applies c once (README.md「结算与账本」): only a revision newer than the last one applied to its attempt
// changes anything, in any state. The attempt's cost always follows the record. The user's charge follows it only for
// the charged attempt, within the correction window: an estimate is replaced, keeping its waiver, the makeup capped by
// what the reservation still holds; a cleaned-up call is charged at the record's usage and locked price. A database
// error fails it, for the caller to retry.
func (b *Billing) Correct(ctx context.Context, c Correction) error {
	err := b.db.Tx(ctx, b.platform, func(tx pg.Tx) error {
		accountID, err := queries.ReservationAccount(ctx, tx, c.Request)
		if err != nil {
			return err
		}
		l, err := b.lock(ctx, tx, c.Request, accountID)
		if err != nil {
			return err
		}
		n, err := queries.ApplyRevision(ctx, tx, ApplyRevisionParams{
			RequestID: c.Request, AttemptSeq: *int32p(c.Seq), SiteID: l.row.SiteID, AccountID: l.row.AccountID, Revision: c.Revision,
		})
		if err != nil || n == 0 { // n == 0: applied already, or older than the one applied
			return err
		}
		if err := l.adjustCost(ctx, tx, c); err != nil {
			return err
		}
		return l.recharge(ctx, tx, c)
	})
	if err != nil {
		return fmt.Errorf("billing: correct %s: %w", c.Request, err)
	}
	return nil
}

// adjustCost moves the attempt's cost to the record's: whatever was booked before, an estimate or an older record,
// the cost entries of the attempt then add up to k.Cost.
func (c *locked) adjustCost(ctx context.Context, tx pg.Tx, k Correction) error {
	seq := *int32p(k.Seq)
	recorded, err := queries.AttemptCost(ctx, tx, AttemptCostParams{RequestID: c.id, AttemptSeq: seq})
	if err != nil {
		return err
	}
	e := c.event("cost_adjust", k.Revision)
	e.seq = seq
	_, err = post(ctx, tx, e, line{bookSupplierCost, k.Cost - recorded}, line{bookSupplierPrepaid, recorded - k.Cost})
	return err
}

// recharges: a record changes the user's charge only for the charged attempt of a reservation still held, within
// its correction window.
func (c *locked) recharges(k Correction) bool {
	r := c.row
	held := r.State == "estimated" || r.State == "cleanup_pending"
	return held && !r.CorrectionClosed && r.ChargedSeq != nil && int(*r.ChargedSeq) == k.Seq && r.Entry != nil
}

// recharge replaces an estimated or cleaned-up charge with the record's: the reservation's hold pays the makeup, which
// therefore never passes the floor; past the hold the platform bears it. An estimate keeps holding the rest until its
// window ends, for a later revision; a cleaned-up call releases it. A record with usage no price covers leaves an
// estimate that holds the rest, with no window, until a record prices it, which opens the window then.
func (c *locked) recharge(ctx context.Context, tx pg.Tx, k Correction) error {
	r := c.row
	if !c.recharges(k) {
		return nil
	}
	p, usage, err := c.priceRecord(k)
	if err != nil {
		return err
	}
	hold := max(r.Amount-r.Charged, 0)
	c.charged = min(p.price, r.Charged+hold)
	if _, err := post(ctx, tx, c.event("reverse", k.Revision), reversal(r.Price, r.Charged)...); err != nil {
		return err
	}
	if _, err := post(ctx, tx, c.event("settle", k.Revision), charge(p.price, c.charged)...); err != nil {
		return err
	}
	w := c.kept("cleanup_settled")
	w.price, w.estimated, w.unpriced, w.usage = p.price, p.estimated, p.unpriced, usage
	keep := int64(0)
	if r.State == "estimated" || p.unpriced {
		w.state, keep = "estimated", max(r.Amount-c.charged, 0)
	}
	if r.Unpriced && !p.unpriced { // priced at last: the window it never had opens now
		w.holdSeconds = c.b.config.CorrectionWindow.Seconds()
	}
	if err := c.move(ctx, tx, keep-hold, c.charged-r.Charged); err != nil {
		return err
	}
	return c.write(ctx, tx, w)
}

// priceRecord prices the record's usage, all of it reported, at the charged entry's locked price, keeping the waiver,
// and encodes it as final_usage stores it.
func (c *locked) priceRecord(k Correction) (priced, []byte, error) {
	s, err := schedule(c.row.Prices, int(*c.row.Entry))
	if err != nil {
		return priced{}, nil, err
	}
	segments := make([]Segment, len(k.Usage))
	for i, u := range k.Usage {
		segments[i] = Segment{Usage: u, Reported: ^catalog.ItemSet(0)}
	}
	p, err := priceSegments(s, segments, c.row.Waived)
	if err != nil {
		return priced{}, nil, err
	}
	usage, err := encodeUsage(segments)
	return p, usage, err
}
