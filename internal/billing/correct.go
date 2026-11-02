package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// correctionEvery is how often the queue's leader reads the suppliers' charge events. Reconciliation holds a charge
// for 30 minutes before it can be matched, so an event is usually hours before a call's deadline; the job still runs
// often enough that a correction lands well inside the hour the design promises.
const correctionEvery = time.Minute

// correctionPage bounds one read of the events or of the retries: the cursor advances one page at a time, so a
// backlog never holds the money queue's worker in one transaction.
const correctionPage = 100

// correctionRetry is how long an event whose settlement is not final waits before it is read again; it grows with the
// attempts but stays below the hour a correction may take after the event.
const (
	correctionRetryMin = time.Minute
	correctionRetryMax = 30 * time.Minute
)

// errNotReady is a charge event whose reservation is not settled yet. The event is kept in billing.charge_corrections
// and read again, rather than dropping a correction the account is owed.
var errNotReady = errors.New("billing: the reservation is not settled yet")

// errNoCorrection is an event that will never correct a charge: it belongs to a different attempt, a test, a call that
// was not wholly estimated, or a supplier's usage that does not price completely. The cursor moves on.
var errNoCorrection = errors.New("billing: the charge event does not correct this settlement")

// CorrectConfig is what the correction job needs. It reads suppliers' events in the platform scope and writes each
// account's ledger in its own transaction, so the queue's leader is the only writer of the cursor.
type CorrectConfig struct {
	DB     *pg.DB
	Scope  pg.Scope
	Queue  *pg.Queue
	Logger *slog.Logger
	Meter  metric.Meter
}

// RegisterCorrections adds the charge correction to the money queue, before it starts.
func RegisterCorrections(cfg CorrectConfig) error {
	meter := cfg.Meter
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("billing")
	}
	c := &corrector{
		db: cfg.DB, scope: cfg.Scope, logger: cfg.Logger,
		corrected: counter(meter, "axiom.billing.charge_correction.corrected", "Estimated charges corrected from a supplier's record."),
		retried:   counter(meter, "axiom.billing.charge_correction.retried", "Charge events waiting for their reservation to settle."),
		borne:     counter(meter, "axiom.billing.charge_correction.borne", "Corrections whose added charge the account's floor kept it from paying in full."),
		borneAmt:  counter(meter, "axiom.billing.charge_correction.borne.amount", "What the platform bore of those corrections, in billionths of a credit."),
	}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(c.work)); err != nil {
		return fmt.Errorf("billing: %w", err)
	}
	cfg.Queue.Every(correctionEvery, func() river.JobArgs { return correctionArgs{} })
	return nil
}

type correctionArgs struct{ singleRun }

// Kind implements river.JobArgs.
func (correctionArgs) Kind() string { return "billing.charge_corrections" }

type corrector struct {
	db                 *pg.DB
	scope              pg.Scope
	logger             *slog.Logger
	corrected, retried metric.Int64Counter
	borne, borneAmt    metric.Int64Counter
}

func (c *corrector) work(ctx context.Context, _ *river.Job[correctionArgs]) error {
	if _, err := c.retryDue(ctx); err != nil {
		return err
	}
	for range 10 {
		more, err := c.readNew(ctx)
		if err != nil || !more {
			return err
		}
	}
	return nil
}

// retryDue corrects the events whose settlement was not ready when they were read.
func (c *corrector) retryDue(ctx context.Context) (int, error) {
	var due []DueCorrectionsRow
	if err := c.db.Tx(ctx, c.scope, func(tx pg.Tx) error {
		var err error
		due, err = queries.DueCorrections(ctx, tx, correctionPage)
		return err
	}); err != nil {
		return 0, fmt.Errorf("billing: read due charge corrections: %w", err)
	}
	for _, r := range due {
		if err := c.retryOne(ctx, r); err != nil {
			return 0, err
		}
	}
	return len(due), nil
}

// retryOne reads one event again and corrects it, or forgets an event whose record is gone.
func (c *corrector) retryOne(ctx context.Context, r DueCorrectionsRow) error {
	ev, err := c.event(ctx, r.EventID)
	if errors.Is(err, pg.ErrNoRows) {
		return c.forget(ctx, r.EventID)
	}
	if err != nil {
		return err
	}
	retry, err := c.one(ctx, ev)
	if err != nil && !errors.Is(err, errNotReady) && !errors.Is(err, errNoCorrection) {
		return err
	}
	wait, reason := time.Duration(0), ""
	if retry {
		reason = err.Error()
		c.retried.Add(ctx, 1)
		wait = backoff(r.Attempts)
	} else if err == nil {
		c.corrected.Add(ctx, 1)
	}
	return c.record(ctx, ev, retry, wait, reason, false)
}

// readNew reads one page of events and corrects them, moving the cursor one event at a time so that a page that
// fails half way is not read again from its start.
func (c *corrector) readNew(ctx context.Context) (bool, error) {
	var cursor CorrectionCursorRow
	var events []suppliers.ChargeEvent
	if err := c.db.Tx(ctx, c.scope, func(tx pg.Tx) error {
		var err error
		if err := queries.LockChargeEvents(ctx, tx); err != nil {
			return fmt.Errorf("lock charge events: %w", err)
		}
		if cursor, err = queries.CorrectionCursor(ctx, tx); err != nil {
			return err
		}
		events, err = suppliers.ChargeEvents(ctx, tx, cursor.AfterAt, cursor.AfterID, correctionPage)
		return err
	}); err != nil {
		return false, fmt.Errorf("billing: read charge events: %w", err)
	}
	for _, ev := range events {
		retry, err := c.one(ctx, ev)
		if err != nil && !errors.Is(err, errNotReady) && !errors.Is(err, errNoCorrection) {
			return false, err
		}
		wait, reason := time.Duration(0), ""
		if retry {
			c.retried.Add(ctx, 1)
			wait, reason = correctionRetryMin, err.Error()
		} else if err == nil {
			c.corrected.Add(ctx, 1)
		}
		if err := c.record(ctx, ev, retry, wait, reason, true); err != nil {
			return false, err
		}
	}
	return len(events) == correctionPage, nil
}

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
		if sub.ClosedAt != nil && delta != 0 {
			voided, err := queries.WindowVoided(ctx, tx, WindowVoidedParams{SubscriptionID: row.SubscriptionID, Seq: row.WindowSeq})
			if err != nil {
				return nil, nil, 0, 0, fmt.Errorf("billing: read whether the refunded window was voided: %w", err)
			}
			if voided {
				// A refund, not expiry, closed this window: it already reversed the call's revenue and left no expired
				// income. The correction still updates the reservation and window counters, but books no money.
				return nil, nil, delta, 0, nil
			}
			// The subscription was written off, so its liability is zero: the difference comes back out of the expired
			// income that write-off booked, and the pair leaves the liability at zero and never negative.
			settle = append(settle, line{bookCreditExpired, delta, siteCredits}, line{bookPlanLiability, -delta, siteCredits})
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

// record stores the event's outcome and, for a page, moves the cursor past it in the same transaction. An event that
// needs a retry keeps its row and is read again from there; any other outcome deletes it.
func (c *corrector) record(ctx context.Context, ev suppliers.ChargeEvent, retry bool, wait time.Duration, reason string, advance bool) error {
	return c.db.Tx(ctx, c.scope, func(tx pg.Tx) error {
		if retry {
			if err := queries.UpsertCorrection(ctx, tx, UpsertCorrectionParams{
				EventID: ev.ID, Revision: ev.Revision, RequestID: ev.Request,
				NextAt: time.Now().Add(wait), LastError: truncate(reason, 500),
			}); err != nil {
				return fmt.Errorf("billing: keep charge event %s for retry: %w", ev.ID, err)
			}
		} else if err := queries.DeleteCorrection(ctx, tx, ev.ID); err != nil {
			return fmt.Errorf("billing: forget charge event %s: %w", ev.ID, err)
		}
		if advance {
			if _, err := queries.AdvanceCorrectionCursor(ctx, tx, AdvanceCorrectionCursorParams{AfterAt: ev.At, AfterID: ev.ID}); err != nil {
				return fmt.Errorf("billing: advance the charge correction cursor: %w", err)
			}
		}
		return nil
	})
}

func (c *corrector) forget(ctx context.Context, id uuid.UUID) error {
	return c.db.Tx(ctx, c.scope, func(tx pg.Tx) error {
		return queries.DeleteCorrection(ctx, tx, id)
	})
}

func (c *corrector) event(ctx context.Context, id uuid.UUID) (suppliers.ChargeEvent, error) {
	var ev suppliers.ChargeEvent
	err := c.db.Tx(ctx, c.scope, func(tx pg.Tx) error {
		var err error
		ev, err = suppliers.ChargeEventByID(ctx, tx, id)
		return err
	})
	return ev, err
}

func backoff(attempts int32) time.Duration {
	wait := correctionRetryMin
	for range min(attempts, 10) {
		wait *= 2
	}
	return min(wait, correctionRetryMax)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1] // never split a rune: PostgreSQL text must be valid UTF-8
	}
	return s
}
