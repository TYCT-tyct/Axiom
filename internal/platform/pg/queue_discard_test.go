package pg_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// discarded is a job whose worker always fails, so River discards it on its only attempt.
type discarded struct{}

func (discarded) Kind() string { return "test.discarded" }

func (discarded) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 1} }

type discarder struct {
	river.WorkerDefaults[discarded]
}

func (*discarder) Work(context.Context, *river.Job[discarded]) error {
	return errors.New("the work failed")
}

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

// TestADiscardedJobIsLoggedAndCounted: a job whose last attempt fails is what River discards, and the queue reports
// it under the stable event name and in the metric the alerting docs name.
func TestADiscardedJobIsLoggedAndCounted(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	reader := sdkmetric.NewManualReader()
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	q := db.NewQueue(logger, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"))
	river.AddWorker(q.Workers(), &discarder{})
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

	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := q.InsertManyTx(t.Context(), tx, []river.InsertManyParams{{Args: discarded{}}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the job to be discarded", func() bool { return jobState(t, db, "test.discarded") == "discarded" })

	if got := logs.String(); !strings.Contains(got, `"msg":"queue.job_discarded"`) ||
		!strings.Contains(got, `"level":"ERROR"`) || !strings.Contains(got, `"kind":"test.discarded"`) {
		t.Errorf("the discard is not the error log alerts watch: %s", got)
	}
	if got := discardedJobs(t, reader)["test.discarded"]; got != 1 {
		t.Errorf("the discard counter is %d for the job's kind, want 1", got)
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

// eventually waits up to 20 seconds for cond, so a busy machine fails a test rather than hanging it.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
