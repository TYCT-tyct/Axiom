package observability

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/pg"
)

type usageArgs struct{}

func (usageArgs) Kind() string { return "observability.usage" }
func (usageArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueArchive, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending,
	}}}
}

// RegisterUsage registers the incremental rollup and backfill before the River queue starts.
func RegisterUsage(db *pg.DB, scope pg.Scope, q *pg.Queue) error {
	w := &usageWorker{db: db, scope: scope}
	if err := river.AddWorkerSafely(q.Workers(), river.WorkFunc(w.work)); err != nil {
		return err
	}
	q.Every(time.Minute, func() river.JobArgs { return usageArgs{} })
	return nil
}

type usageWorker struct {
	db    *pg.DB
	scope pg.Scope
}

const usageBatchSize = 1000

func (w *usageWorker) work(ctx context.Context, _ *river.Job[usageArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Each committed batch survives a later failure. Both time and batch count bound a turn on the archive queue.
	for range 100 {
		var more bool
		err := w.db.Tx(ctx, w.scope, func(tx pg.Tx) (err error) {
			more, err = rollupUsageBatch(ctx, tx, usageBatchSize)
			return err
		})
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	return nil
}

func rollupUsageBatch(ctx context.Context, tx pg.Tx, size int32) (bool, error) {
	var done bool
	var at time.Time
	var id string
	// One lock order across nodes, including after backfill: serialize checkpoint advancement and rollup writes.
	err := tx.QueryRow(ctx, `SELECT done,at,id::text FROM observability.usage_backfill WHERE singleton FOR UPDATE SKIP LOCKED`).Scan(&done, &at, &id)
	if errors.Is(err, pg.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	consumeSize := size
	if !done {
		var backfill store.BackfillUsageRow
		backfill, err = store.New().BackfillUsage(ctx, tx, store.BackfillUsageParams{AfterAt: at, AfterID: id, BatchSize: size})
		if err != nil {
			return false, err
		}
		done = backfill.Done
		// Backfill must not consume the capacity reserved for live arrivals.
		consumeSize += backfill.Enqueued
	}
	n, err := store.New().ConsumeUsageJournal(ctx, tx, consumeSize)
	return !done || n == consumeSize, err
}
