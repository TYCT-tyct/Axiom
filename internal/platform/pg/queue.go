package pg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const (
	// queueSchema is where River keeps its tables. It is the queue's own and not a module's: the audits of Check that
	// every table of a module forces row-level security do not apply to it, since a job holds no tenant's rows.
	queueSchema = "river"
	// queueWorkers is how many jobs one node works at once.
	queueWorkers = 10
	// jobTimeout bounds one run of a job; River's own default of a minute is shorter than a round of syncing.
	jobTimeout = 10 * time.Minute
	// rescueAfter is how long a job may run before the queue takes it for lost. A node that crashes leaves its jobs
	// running, and a unique job in that state keeps its twin from being enqueued, so this is how long such a job holds
	// its supplier up. River needs it longer than jobTimeout, and its default would be an hour.
	rescueAfter = jobTimeout + 5*time.Minute
	// stopCancelGrace is how long jobs still running get, after the deadline given to Stop, to notice they are cancelled.
	stopCancelGrace = 5 * time.Second
	// discardedEvent is the stable name of the error log a discarded job's report writes, for alerting
	// (docs/domains/observability.md「监控与告警」). River itself logs a final failure only at info level.
	discardedEvent = "queue.job_discarded"
	// discardedMetric counts the discarded jobs the queue reports, by kind, so an alert can select the kinds that must
	// not fail. A failed scan transaction can report the same job again; the alert watches a rise, not a total.
	discardedMetric = "axiom.pg.queue.discarded_jobs"
	// discardedScanEvery is how often the queue looks for jobs that reached the discarded state. It must be shorter
	// than the time an operator is willing to wait, and longer than a scan.
	discardedScanEvery = time.Minute
	// discardedRetention is how long a discarded job's row is kept after it finished. River's own cleaner does not
	// delete discarded jobs (Start sets its retention to -1); the scan deletes a job only after claiming it, once the
	// job is this old. A job discarded before an outage therefore survives to be reported when the queue returns.
	discardedRetention = 24 * time.Hour
)

// Queue is the job queue, River on the application's connection pool (docs/architecture.md「技术选型」). A module
// registers its workers and periodic jobs before Start, and enqueues a job with InsertManyTx in the transaction of the
// write that causes it, so the job exists if and only if that write does. A job runs at least once, so a worker must
// tolerate a repeat.
type Queue struct {
	db        *DB
	logger    *slog.Logger
	workers   *river.Workers
	periodic  []*river.PeriodicJob
	discarded metric.Int64Counter
	client    atomic.Pointer[river.Client[pgx.Tx]]
}

// NewQueue makes a queue that does nothing until Start. meter reports the discarded jobs the queue finds; nil for none.
func (db *DB) NewQueue(logger *slog.Logger, meter metric.Meter) *Queue {
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("pg")
	}
	q := &Queue{db: db, logger: logger, workers: river.NewWorkers(), discarded: newDiscardedCounter(meter)}
	q.Every(discardedScanEvery, func() river.JobArgs { return discardedScanArgs{} })
	return q
}

// Workers is where a module registers the workers of its jobs (river.AddWorker), before Start.
func (q *Queue) Workers() *river.Workers { return q.workers }

// Every makes the queue insert the job args returns once every interval, and once when it starts. Only the node that
// leads the queue inserts it, so the job is not inserted by every node, but a node that dies or a change of leader
// restarts the count: a periodic job must be one whose repeat or delay costs nothing. Call it before Start.
func (q *Queue) Every(interval time.Duration, args func() river.JobArgs) {
	q.periodic = append(q.periodic, river.NewPeriodicJob(river.PeriodicInterval(interval),
		func() (river.JobArgs, *river.InsertOpts) { return args(), nil }, &river.PeriodicJobOpts{RunOnStart: true}))
}

// Start begins working jobs. It refuses a database whose queue tables Migrate has not made. The queue runs until Stop,
// whatever becomes of ctx: River would otherwise cancel the jobs it is working the moment ctx ends, before the shutdown
// that decides how long they get has begun, and a job that is cut short mid-way (a login token spent and not yet
// stored, say) is the harm Stop's deadline exists to avoid.
func (q *Queue) Start(ctx context.Context) error {
	if err := river.AddWorkerSafely(q.workers, river.WorkFunc(q.workDiscardedScan)); err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	client, err := river.NewClient(riverpgxv5.New(q.db.pool), &river.Config{
		Logger: q.logger, Schema: queueSchema, Workers: q.workers, PeriodicJobs: q.periodic, JobTimeout: jobTimeout,
		RescueStuckJobsAfter: rescueAfter, DiscardedJobRetentionPeriod: -1, // the scan deletes them, after claiming them
		Queues: map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: queueWorkers}},
	})
	if err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	q.client.Store(client) // before Start: a worker that runs at once may enqueue
	if err := client.Start(context.WithoutCancel(ctx)); err != nil {
		q.client.Store(nil)
		return fmt.Errorf("queue: start: %w", err)
	}
	return nil
}

// Stop stops fetching jobs and waits for those running to finish, until ctx ends; then it cancels them and waits a few
// seconds more. A job cut short is run again by the next node that fetches it. A second Stop does nothing.
func (q *Queue) Stop(ctx context.Context) error {
	client := q.client.Swap(nil) // a second Stop, and a job enqueued after the first, find no queue
	if client == nil {
		return nil
	}
	err := client.Stop(ctx)
	if err == nil || ctx.Err() == nil {
		return err
	}
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopCancelGrace)
	defer cancel()
	return errors.Join(err, client.StopAndCancel(cancelCtx))
}

// InsertManyTx enqueues jobs in the transaction tx and reports how many were new: a job whose InsertOpts make it unique
// and that another job already covers is skipped, which is no error. The queue must have been started.
func (q *Queue) InsertManyTx(ctx context.Context, tx Tx, jobs []river.InsertManyParams) (inserted int, err error) {
	client := q.client.Load()
	if client == nil {
		return 0, errors.New("queue: not started")
	}
	results, err := client.InsertManyTx(ctx, tx.tx, jobs)
	if err != nil {
		return 0, fmt.Errorf("queue: enqueue: %w", err)
	}
	for _, r := range results {
		if !r.UniqueSkippedAsDuplicate {
			inserted++
		}
	}
	return inserted, nil
}

// discardedScanArgs is the queue's periodic scan of jobs River has discarded.
type discardedScanArgs struct{}

// Kind implements river.JobArgs.
func (discardedScanArgs) Kind() string { return "queue.discard_scan" }

// InsertOpts implements river.JobArgsWithInsertOpts. The next scan is the retry: a scan that fails is itself a
// discarded job the next scan reports.
func (discardedScanArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 1} }

// discardedJob is a job the scan claimed to report.
type discardedJob struct {
	id      int64
	kind    string
	attempt int16
}

func (q *Queue) workDiscardedScan(ctx context.Context, _ *river.Job[discardedScanArgs]) error {
	return q.scanDiscarded(ctx)
}

// scanDiscarded claims every discarded job not reported before, reports each as it is claimed, and then deletes the
// claimed jobs that are past the retention period. The claim is a row keyed by job id, so a second scan, another node
// or a restart reports nothing; if the transaction rolls back after a report, the next scan reports again (at least
// once). River's cleaner does not delete discarded jobs, so an unclaimed job older than the retention is still claimed
// and reported before this cleanup removes it.
func (q *Queue) scanDiscarded(ctx context.Context) error {
	err := q.db.Tx(ctx, Scope{}, func(tx Tx) error {
		rows, err := tx.Query(ctx, `INSERT INTO river.axiom_discarded_job (job_id, kind, attempt)
			SELECT id, kind, attempt FROM river.river_job WHERE state = 'discarded'
			ON CONFLICT (job_id) DO NOTHING
			RETURNING job_id, kind, attempt`)
		if err != nil {
			return fmt.Errorf("claim discarded jobs: %w", err)
		}
		for rows.Next() {
			var d discardedJob
			if err := rows.Scan(&d.id, &d.kind, &d.attempt); err != nil {
				rows.Close()
				return fmt.Errorf("read a discarded job: %w", err)
			}
			q.reportDiscarded(ctx, d)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("read discarded jobs: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM river.river_job j
			WHERE j.state = 'discarded'
				AND j.finalized_at < now() - make_interval(hours => $1)
				AND EXISTS (SELECT FROM river.axiom_discarded_job s WHERE s.job_id = j.id)`,
			int32(discardedRetention.Hours())); err != nil {
			return fmt.Errorf("delete reported discards: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM river.axiom_discarded_job s
			WHERE NOT EXISTS (SELECT FROM river.river_job j WHERE j.id = s.job_id)`); err != nil {
			return fmt.Errorf("delete claims whose job is gone: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan discarded jobs: %w", err)
	}
	return nil
}

// reportDiscarded is the signal for one job River will not run again: the error log and the counter.
func (q *Queue) reportDiscarded(ctx context.Context, d discardedJob) {
	q.discarded.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", d.kind)))
	q.logger.ErrorContext(ctx, discardedEvent, "job_id", d.id, "kind", d.kind, "attempt", d.attempt)
}

func newDiscardedCounter(meter metric.Meter) metric.Int64Counter {
	c, err := meter.Int64Counter(discardedMetric, metric.WithDescription("Discarded jobs reported by the queue, by kind."))
	if err != nil {
		panic(err) // the instrument name is a constant
	}
	return c
}
