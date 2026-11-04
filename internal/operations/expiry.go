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

// expiryArgs is one run of the sweep of approvals nobody decided in time.
type expiryArgs struct{}

// Kind implements river.JobArgs.
func (expiryArgs) Kind() string { return "operations.approval_expiry" }

// InsertOpts implements river.JobArgsWithInsertOpts: one run at a time.
func (expiryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}}}
}

// RegisterExpiry adds the sweep to queue, before it starts: every minute, on whichever node leads the queue, the
// approvals past their expiry are expired with their operations, recorded and their signals resolved. An approval is
// refused a decision from its expiry on whether or not the sweep has reached it, so the sweep's delay only defers the
// record. scope is the platform's, which only cmd/ builds.
func RegisterExpiry(db *pg.DB, scope pg.Scope, queue *pg.Queue, logger *slog.Logger) error {
	// A failed run is logged and not retried: the next one comes within the minute, and a retrying job would keep it
	// from being inserted.
	work := func(ctx context.Context, _ *river.Job[expiryArgs]) error {
		for {
			var n int
			if err := db.Tx(ctx, scope, func(tx pg.Tx) (err error) {
				n, err = ExpireDue(ctx, tx, expiryBatch)
				return err
			}); err != nil {
				logger.Error("operations: the expiry of approvals stopped", "err", err)
				return nil
			}
			if n < expiryBatch {
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
