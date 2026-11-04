package operations

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/pg"
)

// expiryBatch bounds one transaction of the sweep, so a backlog is worked off in short transactions.
const expiryBatch = 100

// expiryArgs is one run of the sweep of approvals nobody decided in time and of reservations nobody confirmed.
type expiryArgs struct{}

// Kind implements river.JobArgs.
func (expiryArgs) Kind() string { return "operations.approval_expiry" }

// InsertOpts implements river.JobArgsWithInsertOpts: one run at a time, on the housekeeping queue. A decision refuses
// an expired approval by itself, so the sweep only records the expiry and may wait behind the call log's retention.
func (expiryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueArchive, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}}}
}

// RegisterExpiry adds the sweep to queue, before it starts: every minute, on whichever node leads the queue, the
// approvals past their expiry are expired with their operations, recorded and their signals resolved, and the money
// reservations past the end of their windows are released. An approval is
// refused a decision from its expiry on whether or not the sweep has reached it, so the sweep's delay only defers the
// record. scope is the platform's, which only cmd/ builds.
func RegisterExpiry(db *pg.DB, scope pg.Scope, queue *pg.Queue, logger *slog.Logger) error {
	// A failed run is logged and not retried: the next one comes within the minute, and a retrying job would keep it
	// from being inserted.
	work := func(ctx context.Context, _ *river.Job[expiryArgs]) error {
		for {
			// Two transactions: the approvals' signals are the last lock either takes (resolve), and the reservations'
			// counters must not be taken after them.
			var approvals, reservations int
			err := db.Tx(ctx, scope, func(tx pg.Tx) (err error) {
				approvals, err = ExpireDue(ctx, tx, expiryBatch)
				return err
			})
			if err == nil {
				err = db.Tx(ctx, scope, func(tx pg.Tx) (err error) {
					reservations, err = ExpireReservations(ctx, tx, expiryBatch)
					return err
				})
			}
			if err != nil {
				logger.Error("operations: the expiry of approvals and reservations stopped", "err", err)
				return nil
			}
			if max(approvals, reservations) < expiryBatch {
				return nil
			}
		}
	}
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(work)); err != nil {
		return fmt.Errorf("operations: %w", err)
	}
	queue.Every(time.Minute, func() river.JobArgs { return expiryArgs{} })
	return nil
}
