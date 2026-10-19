package suppliers

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"axiom/internal/platform/pg"
)

type reconciliationLogs struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *reconciliationLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *reconciliationLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Pagination is persisted on the queue, so the first old unmatched attempt
// cannot hide those on later pages. A full last page terminates with an empty page.
func TestReconcileQueueReachesEveryUnmatchedAttempt(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	attempts := []AttemptRecord{
		w.attempt(newRequest(), 1, "missing-1"), w.attempt(newRequest(), 1, "missing-2"), w.attempt(newRequest(), 1, "missing-3"),
	}
	w.record(attempts...)
	w.clock.advance(time.Hour)
	logs := &reconciliationLogs{}
	q := w.db.NewQueue(slog.New(slog.DiscardHandler), platformScope, nil)
	r := &reconcileJob{
		deps:   ReconcileJobConfig{DB: w.db, Scope: platformScope, Queue: q, Logger: slog.New(slog.NewTextHandler(logs, nil)), Clock: w.clock.now},
		policy: DefaultReconcileConfig(),
	}
	r.policy.Batch = 1
	river.AddWorker(q.Workers(), river.WorkFunc(r.work))
	q.Every(time.Hour, func() river.JobArgs { return reconcileArgs{} })
	startReconcileQueue(t, q)
	w.waitReconciliationJobs(4)
	for _, a := range attempts {
		if n := strings.Count(logs.String(), a.Request.String()); n != 1 {
			t.Errorf("attempt %s was reported %d times: %s", a.Request, n, logs.String())
		}
	}
	if n := w.count("SELECT count(*) FROM suppliers.attempt_costs WHERE state = 'pending'"); n != 3 {
		t.Errorf("%d attempts pending; a missing charge must not change them", n)
	}
}

func startReconcileQueue(t *testing.T, q *pg.Queue) {
	t.Helper()
	if err := q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := q.Stop(ctx); err != nil {
			t.Errorf("stop queue: %v", err)
		}
	})
}

func (w *world) waitReconciliationJobs(want int) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.t.Context(), 30*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if w.count("SELECT count(*) FROM river.river_job WHERE kind = 'suppliers.reconcile' AND state = 'completed'") == want {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			w.t.Fatalf("wanted %d completed reconciliation pages", want)
		}
	}
}

// Losing the continuation enqueue rolls back the matches too; no page of the
// scan is silently skipped and no alert escapes a known failure before reporting.
func TestReconcileJobRollsBackWhenItCannotQueueTheNextPage(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.record(w.attempt(newRequest(), 1, newAPIRef), w.attempt(newRequest(), 1, "missing"))
	w.ingest(w.charge(newAPIRef, "400", w.clock.now()))
	w.clock.advance(time.Hour)
	var logs bytes.Buffer
	r := w.reconciliation(&logs)
	r.policy.Batch = 1
	r.deps.Queue = w.db.NewQueue(slog.New(slog.DiscardHandler), platformScope, nil) // deliberately not started
	if err := r.work(t.Context(), &river.Job[reconcileArgs]{}); err == nil {
		t.Fatal("a missing continuation was ignored")
	}
	if got := w.chargeOf(newAPIRef); got != (chargeState{State: "new", Revision: 1}) || w.events(newAPIRef) != 0 {
		t.Fatalf("failed job left state %+v, events %d", got, w.events(newAPIRef))
	}
	if n := w.count("SELECT count(*) FROM suppliers.endpoints WHERE cost_state = 'known'"); n != 1 || logs.Len() != 0 {
		t.Fatalf("failed job delisted or alerted: known %d, logs %s", n, logs.String())
	}
}
