package pg_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// stuck is a registered job kind whose running row the rescuer discards.
type stuck struct{}

func (stuck) Kind() string { return "test.stuck" }

type stuckWorker struct{ river.WorkerDefaults[stuck] }

func (*stuckWorker) Work(context.Context, *river.Job[stuck]) error { return nil }

// lockedBuffer collects logs written by the queue's goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// TestARescuedDiscardIsReportedOnce: River's rescuer discards a job whose last attempt's node died without calling a
// worker or an error handler; the scan of the persisted state reports it, and a second scan reports nothing.
func TestARescuedDiscardIsReportedOnce(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	var id int64
	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO river.river_job
			(kind, args, state, queue, max_attempts, attempt, attempted_at)
			VALUES ('test.stuck', '{}', 'running', 'default', 1, 1, now() - interval '16 minutes')
			RETURNING id`).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}

	reader := sdkmetric.NewManualReader()
	logs := &lockedBuffer{}
	q := newDiscardQueue(db, reader, logs)
	var notices atomic.Int32
	q.OnDiscard = func(context.Context, pg.Tx, string, []byte) error { notices.Add(1); return nil }
	river.AddWorker(q.Workers(), &stuckWorker{})
	startDiscardQueue(t, q)
	eventually(t, "the rescuer to discard the stuck job", func() bool { return jobState(t, db, "test.stuck") == "discarded" })

	if err := q.ScanDiscarded(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := q.ScanDiscarded(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(logs.String(), "queue.job_discarded"); got != 1 {
		t.Errorf("the discard was logged %d times, want 1: %s", got, logs.String())
	}
	if !strings.Contains(logs.String(), fmt.Sprintf(`"job_id":%d`, id)) {
		t.Errorf("the log does not name job %d: %s", id, logs.String())
	}
	if got := notices.Load(); got != 1 {
		t.Errorf("persistent discard observer ran %d times; want 1", got)
	}
	if got := discardedJobs(t, reader)["test.stuck"]; got != 1 {
		t.Errorf("the discard counter is %d for the job's kind, want 1", got)
	}
}

// TestAnOldDiscardIsReportedOnceAndCleanedAfterRestart: a job discarded before this process started, and older than
// the retention, is reported once when a queue returns, then deleted with its claim; a second queue reports nothing.
func TestAnOldDiscardIsReportedOnceAndCleanedAfterRestart(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO river.river_job
			(kind, args, state, queue, max_attempts, attempt, attempted_at, finalized_at)
			VALUES ('test.old', '{}', 'discarded', 'default', 1, 1, now() - interval '25 hours',
				now() - interval '25 hours')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	first := sdkmetric.NewManualReader()
	firstLogs := &lockedBuffer{}
	q1 := newDiscardQueue(db, first, firstLogs)
	startDiscardQueue(t, q1)
	if err := q1.ScanDiscarded(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(firstLogs.String(), "queue.job_discarded"); got != 1 {
		t.Fatalf("the first scan logged the discard %d times, want 1: %s", got, firstLogs.String())
	}
	if got := discardedJobs(t, first)["test.old"]; got != 1 {
		t.Errorf("the first scan counted the discard %d times, want 1", got)
	}
	if got := jobCount(t, db, "test.old"); got != 0 {
		t.Errorf("%d old discarded rows are left after the scan, want 0", got)
	}
	stopDiscardQueue(t, q1)

	second := sdkmetric.NewManualReader()
	secondLogs := &lockedBuffer{}
	q2 := newDiscardQueue(db, second, secondLogs)
	startDiscardQueue(t, q2)
	if err := q2.ScanDiscarded(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(secondLogs.String(), "queue.job_discarded"); got != 0 {
		t.Errorf("a restart logged the discard %d times, want 0: %s", got, secondLogs.String())
	}
	if got := discardedJobs(t, second)["test.old"]; got != 0 {
		t.Errorf("a restart counted the discard %d times, want 0", got)
	}
	stopDiscardQueue(t, q2)
}

// newDiscardQueue makes a queue whose logs and counter the test can read.
func newDiscardQueue(db *pg.DB, reader *sdkmetric.ManualReader, logs *lockedBuffer) *pg.Queue {
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	return db.NewQueue(logger, platform, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"))
}

// startDiscardQueue starts q and stops it when the test ends.
func startDiscardQueue(t *testing.T, q *pg.Queue) {
	t.Helper()
	if err := q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopDiscardQueue(t, q) })
}

// stopDiscardQueue stops q within a deadline; stopping twice does nothing.
func stopDiscardQueue(t *testing.T, q *pg.Queue) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
	defer cancel()
	if err := q.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// jobState is the state of the latest job of kind, or "" when it has no row yet.
func jobState(t *testing.T, db *pg.DB, kind string) string {
	t.Helper()
	var state string
	err := db.ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT state FROM river.river_job WHERE kind = $1 ORDER BY id DESC LIMIT 1", kind).Scan(&state)
	})
	if errors.Is(err, pg.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// jobCount is how many job rows of kind exist, including discarded ones the scan has not cleaned yet.
func jobCount(t *testing.T, db *pg.DB, kind string) int {
	t.Helper()
	var n int
	err := db.ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM river.river_job WHERE kind = $1", kind).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// discardedJobs is the discard counter by job kind, as reader has collected it.
func discardedJobs(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "axiom.pg.queue.discarded_jobs" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T, want an int64 sum", m.Name, m.Data)
			}
			for _, point := range sum.DataPoints {
				kind, _ := point.Attributes.Value(attribute.Key("kind"))
				counts[kind.AsString()] += point.Value
			}
		}
	}
	return counts
}

// eventually waits up to 30 seconds for cond, so a busy machine fails a test rather than hanging it.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
