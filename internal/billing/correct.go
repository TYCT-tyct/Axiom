package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// one corrects one event in the account's transaction. It reports whether the event must be retried; errNoCorrection
// means the event cannot ever correct this settlement and is done.
func (c *corrector) one(ctx context.Context, ev suppliers.ChargeEvent) (bool, error) {
	borne, err := c.correct(ctx, ev)
	if errors.Is(err, errNotReady) {
		return true, err
	}
	if errors.Is(err, errNoCorrection) {
		return false, errNoCorrection
	}
	if err != nil {
		return false, err
	}
	if borne > 0 {
		c.borne.Add(ctx, 1)
		c.borneAmt.Add(ctx, borne)
		if c.logger != nil {
			c.logger.Error("billing: the platform bears part of a corrected charge; the account could not pay it in full",
				"request", ev.Request.String(), "event", ev.ID.String(), "borne", borne)
		}
	}
	return false, err
}

func (c *corrector) correct(ctx context.Context, ev suppliers.ChargeEvent) (int64, error) {
	if ev.Test {
		// A test generation is the platform's own cost and has no tenant reservation to correct. Filter it before
		// looking for the reservation, or a probe event would wait forever in the retry table.
		return 0, errNoCorrection
	}
	var borne int64
	err := c.db.Tx(ctx, c.scope, func(tx pg.Tx) error {
		var err error
		borne, err = replaceCharge(ctx, tx, ev)
		return err
	})
	return borne, err
}

// replaceCharge reverses the estimated settlement and settles the supplier's usage in the account's transaction. It
// returns what the account's floor kept it from paying.
func replaceCharge(ctx context.Context, tx pg.Tx, ev suppliers.ChargeEvent) (int64, error) {
	row, balance, err := lockCorrection(ctx, tx, ev.Request)
	if err != nil {
		return 0, err
	}
	segments, price, err := correctedPrice(row, ev)
	if err != nil {
		return 0, err
	}
	charged := &locked{id: ev.Request, row: row, balance: balance}
	charged.owed = price.price
	correction, err := chargeCorrection(ctx, tx, row, balance, charged, price, int64(ev.Revision))
	if err != nil {
		return 0, err
	}
	if !correction.reversed && !correction.settled {
		return 0, errNoCorrection
	}
	if err := correctPlanWindow(ctx, tx, ev.Request, row, correction.windowDelta); err != nil {
		return 0, err
	}
	if err := writeCorrection(ctx, tx, charged, segments, price.tiers); err != nil {
		return 0, err
	}
	if correction.walletDelta != 0 { // a plan charge moves no wallet money, so the allocation has nothing to realign
		if err := realignAllocation(ctx, tx, ev.Request, row, balance, charged.charged); err != nil {
			return 0, err
		}
	}
	if correction.borne > 0 {
		if err := alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
			Kind: "billing.charge_correction_borne", Key: ev.Request.String(), Severity: "critical",
			Fields: map[string]string{"request": ev.Request.String(), "event": ev.ID.String(), "borne": fmt.Sprint(correction.borne)},
		}); err != nil {
			return 0, fmt.Errorf("billing: report the borne correction: %w", err)
		}
	}
	return correction.borne, nil
}

// correctionPost is what chargeCorrection wrote: whether each event landed, and the counter and allocation moves that
// follow from the source that actually paid.
type correctionPost struct {
	reversed, settled bool
	windowDelta       int64 // the frozen window's used amount changes by this much
	walletDelta       int64 // the user's balance changes by this much
	borne             int64
}

// chargeCorrection reverses the old charge and posts the corrected one, choosing the source the effective settlement
// actually used. A true plan charge is capped by its frozen window and moves no wallet money; a reservation whose late
// settlement paid from the wallet (funding was rewritten then) is corrected against the wallet and its allocation.
func chargeCorrection(ctx context.Context, tx pg.Tx, row LockReservationRow, balance LockBalanceRow, charged *locked, price priced, revision int64) (correctionPost, error) {
	reverse, settle, windowDelta, walletDelta, err := correctionLines(ctx, tx, row, balance, charged, price)
	if err != nil {
		return correctionPost{}, err
	}
	reversed, err := post(ctx, tx, charged.event("reverse", revision), reverse...)
	if err != nil {
		return correctionPost{}, err
	}
	settled, err := post(ctx, tx, charged.event("settle", revision), settle...)
	if err != nil {
		return correctionPost{}, err
	}
	return correctionPost{
		reversed: reversed, settled: settled, windowDelta: windowDelta, walletDelta: walletDelta,
		borne: max(price.price-charged.charged, 0),
	}, nil
}

// correctionLines prices the corrected charge against the source the reservation records and returns the two ledger
// entries and the counters they move.
func correctionLines(ctx context.Context, tx pg.Tx, row LockReservationRow, balance LockBalanceRow, charged *locked, price priced) ([]line, []line, int64, int64, error) {
	if row.SubscriptionID != (uuid.UUID{}) {
		// A plan charge is capped by the frozen window, whose room is read afresh: a later request may have taken what
		// this one released, and the window's quota bounds every charge into it (docs/domains/billing.md「订阅套餐」).
		left, sub, err := planRoom(ctx, tx, row.SubscriptionID, row.WindowSeq)
		if err != nil {
			return nil, nil, 0, 0, err
		}
		charged.charged = min(price.price, min(row.Amount, add(row.Charged, left)))
		delta := charged.charged - row.Charged
		reverse, settle := planReversal(row.Price, row.Charged), planCharge(price.price, charged.charged)
		if delta != 0 {
			voided, err := queries.WindowVoided(ctx, tx, WindowVoidedParams{SubscriptionID: row.SubscriptionID, Seq: row.WindowSeq})
			if err != nil {
				return nil, nil, 0, 0, fmt.Errorf("billing: read whether the refunded window was voided: %w", err)
			}
			if voided {
				// A refund, not expiry, voided this window: it already reversed the call's revenue and left no expired
				// income. The correction still updates the reservation and window counters, but books no money.
				return nil, nil, delta, 0, nil
			}
			if sub.ClosedAt != nil {
				// The subscription was written off, so its liability is zero: the difference comes back out of the expired
				// income that write-off booked, and the pair leaves the liability at zero and never negative.
				settle = append(settle, line{bookCreditExpired, delta, siteCredits}, line{bookPlanLiability, -delta, siteCredits})
			}
		}
		return reverse, settle, delta, 0, nil
	}
	charged.charged = min(price.price, add(row.Charged, room(balance, 0)))
	return reversal(row.Price, row.Charged), charge(price.price, charged.charged), 0, charged.charged - row.Charged, nil
}

// lockCorrection reads the request's account, then locks its balance and reservation, the order every settlement uses.
func lockCorrection(ctx context.Context, tx pg.Tx, request uuid.UUID) (LockReservationRow, LockBalanceRow, error) {
	account, err := queries.ReservationAccount(ctx, tx, request)
	if errors.Is(err, pg.ErrNoRows) {
		return LockReservationRow{}, LockBalanceRow{}, errNotReady
	}
	if err != nil {
		return LockReservationRow{}, LockBalanceRow{}, fmt.Errorf("billing: read the account of %s: %w", request, err)
	}
	balance, err := lockAccount(ctx, tx, account)
	if err != nil {
		return LockReservationRow{}, LockBalanceRow{}, err
	}
	row, err := queries.LockReservation(ctx, tx, request)
	if err != nil {
		return LockReservationRow{}, LockBalanceRow{}, fmt.Errorf("billing: lock the reservation of %s: %w", request, err)
	}
	return row, balance, nil
}

// writeCorrection records the corrected settlement on the reservation: the supplier's usage replaces the estimate.
func writeCorrection(ctx context.Context, tx pg.Tx, charged *locked, segments []Segment, tiers int16) error {
	usage, err := encodeUsage(segments)
	if err != nil {
		return err
	}
	row := charged.row
	if _, err := queries.EndReservation(ctx, tx, EndReservationParams{
		Profit: correctedProfit(row, charged.charged), BalanceReserved: 0, SpendReserved: 0, Spent: charged.charged - row.Charged,
		AccountID: row.AccountID, WorkspaceID: row.WorkspaceID, WorkspaceWindow: row.WorkspaceWindow,
		KeyID: row.KeyID, KeyWindow: row.KeyWindow, RequestID: charged.id, State: "settled",
		Charged: charged.charged, Price: charged.owed, Waived: row.Waived, Estimated: false,
		FinalUsage: usage, PricingTiers: tiers,
	}); err != nil {
		return fmt.Errorf("billing: write the corrected settlement of %s: %w", charged.id, err)
	}
	return nil
}

// realignAllocation moves the account's allocation with the balance the correction changed in the same transaction: a
// later expiry or freeze replays from the saved place and would otherwise never see this refund, especially after the
// charged batch was fully spent (#277).
func realignAllocation(ctx context.Context, tx pg.Tx, request uuid.UUID, row LockReservationRow, balance LockBalanceRow, charged int64) error {
	a, err := loadAllocation(ctx, tx, row.AccountID)
	if errors.Is(err, errStale) {
		return fmt.Errorf("%w: the allocation moved while correcting %s", errNotReady, request)
	}
	if err != nil {
		return err
	}
	if err := a.catchUp(ctx, tx, row.AccountID, movementPage); err != nil {
		return fmt.Errorf("%w: replay the allocation of %s: %w", errNotReady, request, err)
	}
	if want := balance.Balance - (charged - row.Charged); a.funded() != want {
		return fmt.Errorf("%w: the correction of %s funds %d, balance %d", errNotReady, request, a.funded(), want)
	}
	if err := a.save(ctx, tx, row.AccountID); errors.Is(err, errStale) {
		return fmt.Errorf("%w: the allocation moved while correcting %s", errNotReady, request)
	} else if err != nil {
		return err
	}
	return nil
}

// correctPlanWindow moves the corrected charge into the frozen window's used amount.
func correctPlanWindow(ctx context.Context, tx pg.Tx, request uuid.UUID, row LockReservationRow, delta int64) error {
	if delta == 0 {
		return nil
	}
	if n, err := queries.ChargeWindowUsed(ctx, tx, ChargeWindowUsedParams{
		SubscriptionID: row.SubscriptionID, Seq: row.WindowSeq, Delta: delta,
	}); err != nil {
		return fmt.Errorf("billing: move the window's usage: %w", err)
	} else if n != 1 {
		return fmt.Errorf("billing: the window of %s cannot take a correction of %d", request, delta)
	}
	return nil
}

// correctedPrice prices the supplier's usage at the schedule the request locked. It refuses anything but a settlement
// that was wholly estimated, for the attempt that was billed, with a supplier's record that declares every charged item: a partly reported
// charge is already actual for its reported items and mixing the two would charge twice.
func correctedPrice(row LockReservationRow, ev suppliers.ChargeEvent) ([]Segment, priced, error) {
	if err := correctionReady(row, ev); err != nil {
		return nil, priced{}, err
	}
	old, err := decodeUsage(row.FinalUsage)
	if err != nil {
		return nil, priced{}, fmt.Errorf("billing: read the estimated usage of %s: %w", ev.Request, err)
	}
	if err := whollyEstimated(old, chargedItems(row.Waived), ev.Reported); err != nil {
		return nil, priced{}, err
	}
	segments := []Segment{{Usage: ev.Usage, Reported: ev.Reported}}
	s, f, err := (&locked{row: row}).pricing(int(*row.Entry))
	if errors.Is(err, ErrNoAttempt) {
		return nil, priced{}, errNoCorrection
	}
	if err != nil {
		return nil, priced{}, fmt.Errorf("billing: read the locked price of %s: %w", ev.Request, err)
	}
	price, err := priceSegments(s, f, segments, row.Waived, catalog.ServiceTierSet(row.PricingTiers)) //nolint:gosec // G115: correctionReady excludes -1 and the column CHECKs at most 15
	if err != nil {
		return nil, priced{}, err
	}
	if price.estimated {
		return nil, priced{}, errNoCorrection
	}
	price.tiers = row.PricingTiers
	return segments, price, nil
}

// correctionReady reports whether the reservation can be corrected: its settlement is final and wholly estimated, for
// the attempt the supplier billed.
func correctionReady(row LockReservationRow, ev suppliers.ChargeEvent) error {
	if row.State == "pending" {
		return errNotReady
	}
	if row.State != "settled" && row.State != "cleanup_settled" {
		return errNoCorrection
	}
	if !row.Estimated || row.Entry == nil || row.ChargedSeq == nil || row.PricingTiers < 0 {
		return errNoCorrection
	}
	if ev.Revision < 1 || *row.ChargedSeq < 1 || *row.ChargedSeq > platform.MaxAttempts {
		return errNoCorrection
	}
	if ev.AttemptSeq != platform.AttemptSeq(*row.ChargedSeq) {
		return errNoCorrection
	}
	return nil
}

// whollyEstimated reports whether every charged item of the stored usage was estimated and is declared by the supplier:
// a partly reported charge is already actual for its items, and pricing a missing one as zero would refund it.
func whollyEstimated(old []Segment, charged, reported catalog.ItemSet) error {
	for _, seg := range old {
		if len(seg.Usage.Unknown()) > 0 {
			// An item without a name cannot be matched to a supplier declaration; replacing it would refund a charge
			// the supplier has not accounted for.
			return errNoCorrection
		}
		for it := range catalog.NumItems {
			if !charged.Has(it) || seg.Usage.Count(it) == 0 {
				continue
			}
			// An item the upstream already reported is actual; the supplier's count for it would charge it twice.
			if seg.Reported.Has(it) {
				return errNoCorrection
			}
			// Every item we charged but estimated must be declared by the supplier; a record that only mentions
			// some items is not a complete count, and pricing the rest as zero would silently refund them.
			if !reported.Has(it) {
				return errNoCorrection
			}
		}
	}
	return nil
}

// correctedProfit moves the recorded profit by what the charge changed. The supplier's cost did not change with the
// correction, so recomputing it from the ledger would mix a re-read cost with the charge of another moment; an unknown
// profit stays unknown.
func correctedProfit(row LockReservationRow, charged int64) *int64 {
	if row.Profit == nil {
		return nil
	}
	delta := sum(platform.AmountExact(charged), platform.AmountExact(row.Charged).Neg())
	profit := sum(platform.AmountExact(*row.Profit), delta)
	if profit.Cmp(platform.AmountExact(math.MinInt64)) < 0 || profit.Cmp(platform.AmountExact(math.MaxInt64)) > 0 {
		return nil
	}
	n := profit.CeilAmount()
	return &n
}
