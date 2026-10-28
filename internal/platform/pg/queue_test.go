package pg_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var platform = pg.NewScope(true, nil, nil)

// ping is a job that carries a number, and tells the test when a worker ran it.
type ping struct {
	N int `json:"n"`
}

func (ping) Kind() string { return "test.ping" }

// InsertOpts makes a ping of one number unique, so that a second insert of it is skipped.
func (ping) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueMoney, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

type pinger struct {
	river.WorkerDefaults[ping]
	ran chan int
}

func (p *pinger) Work(_ context.Context, job *river.Job[ping]) error {
	p.ran <- job.Args.N
	return nil
}

// startQueue starts a queue with a pinger on a fresh database, and stops it with the test.
func startQueue(t *testing.T) (*pg.DB, *pg.Queue, chan int) {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	q := db.NewQueue(slog.New(slog.DiscardHandler), platform, nil)
	worker := &pinger{ran: make(chan int, 10)}
	river.AddWorker(q.Workers(), worker)
	if err := q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := q.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return db, q, worker.ran
}

func enqueue(t *testing.T, db *pg.DB, q *pg.Queue, fail error, numbers ...int) (inserted int) {
	t.Helper()
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) (err error) {
		var jobs []river.InsertManyParams
		for _, n := range numbers {
			jobs = append(jobs, river.InsertManyParams{Args: ping{N: n}})
		}
		if inserted, err = q.InsertManyTx(t.Context(), tx, jobs); err != nil {
			return err
		}
		return fail
	})
	if !errors.Is(err, fail) {
		t.Fatalf("the transaction ended with %v, want %v", err, fail)
	}
	return inserted
}

func ran(t *testing.T, ch chan int) int {
	t.Helper()
	select {
	case n := <-ch:
		return n
	case <-time.After(20 * time.Second):
		t.Fatal("no job ran")
		return 0
	}
}

// TestAJobExistsIfAndOnlyIfItsTransactionCommits: a job enqueued in a transaction that rolls back is never worked and
// leaves no row; one enqueued in a transaction that commits is worked; a unique job that another covers is skipped.
func TestAJobExistsIfAndOnlyIfItsTransactionCommits(t *testing.T) {
	t.Parallel()
	db, q, worked := startQueue(t)
	rolledBack := errors.New("the business write failed")
	enqueue(t, db, q, rolledBack, 1)
	if got := enqueue(t, db, q, nil, 2); got != 1 {
		t.Fatalf("enqueued %d jobs, want 1", got)
	}
	if n := ran(t, worked); n != 2 {
		t.Fatalf("job %d ran first, want only 2: the rolled-back job must not exist", n)
	}
	if got := enqueue(t, db, q, nil, 2, 3); got != 1 {
		t.Errorf("enqueued %d of a job already covered and a new one, want 1", got)
	}
	if n := ran(t, worked); n != 3 {
		t.Errorf("job %d ran, want 3", n)
	}
	var rows int
	err := db.ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM river.river_job WHERE kind = 'test.ping'").Scan(&rows)
	})
	if err != nil || rows != 2 {
		t.Errorf("%d jobs in the table (%v), want 2: the rolled-back one is not there", rows, err)
	}
}

// TestMigrateMakesTheQueueTables: on a database that has nothing, Migrate makes River's tables at the version of the
// library in one transaction (River deprecated its all-in-one-transaction call for the migrations that cannot be, and a
// version of River that has one makes this fail), and the application, which Check lets in, can work the jobs.
func TestMigrateMakesTheQueueTables(t *testing.T) {
	t.Parallel()
	fresh := freshDatabase(t)
	if err := pg.Migrate(t.Context(), fresh.Migrator); err != nil {
		t.Fatal(err)
	}
	db, err := pg.Open(t.Context(), fresh.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO river.river_job (kind, args, state, queue, max_attempts)
			VALUES ('test.ping', '{}', 'available', 'default', 1)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), "DELETE FROM river.river_job")
		return err
	})
	if err != nil {
		t.Errorf("the application working the queue's tables: %v", err)
	}
}

// TestAStoppedQueueStopsAgain: Stop is safe to call twice, as serve does on its way out, and a job cannot be enqueued
// after it.
func TestAStoppedQueueStopsAgain(t *testing.T) {
	t.Parallel()
	db, q, _ := startQueue(t)
	for range 2 {
		if err := q.Stop(t.Context()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := q.InsertManyTx(t.Context(), tx, []river.InsertManyParams{{Args: ping{N: 1}}})
		return err
	})
	if err == nil {
		t.Error("a job was enqueued on a stopped queue")
	}
}

// TestAJobCannotBeEnqueuedBeforeTheQueueStarts: a queue that has not started has no client to enqueue with.
func TestAJobCannotBeEnqueuedBeforeTheQueueStarts(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	q := db.NewQueue(slog.New(slog.DiscardHandler), platform, nil)
	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := q.InsertManyTx(t.Context(), tx, []river.InsertManyParams{{Args: ping{N: 1}}})
		return err
	})
	if err == nil {
		t.Error("a job was enqueued on a queue that has not started")
	}
}

// TestCheckRefusesADatabaseWithoutTheQueue: a database whose queue tables are missing or behind is refused at the start
// with the instruction to migrate, not left to fail at the first job.
func TestCheckRefusesADatabaseWithoutTheQueue(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t)
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Check(t.Context()); err != nil {
		t.Fatalf("Check on a migrated database: %v", err)
	}
	// A queue one migration behind the library's is refused, not run against.
	if err := pgtest.Run(t.Context(), database.Superuser, "DELETE FROM river.river_migration WHERE version = (SELECT max(version) FROM river.river_migration)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "queue") || !strings.Contains(err.Error(), "axiom migrate") {
		t.Errorf("Check on a queue that lacks its last migration: %v", err)
	}
	if err := pgtest.Run(t.Context(), database.Superuser, "DROP TABLE river.river_migration"); err != nil {
		t.Fatal(err)
	}
	if err := db.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "queue") || !strings.Contains(err.Error(), "axiom migrate") {
		t.Errorf("Check on a database without the queue's version table: %v", err)
	}
}

// held is a job that works until its test lets it go or its context ends, and reports how it ended.
type held struct{ N int }

func (held) Kind() string { return "test.held" }

// InsertOpts puts the held job on the money queue, where the test queue's workers fetch it.
func (held) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: pg.QueueMoney} }

type holder struct {
	river.WorkerDefaults[held]
	started chan struct{}
	release chan struct{}
	ended   chan error // the context's error when the job ended, nil if it was let go
}

func (h *holder) Work(ctx context.Context, _ *river.Job[held]) error {
	h.started <- struct{}{}
	select {
	case <-h.release:
		h.ended <- nil
	case <-ctx.Done():
		h.ended <- context.Cause(ctx)
	}
	return nil
}

// runningHeldJob starts a queue on a context the test can cancel, as serve's is when the node is told to stop, and
// returns once a held job is running on it.
func runningHeldJob(t *testing.T) (q *pg.Queue, h *holder, endNode context.CancelFunc) {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	q = db.NewQueue(slog.New(slog.DiscardHandler), platform, nil)
	h = &holder{started: make(chan struct{}, 1), release: make(chan struct{}), ended: make(chan error, 1)}
	river.AddWorker(q.Workers(), h)
	ctx, endNode := context.WithCancel(t.Context())
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := q.InsertManyTx(t.Context(), tx, []river.InsertManyParams{{Args: held{N: 1}}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.started:
	case <-time.After(20 * time.Second):
		t.Fatal("the job did not start")
	}
	return q, h, endNode
}

// TestANodeWorksOnlyTheJobKindsItHasWorkersFor: during a rolling deploy an older node shares the queue with a kind it
// has no worker for. It leaves that job available and spends no attempt on it, so the node that has the worker runs it;
// River's default would fail it with an unknown-kind error, spend an attempt and back off, and a unique job in that
// state keeps its twin from being enqueued.
func TestANodeWorksOnlyTheJobKindsItHasWorkersFor(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	older := db.NewQueue(slog.New(slog.DiscardHandler), platform, nil) // a node from before test.ping existed
	startDiscardQueue(t, older)
	// The row is what the newer node's InsertManyTx leaves: a queue that has the worker would work the job at once, and
	// River refuses to insert a kind its own workers do not cover. It is on a queue the older node does work, so only
	// FetchOnlyKnownKinds keeps it off the unknown kind.
	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO river.river_job (kind, args, state, queue, max_attempts)
			VALUES ('test.ping', '{"n": 1}', 'available', $1, 1)`, pg.QueueMoney)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second) // three fetch intervals: the older node has had every chance to take the job
	if state, attempt := jobStateAndAttempt(t, db, "test.ping"); state != "available" || attempt != 0 {
		t.Fatalf("the job is %q after attempt %d on a node without its worker, want available and no attempt", state, attempt)
	}

	newer := db.NewQueue(slog.New(slog.DiscardHandler), platform, nil)
	worked := make(chan int, 1)
	river.AddWorker(newer.Workers(), &pinger{ran: worked})
	startDiscardQueue(t, newer)
	if n := ran(t, worked); n != 1 {
		t.Fatalf("job %d ran, want 1", n)
	}
	// The worker reports before the queue writes the job's final state.
	eventually(t, "the job to be completed", func() bool { return jobState(t, db, "test.ping") == "completed" })
	if _, attempt := jobStateAndAttempt(t, db, "test.ping"); attempt != 1 {
		t.Errorf("the job took %d attempts, want 1: the node without its worker spent none", attempt)
	}
}

// TestAJobLeftOnTheDefaultQueueIsFinished: an earlier version enqueued every kind on River's default queue, which no
// kind names now. The queue keeps one worker there, so such a job finishes and its unique key stops covering the
// periodic job that now names its own queue.
func TestAJobLeftOnTheDefaultQueueIsFinished(t *testing.T) {
	t.Parallel()
	db, _, worked := startQueue(t)
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO river.river_job (kind, args, state, queue, max_attempts)
			VALUES ('test.ping', '{"n": 7}', 'available', 'default', 1)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := ran(t, worked); n != 7 {
		t.Fatalf("job %d ran, want 7", n)
	}
}

// jobStateAndAttempt is the state and the attempt count of the latest job of kind.
func jobStateAndAttempt(t *testing.T, db *pg.DB, kind string) (state string, attempt int16) {
	t.Helper()
	err := db.ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT state, attempt FROM river.river_job WHERE kind = $1 ORDER BY id DESC LIMIT 1", kind).
			Scan(&state, &attempt)
	})
	if err != nil {
		t.Fatal(err)
	}
	return state, attempt
}

// TestAJobRunningWhenTheNodeIsToldToStopIsLeftToFinish: the context Start was given ending does not cancel the jobs
// running. Stop waits for them until its own deadline: a job that finishes before it ends undisturbed, and one that
// does not is cancelled by it, and only then.
func TestAJobRunningWhenTheNodeIsToldToStopIsLeftToFinish(t *testing.T) {
	t.Parallel()
	t.Run("finishes within the deadline", func(t *testing.T) {
		t.Parallel()
		q, h, endNode := runningHeldJob(t)
		endNode()
		stopped := make(chan error, 1)
		go func() { stopped <- q.Stop(context.WithoutCancel(t.Context())) }()
		select {
		case err := <-h.ended:
			t.Fatalf("the job ended before it was let go: %v", err)
		case <-time.After(500 * time.Millisecond):
		}
		close(h.release)
		if err := <-h.ended; err != nil {
			t.Errorf("the job was cancelled although it finished within the deadline: %v", err)
		}
		if err := <-stopped; err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	t.Run("outlasts the deadline", func(t *testing.T) {
		t.Parallel()
		q, h, endNode := runningHeldJob(t)
		endNode()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		if err := q.Stop(ctx); err == nil {
			t.Error("Stop reported a clean stop although a job outlasted its deadline")
		}
		select {
		case err := <-h.ended:
			if err == nil {
				t.Error("the job was let go, not cancelled")
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the job was not cancelled after the deadline")
		}
	})
}

// blocked is a job on the syncs queue that runs until its test lets it go, reporting when it started.
type blocked struct{ N int }

// Kind implements river.JobArgs.
func (blocked) Kind() string { return "test.blocked" }

// InsertOpts puts the blocking job on the syncs queue, the budget a burst of supplier syncs shares.
func (blocked) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: pg.QueueSyncs} }

type blocker struct {
	river.WorkerDefaults[blocked]
	started chan struct{}
	release chan struct{}
}

func (b *blocker) Work(ctx context.Context, _ *river.Job[blocked]) error {
	b.started <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil
}

// paid is a job on the money queue; it only reports that it ran.
type paid struct{ N int }

// Kind implements river.JobArgs.
func (paid) Kind() string { return "test.paid" }

// InsertOpts puts it on the money queue, which a sync burst must not reach.
func (paid) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: pg.QueueMoney} }

type payer struct {
	river.WorkerDefaults[paid]
	ran chan int
}

func (p *payer) Work(_ context.Context, job *river.Job[paid]) error {
	p.ran <- job.Args.N
	return nil
}

// TestASaturatedSyncsQueueDoesNotDelayAMoneyJob: every worker the syncs queue has is held by a job that will not finish,
// and a money job still runs on the money queue's own workers. With one queue and one budget the money job would wait
// for a sync to finish (issue #321 E8-d): the test enqueues one more blocking job than every worker a node has, so one
// queue would hold them all. The syncs count comes from the code default, so it fills what a node really runs.
func TestASaturatedSyncsQueueDoesNotDelayAMoneyJob(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	q := db.NewQueue(slog.New(slog.DiscardHandler), platform, nil)
	syncs := pg.QueueWorkers(pg.QueueSyncs)
	// One more sync job than every worker the node has, so that if the queues were one, every worker would be held and
	// the money job would wait behind these.
	burst := pg.QueueWorkersTotal() + 1
	b := &blocker{started: make(chan struct{}, burst), release: make(chan struct{})}
	p := &payer{ran: make(chan int, 1)}
	river.AddWorker(q.Workers(), b)
	river.AddWorker(q.Workers(), p)
	if err := q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := q.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	// Fill every worker of the syncs queue, and wait until each holds one: the queue is then saturated for as long as
	// the test keeps the jobs running.
	saturate := make([]river.InsertManyParams, burst)
	for i := range saturate {
		saturate[i] = river.InsertManyParams{Args: blocked{N: i}}
	}
	enqueueJobs(t, db, q, saturate)
	for range syncs {
		select {
		case <-b.started:
		case <-time.After(20 * time.Second):
			t.Fatal("a sync job did not start: the syncs queue is not saturated")
		}
	}
	enqueueJobs(t, db, q, []river.InsertManyParams{{Args: paid{N: 1}}})
	if n := ran(t, p.ran); n != 1 {
		t.Fatalf("money job %d ran, want 1", n)
	}
	close(b.release)
}

// enqueueJobs inserts jobs in a transaction of their own, for a test that does not need to check the count.
func enqueueJobs(t *testing.T, db *pg.DB, q *pg.Queue, jobs []river.InsertManyParams) {
	t.Helper()
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := q.InsertManyTx(t.Context(), tx, jobs)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
