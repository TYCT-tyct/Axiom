package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

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
