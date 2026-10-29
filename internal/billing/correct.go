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
		ev, err := c.event(ctx, r.EventID)
		if errors.Is(err, pg.ErrNoRows) {
			if err := c.forget(ctx, r.EventID); err != nil {
				return 0, err
			}
			continue
		}
		if err != nil {
			return 0, err
		}
		retry, err := c.one(ctx, ev)
		if err != nil && !errors.Is(err, errNotReady) && !errors.Is(err, errNoCorrection) {
			return 0, err
		}
		wait := correctionRetryMin
		reason := ""
		if retry {
			reason = err.Error()
			c.retried.Add(ctx, 1)
			wait = backoff(r.Attempts)
		} else {
			reason = ""
			if err == nil {
				c.corrected.Add(ctx, 1)
			}
		}
		if err := c.record(ctx, ev, retry, wait, reason, false); err != nil {
			return 0, err
		}
	}
	return len(due), nil
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
		account, err := queries.ReservationAccount(ctx, tx, ev.Request)
		if errors.Is(err, pg.ErrNoRows) {
			return errNotReady
		}
		if err != nil {
			return fmt.Errorf("billing: read the account of %s: %w", ev.Request, err)
		}
		balance, err := lockAccount(ctx, tx, account)
		if err != nil {
			return err
		}
		row, err := queries.LockReservation(ctx, tx, ev.Request)
		if err != nil {
			return fmt.Errorf("billing: lock the reservation of %s: %w", ev.Request, err)
		}
		segments, price, err := c.correctedPrice(row, ev)
		if err != nil {
			return err
		}
		charged := &locked{id: ev.Request, row: row, balance: balance}
		charged.owed = price.price
		charged.charged = min(price.price, add(row.Charged, room(balance, 0)))
		borne = max(price.price-charged.charged, 0)
		revision := int64(ev.Revision)
		reversed, err := post(ctx, tx, charged.event("reverse", revision), reversal(row.Price, row.Charged)...)
		if err != nil {
			return err
		}
		settled, err := post(ctx, tx, charged.event("settle", revision), charge(price.price, charged.charged)...)
		if err != nil {
			return err
		}
		if !reversed && !settled {
			return errNoCorrection
		}
		usage, err := encodeUsage(segments)
		if err != nil {
			return err
		}
		if _, err := queries.EndReservation(ctx, tx, EndReservationParams{
			Profit: correctedProfit(row, charged.charged), Reserved: 0, Spent: charged.charged - row.Charged,
			AccountID: row.AccountID, WorkspaceID: row.WorkspaceID, WorkspaceWindow: row.WorkspaceWindow,
			KeyID: row.KeyID, KeyWindow: row.KeyWindow, RequestID: ev.Request, State: "settled",
			Charged: charged.charged, Price: price.price, Waived: row.Waived, Estimated: false,
			FinalUsage: usage, PricingTiers: price.tiers,
		}); err != nil {
			return fmt.Errorf("billing: write the corrected settlement of %s: %w", ev.Request, err)
		}
		// The correction moved the balance, so the account's allocation must move with it in the same transaction: a
		// later expiry or freeze replays from the saved place and would otherwise never see this refund, especially
		// after the charged batch was fully spent (#277).
		a, err := loadAllocation(ctx, tx, row.AccountID)
		if errors.Is(err, errStale) {
			return fmt.Errorf("%w: the allocation moved while correcting %s", errNotReady, ev.Request)
		}
		if err != nil {
			return err
		}
		if err := a.catchUp(ctx, tx, row.AccountID, movementPage); err != nil {
			return fmt.Errorf("%w: replay the allocation of %s: %v", errNotReady, ev.Request, err)
		}
		if want := balance.Balance - (charged.charged - row.Charged); a.funded() != want {
			return fmt.Errorf("%w: the correction of %s funds %d, balance %d", errNotReady, ev.Request, a.funded(), want)
		}
		if err := a.save(ctx, tx, row.AccountID); errors.Is(err, errStale) {
			return fmt.Errorf("%w: the allocation moved while correcting %s", errNotReady, ev.Request)
		} else if err != nil {
			return err
		}
		if borne > 0 {
			if err := alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
				Kind: "billing.charge_correction_borne", Key: ev.Request.String(), Severity: "critical",
				Fields: map[string]string{"request": ev.Request.String(), "event": ev.ID.String(), "borne": fmt.Sprint(borne)},
			}); err != nil {
				return fmt.Errorf("billing: report the borne correction: %w", err)
			}
		}
		return nil
	})
	return borne, err
}

// correctedPrice prices the supplier's usage at the schedule the request locked. It refuses anything but a settlement
// that was wholly estimated, for the attempt that was billed, with a supplier's record that declares every charged item: a partly reported
// charge is already actual for its reported items and mixing the two would charge twice.
func (c *corrector) correctedPrice(row LockReservationRow, ev suppliers.ChargeEvent) ([]Segment, priced, error) {
	if row.State == "pending" {
		return nil, priced{}, errNotReady
	}
	if row.State != "settled" && row.State != "cleanup_settled" {
		return nil, priced{}, errNoCorrection
	}
	if !row.Estimated || row.Entry == nil || row.ChargedSeq == nil || row.PricingTiers < 0 {
		return nil, priced{}, errNoCorrection
	}
	if ev.Revision < 1 {
		return nil, priced{}, errNoCorrection
	}
	if ev.AttemptSeq != platform.AttemptSeq(*row.ChargedSeq) {
		return nil, priced{}, errNoCorrection
	}
	old, err := decodeUsage(row.FinalUsage)
	if err != nil {
		return nil, priced{}, fmt.Errorf("billing: read the estimated usage of %s: %w", ev.Request, err)
	}
	charged := chargedItems(row.Waived)
	for _, seg := range old {
		if len(seg.Usage.Unknown()) > 0 {
			// An item without a name cannot be matched to a supplier declaration; replacing it would refund a charge
			// the supplier has not accounted for.
			return nil, priced{}, errNoCorrection
		}
		for it := range catalog.NumItems {
			if !charged.Has(it) || seg.Usage.Count(it) == 0 {
				continue
			}
			// An item the upstream already reported is actual; the supplier's count for it would charge it twice.
			if seg.Reported.Has(it) {
				return nil, priced{}, errNoCorrection
			}
			// Every item we charged but estimated must be declared by the supplier; a record that only mentions
			// some items is not a complete count, and pricing the rest as zero would silently refund them.
			if !ev.Reported.Has(it) {
				return nil, priced{}, errNoCorrection
			}
		}
	}
	segments := []Segment{{Usage: ev.Usage, Reported: ev.Reported}}
	s, f, err := (&locked{row: row}).pricing(int(*row.Entry))
	if errors.Is(err, ErrNoAttempt) {
		return nil, priced{}, errNoCorrection
	}
	if err != nil {
		return nil, priced{}, fmt.Errorf("billing: read the locked price of %s: %w", ev.Request, err)
	}
	price, err := priceSegments(s, f, segments, row.Waived, catalog.ServiceTierSet(row.PricingTiers)) //nolint:gosec // stored CHECK bounds tiers
	if err != nil {
		return nil, priced{}, err
	}
	if price.estimated {
		return nil, priced{}, errNoCorrection
	}
	price.tiers = row.PricingTiers
	return segments, price, nil
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
