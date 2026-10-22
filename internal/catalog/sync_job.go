package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/platform/pg"
)

// syncEvery is how often a round reads both datasets (docs/domains/catalog.md「参考价怎么进入目录」): once a day, since
// they change slowly. A node that starts, or a new leader of the queue, runs a round at once.
const syncEvery = 24 * time.Hour

// syncAlertAfter is how long the prices may go unrefreshed before every failed round reports it: two intervals, so one
// bad download is left to the next round. This code default is a fallback; the ops node applies backend thresholds to
// the persisted state gauges, independently of whether a round runs.
const syncAlertAfter = 2 * syncEvery

// Bookkeeping must survive a cancelled download without extending shutdown indefinitely.
const syncRecordTimeout = 5 * time.Second

// syncOverdueEvent is the stable name of the alert log, and syncOverdueMetric the counter that rises with it once per
// round, so an alert rule watches a rise and not a total.
const (
	syncOverdueEvent  = "catalog.sync.overdue"
	syncOverdueMetric = "axiom.catalog.sync.overdue"
)

// SyncJobConfig is what the scheduled price sync needs: the database and the platform scope that only cmd/ builds, the
// queue it runs on, the fetcher of the datasets, where its log goes, and the meter of its counter (nil for none). Clock
// is nil for the database clock, and AlertAfter zero for the fallback code default syncAlertAfter.
type SyncJobConfig struct {
	DB         *pg.DB
	Scope      pg.Scope
	Queue      *pg.Queue
	Fetcher    *Fetcher
	Logger     *slog.Logger
	Meter      metric.Meter
	Clock      func() time.Time
	AlertAfter time.Duration
}

// RegisterSync adds the daily dataset sync to the queue, before it starts. It cannot take a *Catalog: the queue starts
// before the call path opens its own, and a round needs only the database and the fetcher. Only the leader schedules a
// round; any worker may run one. ctx is the node's lifetime: unlike money jobs, a price download can stop as soon as the
// node shuts down, without waiting for the queue's drain deadline.
func RegisterSync(ctx context.Context, cfg SyncJobConfig) error {
	if cfg.Queue == nil {
		return errors.New("catalog: the scheduled sync needs a queue")
	}
	j, err := newSyncJob(cfg)
	if err != nil {
		return err
	}
	worker := river.WorkFunc(func(jobCtx context.Context, job *river.Job[syncArgs]) error {
		roundCtx, cancel := context.WithCancel(jobCtx)
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		if ctx.Err() != nil {
			cancel()
		}
		return j.work(roundCtx, job)
	})
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), worker); err != nil {
		return fmt.Errorf("catalog: register the price sync: %w", err)
	}
	if err := j.registerStatusMetrics(); err != nil {
		return err
	}
	cfg.Queue.Every(syncEvery, func() river.JobArgs { return syncArgs{} })
	return nil
}

// newSyncJob is what a config describes: the catalog a round runs on and the record and alert it keeps. RegisterSync puts
// it on the queue; a test runs a round by hand.
func newSyncJob(cfg SyncJobConfig) (*syncJob, error) {
	if cfg.DB == nil || cfg.Fetcher == nil || cfg.Logger == nil {
		return nil, errors.New("catalog: the scheduled sync needs a database, a fetcher and a logger")
	}
	if cfg.AlertAfter == 0 {
		cfg.AlertAfter = syncAlertAfter
	}
	if cfg.AlertAfter < 0 {
		return nil, errors.New("catalog: the sync's alert threshold cannot be negative")
	}
	cat, err := Open(cfg.DB, cfg.Scope, Config{Logger: cfg.Logger})
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	meter := cfg.Meter
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("catalog")
	}
	overdue, err := meter.Int64Counter(syncOverdueMetric, metric.WithDescription(
		"Rounds of the price sync that found the prices overdue; an alert watches a rise."))
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	return &syncJob{
		cat: cat, fetcher: cfg.Fetcher, logger: cfg.Logger, now: cfg.Clock, alertAfter: cfg.AlertAfter, overdue: overdue,
		meter: meter,
	}, nil
}

// syncArgs is the job of one round. Its args are the same every round, so a round still waiting or working covers the
// next, and nodes restarted over and over do not stack rounds.
type syncArgs struct{}

// Kind implements river.JobArgs.
func (syncArgs) Kind() string { return "catalog.sync" }

// InsertOpts implements river.JobArgsWithInsertOpts. A round reaches the network and writes the catalog, so it runs on
// the syncs queue, never behind a settlement. A recorded fetch failure waits for the next daily round; it must not also
// trigger River's discarded-job alert after a short upstream outage.
func (syncArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: pg.QueueSyncs, MaxAttempts: 1,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
			rivertype.JobStateRetryable, rivertype.JobStateScheduled,
		}},
	}
}

// syncJob runs rounds and keeps their record. The record is in the database, not in the worker: it outlives a restart
// and a change of the queue's leader, and the counter is per node, so a round is counted where it ran.
type syncJob struct {
	cat        *Catalog
	fetcher    *Fetcher
	logger     *slog.Logger
	now        func() time.Time
	alertAfter time.Duration
	overdue    metric.Int64Counter
	meter      metric.Meter
}

// work runs a round, records how it went and reports prices that have gone unrefreshed for too long. A round that could
// not read a dataset completes after its failure is recorded; state gauges and the fallback overdue signal cover the
// outage. Only failure to record the outcome is returned to River.
func (j *syncJob) work(ctx context.Context, _ *river.Job[syncArgs]) error {
	reports, syncErr := j.cat.SyncAll(ctx, j.fetcher, true)
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), syncRecordTimeout)
	defer cancel()
	state, at, err := j.record(recordCtx, reports, syncErr)
	if err != nil {
		return errors.Join(syncErr, err)
	}
	j.report(recordCtx, state, syncErr, at)
	return nil
}

// record writes the round's outcome to catalog.sync_state and returns the record. A successful round read every
// dataset: it sets the last success, clears the failure streak and counts the price changes it left for a person. A
// failed round adds to the streak, remembers when the streak began, and leaves the last success and the pending changes
// as they were: a round that could not read every dataset knows only part of what waits.
func (j *syncJob) record(ctx context.Context, reports []SyncReport, syncErr error) (GetSyncStateRow, time.Time, error) {
	var state GetSyncStateRow
	var at time.Time
	err := j.cat.db.Tx(ctx, j.cat.scope, func(tx pg.Tx) error {
		q := New()
		var err error
		at, err = j.timestamp(ctx, tx)
		if err != nil {
			return err
		}
		if syncErr == nil {
			err = q.RecordSyncSuccess(ctx, tx, RecordSyncSuccessParams{LastSuccessAt: at, Pending: pendingChanges(reports)})
		} else {
			err = q.RecordSyncFailure(ctx, tx, at)
		}
		if err != nil {
			return err
		}
		state, err = q.GetSyncState(ctx, tx)
		return err
	})
	if err != nil {
		return GetSyncStateRow{}, time.Time{}, fmt.Errorf("catalog: record the sync: %w", err)
	}
	return state, at, nil
}

func (j *syncJob) timestamp(ctx context.Context, tx pg.Tx) (time.Time, error) {
	if j.now != nil {
		return j.now(), nil
	}
	var at time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("catalog: read the database clock: %w", err)
	}
	return at, nil
}

// A missing or ambiguous entry has no candidate price to confirm.
func pendingChanges(reports []SyncReport) int64 {
	var n int64
	for _, report := range reports {
		for _, result := range report.Results {
			if result.Outcome == OutcomeHeld && result.Candidate != nil {
				n++
			}
		}
	}
	return n
}

// report raises the alert when the prices have gone unrefreshed for longer than the threshold: the error log names the
// stable event, the counter rises by one, and the record says how far behind it is. A round that failed for its own
// reasons is logged by SyncAll with the dataset and the error; this is the lasting outage.
func (j *syncJob) report(ctx context.Context, state GetSyncStateRow, syncErr error, at time.Time) {
	since := syncStateSince(state)
	if since == nil || at.Sub(*since) <= j.alertAfter {
		return
	}
	j.overdue.Add(ctx, 1)
	j.logger.ErrorContext(ctx, syncOverdueEvent,
		"since", since.UTC().Format(time.RFC3339), "never_succeeded", state.LastSuccessAt == nil,
		"failures", state.Failures, "pending", state.Pending, "err", syncErr)
}
