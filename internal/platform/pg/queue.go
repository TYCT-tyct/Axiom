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
	"github.com/riverqueue/river/rivertype"
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
	// discardedEvent is the stable name of the error log a job's discard writes, for alerting
	// (docs/domains/observability.md「监控与告警」). River itself logs the final failure only at info level.
	discardedEvent = "queue.job_discarded"
	// discardedMetric counts the jobs River discards, by kind, so an alert can select the kinds that must not fail.
	discardedMetric = "axiom.pg.queue.discarded_jobs"
)

// Queue is the job queue, River on the application's connection pool (docs/architecture.md「技术选型」). A module
// registers its workers and periodic jobs before Start, and enqueues a job with InsertManyTx in the transaction of the
// write that causes it, so the job exists if and only if that write does. A job runs at least once, so a worker must
// tolerate a repeat.
type Queue struct {
	db       *DB
	logger   *slog.Logger
	workers  *river.Workers
	periodic []*river.PeriodicJob
	handler  *discardHandler
	client   atomic.Pointer[river.Client[pgx.Tx]]
}

// NewQueue makes a queue that does nothing until Start. meter reports jobs River discards; nil for none.
func (db *DB) NewQueue(logger *slog.Logger, meter metric.Meter) *Queue {
	return &Queue{db: db, logger: logger, workers: river.NewWorkers(), handler: newDiscardHandler(logger, meter)}
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
	client, err := river.NewClient(riverpgxv5.New(q.db.pool), &river.Config{
		Logger: q.logger, Schema: queueSchema, Workers: q.workers, PeriodicJobs: q.periodic, JobTimeout: jobTimeout,
		RescueStuckJobsAfter: rescueAfter, ErrorHandler: q.handler,
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

// discardHandler turns the last failure of a job into an error log and a metric. River uses the same condition for
// the last attempt (Attempt >= MaxAttempts) and then marks the job discarded, so the alert fires exactly when a job
// will not run again. A cancelled or snoozed job ends another way and never reaches the handler.
type discardHandler struct {
	logger    *slog.Logger
	discarded metric.Int64Counter
}

func newDiscardHandler(logger *slog.Logger, meter metric.Meter) *discardHandler {
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("pg")
	}
	return &discardHandler{logger: logger, discarded: newDiscardedCounter(meter)}
}

func newDiscardedCounter(meter metric.Meter) metric.Int64Counter {
	c, err := meter.Int64Counter(discardedMetric, metric.WithDescription("Jobs River discarded after their last attempt failed."))
	if err != nil {
		panic(err) // the instrument name is a constant
	}
	return c
}

// HandleError implements river.ErrorHandler.
func (h *discardHandler) HandleError(ctx context.Context, job *rivertype.JobRow, err error) *river.ErrorHandlerResult {
	if job.Attempt >= job.MaxAttempts {
		h.discarded.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", job.Kind)))
		h.logger.ErrorContext(ctx, discardedEvent, "job_id", job.ID, "kind", job.Kind, "attempt", job.Attempt, "err", err)
	}
	return nil
}

// HandlePanic implements river.ErrorHandler. A panic on the last attempt discards the job just as an error does.
func (h *discardHandler) HandlePanic(ctx context.Context, job *rivertype.JobRow, panicVal any, _ string) *river.ErrorHandlerResult {
	if job.Attempt >= job.MaxAttempts {
		h.discarded.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", job.Kind)))
		h.logger.ErrorContext(ctx, discardedEvent, "job_id", job.ID, "kind", job.Kind, "attempt", job.Attempt, "panic", panicVal)
	}
	return nil
}
