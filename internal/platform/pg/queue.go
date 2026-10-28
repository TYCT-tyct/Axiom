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
	// oldestWaitMetric is how long the oldest available job of each queue has waited, by queue: the measure of whether
	// a queue's workers keep up. It rises while work waits, and is 0 for a queue with nothing waiting.
	oldestWaitMetric = "axiom.pg.queue.oldest_job_wait"
	// oldestWaitTimeout bounds the database work behind one collection of oldestWaitMetric: the wait for a pooled
	// connection, the query, and the transaction's end. The Prometheus exporter collects with a context of its own that
	// has no deadline, and the SDK holds the metric pipeline's lock while a callback runs, so an unbounded query here
	// would hold back every metric of the process, not this gauge alone. A collection that runs out of time reports the
	// error and no wait: the gauge keeps its last value, and the next collection asks again.
	oldestWaitTimeout = 2 * time.Second
	// defaultQueueWorkers is the one worker River's default queue gets, to finish the jobs an earlier version enqueued
	// there before the split. No kind names it.
	defaultQueueWorkers = 1
)

// The queues every node works. A job kind names its queue in InsertOpts, its only assignment, so a burst of one kind
// waits behind that queue's workers alone (issue #321 E8-d). River's default queue is not one of them: it only drains
// the jobs an earlier version left there (queueConfigs), and the table test in queue_kinds_test.go catches a kind added
// without a queue or on a queue a kind may not name.
const (
	// QueueMoney is the queue of the jobs whose lateness costs money: the expiry of credit, and the reconciliation of
	// charges and of the ledger.
	QueueMoney = "money"
	// QueueNotifications is the queue of email and alerts. Only mail.send registers on it so far; it has workers so that the
	// first one takes its own budget rather than another queue's.
	QueueNotifications = "notifications"
	// QueueSyncs is the queue of the jobs that reach a supplier: a sweep, a round of one supplier, and the price syncs
	// when they arrive. They are the long jobs, waiting on the network.
	QueueSyncs = "syncs"
	// QueueArchive is the queue of housekeeping: the scan of discarded jobs today, retention when it lands.
	QueueArchive = "archive"
)

// queues are the queues a node works and the workers each may run at once. Together they add up to the ten the single
// queue had, so the split changes which jobs wait for each other, not how much a node runs; a burst of syncs or archive
// jobs can no longer hold a money job back. The numbers are code defaults: only a deployment that has measured its own
// load should change them, and only through a deliberate departure from the default.
var queues = []struct {
	name    string
	workers int
}{
	{QueueMoney, 3},
	{QueueNotifications, 2},
	{QueueSyncs, 4},
	{QueueArchive, 1},
}

// queueConfigs is the table in the form River takes, built from it so the two cannot drift. River's default queue is
// added too, with one worker: it holds the jobs an earlier version enqueued before the split, and working them frees
// their unique keys, which would otherwise cover the periodic job that now names its own queue.
func queueConfigs() map[string]river.QueueConfig {
	configs := make(map[string]river.QueueConfig, len(queues)+1)
	configs[river.QueueDefault] = river.QueueConfig{MaxWorkers: defaultQueueWorkers}
	for _, q := range queues {
		configs[q.name] = river.QueueConfig{MaxWorkers: q.workers}
	}
	return configs
}

// Queue is the job queue, River on the application's connection pool (docs/architecture.md「技术选型」). A module
// registers its workers and periodic jobs before Start, and enqueues a job with InsertManyTx in the transaction of the
// write that causes it, so the job exists if and only if that write does. A job runs at least once, so a worker must
// tolerate a repeat.
type Queue struct {
	db       *DB
	scope    Scope // the queue's own table; jobs carry no tenant's rows
	logger   *slog.Logger
	workers  *river.Workers
	periodic []*river.PeriodicJob
	// OnDiscard persists an observer's result with the claim; install before Start.
	OnDiscard func(context.Context, Tx, string, []byte) error
	discarded metric.Int64Counter
	client    atomic.Pointer[river.Client[pgx.Tx]]
}

// NewQueue makes a queue that does nothing until Start. scope is the platform scope, which only cmd/ builds; meter
// reports the discarded jobs the queue finds and the wait of each queue's oldest job, nil for none.
func (db *DB) NewQueue(logger *slog.Logger, scope Scope, meter metric.Meter) *Queue {
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("pg")
	}
	q := &Queue{db: db, scope: scope, logger: logger, workers: river.NewWorkers(), discarded: newDiscardedCounter(meter)}
	if _, err := meter.Float64ObservableGauge(oldestWaitMetric,
		metric.WithUnit("s"),
		metric.WithDescription("How long the oldest available job of each queue has waited."),
		metric.WithFloat64Callback(q.observeOldestWait)); err != nil {
		panic(err) // the instrument name is a constant
	}
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
	// River uses only ordinary connections. Its driver detaches one for LISTEN (see the connection budget in README).
	client, err := river.NewClient(riverpgxv5.New(q.db.pool), &river.Config{
		Logger: q.logger, Schema: queueSchema, Workers: q.workers, PeriodicJobs: q.periodic, JobTimeout: jobTimeout,
		RescueStuckJobsAfter: rescueAfter, DiscardedJobRetentionPeriod: -1, // the scan deletes them, after claiming them
		Queues: queueConfigs(),
		// A node fetches only the kinds it has workers for: during a rolling deploy an older node would otherwise take a
		// kind it does not know, spend an attempt on River's unknown-kind error and back off, and a unique job in that
		// state lets no twin be enqueued. Every node registers the same workers within one version, so the mixed set
		// lasts only as long as a deploy. This affects fetching alone: a leader's rescuer still discards stuck jobs of
		// kinds it does not know, and only those that ran past rescueAfter.
		FetchOnlyKnownKinds: true,
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
// discarded job the next scan reports. It runs on the archive queue: it is housekeeping, and must not take a money
// worker from a settlement.
func (discardedScanArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueArchive, MaxAttempts: 1}
}

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
	err := q.db.Tx(ctx, q.scope, func(tx Tx) error {
		return q.scanDiscardedTx(ctx, tx)
	})
	if err != nil {
		return fmt.Errorf("scan discarded jobs: %w", err)
	}
	return nil
}

func (q *Queue) scanDiscardedTx(ctx context.Context, tx Tx) error {
	rows, err := tx.Query(ctx, `INSERT INTO platform.discarded_jobs (job_id, kind, attempt)
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
	if err := q.observeDiscards(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM river.river_job j
			WHERE j.state = 'discarded'
				AND j.finalized_at < now() - make_interval(hours => $1)
				AND EXISTS (SELECT FROM platform.discarded_jobs s WHERE s.job_id = j.id)`,
		int32(discardedRetention.Hours())); err != nil {
		return fmt.Errorf("delete reported discards: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.discarded_jobs s
			WHERE NOT EXISTS (SELECT FROM river.river_job j WHERE j.id = s.job_id)`); err != nil {
		return fmt.Errorf("delete claims whose job is gone: %w", err)
	}
	return nil
}

// reportDiscarded is the signal for one job River will not run again: the error log and the counter.
func (q *Queue) reportDiscarded(ctx context.Context, d discardedJob) {
	q.discarded.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", d.kind)))
	q.logger.ErrorContext(ctx, discardedEvent, "job_id", d.id, "kind", d.kind, "attempt", d.attempt)
}

// observeOldestWait reports every queue's oldest wait, 0 for a queue with nothing waiting: a scrape of an idle queue
// then reads 0 instead of the last value it happened to have. It reports nothing when the database does not answer
// within oldestWaitTimeout, and says why: the last reported values stay in place, and a failed collection is visible
// rather than read as a queue that caught up.
func (q *Queue) observeOldestWait(ctx context.Context, o metric.Float64Observer) error {
	ctx, cancel := context.WithTimeout(ctx, oldestWaitTimeout)
	defer cancel()
	waits, err := q.oldestWaits(ctx)
	if err != nil {
		return err
	}
	for _, queue := range queues {
		o.Observe(waits[queue.name], metric.WithAttributes(attribute.String("queue", queue.name)))
	}
	return nil
}

// oldestWaits is how long each queue's oldest available job has been ready to run, in seconds. One still scheduled for
// later is not waiting yet: River makes it available when its time comes. 0 for a queue with no available job.
func (q *Queue) oldestWaits(ctx context.Context) (map[string]float64, error) {
	waits := make(map[string]float64, len(queues))
	err := q.db.ReadTx(ctx, q.scope, func(tx Tx) error {
		rows, err := tx.Query(ctx, `SELECT queue, extract(epoch FROM now() - min(scheduled_at))::float8
			FROM river.river_job WHERE state = 'available' GROUP BY queue`)
		if err != nil {
			return fmt.Errorf("read the oldest waiting jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var queue string
			var seconds float64
			if err := rows.Scan(&queue, &seconds); err != nil {
				return fmt.Errorf("read an oldest waiting job: %w", err)
			}
			waits[queue] = seconds
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("queue: %w", err)
	}
	return waits, nil
}

func newDiscardedCounter(meter metric.Meter) metric.Int64Counter {
	c, err := meter.Int64Counter(discardedMetric, metric.WithDescription("Discarded jobs reported by the queue, by kind."))
	if err != nil {
		panic(err) // the instrument name is a constant
	}
	return c
}

func (q *Queue) observeDiscards(ctx context.Context, tx Tx) error {
	if q.OnDiscard == nil {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT s.job_id,s.kind,j.args FROM platform.discarded_jobs s JOIN river.river_job j ON j.id=s.job_id WHERE NOT s.notified FOR UPDATE OF s SKIP LOCKED`)
	if err != nil {
		return err
	}
	type notice struct {
		id   int64
		kind string
		args []byte
	}
	var notices []notice
	for rows.Next() {
		var n notice
		if err = rows.Scan(&n.id, &n.kind, &n.args); err != nil {
			break
		}
		notices = append(notices, n)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range notices {
		if err = q.OnDiscard(ctx, tx, n.kind, n.args); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE platform.discarded_jobs SET notified=true WHERE job_id=$1`, n.id); err != nil {
			return err
		}
	}
	return nil
}
