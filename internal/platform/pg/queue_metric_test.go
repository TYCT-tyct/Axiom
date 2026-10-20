package pg_test

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/telemetry"
)

const oldestWaitName = "axiom_pg_queue_oldest_job_wait_seconds"

// TestTheOldestWaitingJobOfEachQueueIsMeasured: the queue exports how long each queue's oldest available job has been
// ready to run, so an alert can tell which queue's workers fall behind. Both jobs are written with the same database
// time, so within one collection their waits differ by exactly the 80 seconds between their ready times, whatever the
// test's pace; the absolute values are bounded by the database's own clock read before the jobs and after the
// collection. A queue with nothing waiting reads 0.
func TestTheOldestWaitingJobOfEachQueueIsMeasured(t *testing.T) {
	t.Parallel()
	db, tel := openQueueMetrics(t, pgtest.New(t).App)
	before := databaseSeconds(t, db)
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		// One ready for 90 seconds on the money queue, one ready for 10 on the archive queue, and one scheduled for an
		// hour from now, which is not waiting yet.
		_, err := tx.Exec(t.Context(), `INSERT INTO river.river_job (kind, args, state, queue, max_attempts, scheduled_at)
			VALUES ('test.waiting', '{}', 'available', $1, 1, now() - interval '90 seconds'),
				('test.waiting', '{}', 'available', $2, 1, now() - interval '10 seconds'),
				('test.waiting', '{}', 'scheduled', $1, 1, now() + interval '1 hour')`, pg.QueueMoney, pg.QueueArchive)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	waits := readOldestWaits(t, scrape(t, tel))
	elapsed := databaseSeconds(t, db) - before // the most the jobs' time and the collection's can lie apart
	money, archive := waits[pg.QueueMoney], waits[pg.QueueArchive]
	if got := money - archive; math.Abs(got-80) > 1e-3 {
		t.Errorf("the money queue waited %v seconds longer than the archive queue in one collection, want 80", got)
	}
	if money < 90 || money > 90+elapsed {
		t.Errorf("the money queue's oldest wait is %v seconds, want between 90 and %v", money, 90+elapsed)
	}
	if archive < 10 || archive > 10+elapsed {
		t.Errorf("the archive queue's oldest wait is %v seconds, want between 10 and %v", archive, 10+elapsed)
	}
	for _, empty := range []string{pg.QueueNotifications, pg.QueueSyncs} {
		if got, ok := waits[empty]; !ok || got != 0 {
			t.Errorf("the %s queue, with nothing waiting, reads %v (present: %v), want 0", empty, got, ok)
		}
	}
}

// TestACollectionEndsWhileTheDatabaseBlocksAndRecovers: a lock on the job table that never lifts holds the gauge's
// query, and the exporter collects without a deadline of its own while the SDK holds the pipeline's lock. The queue
// bounds its own database work, so the collection ends and reports no wait, not a 0 that reads as a queue that caught
// up; once the lock lifts the next collection reports again.
func TestACollectionEndsWhileTheDatabaseBlocksAndRecovers(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t)
	_, tel := openQueueMetrics(t, database.App)
	holder, err := pgx.Connect(t.Context(), database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close(context.WithoutCancel(t.Context())) })
	lock, err := holder.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Rollback(context.WithoutCancel(t.Context())) })
	if _, err := lock.Exec(t.Context(), "LOCK TABLE river.river_job IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}

	// A hang is the failure under test: the guard only turns it into a failure sooner than the test binary's timeout,
	// and no passing run depends on it.
	collected := make(chan string, 1)
	go func() { collected <- scrape(t, tel) }()
	select {
	case body := <-collected:
		if strings.Contains(body, oldestWaitName) {
			t.Errorf("a collection that could not read the database reported waits:\n%s", body)
		}
	case <-time.After(time.Minute):
		t.Fatal("the collection did not end while the job table was locked")
	}

	if err := lock.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if waits := readOldestWaits(t, scrape(t, tel)); len(waits) != 4 {
		t.Errorf("after the lock lifted the gauge reports %v, want the four queues", waits)
	}
}

// openQueueMetrics is a database and the telemetry whose metrics a queue on it reports to.
func openQueueMetrics(t *testing.T, appURL string) (*pg.DB, *telemetry.Telemetry) {
	t.Helper()
	db, err := pg.Open(t.Context(), appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	_ = db.NewQueue(slog.New(slog.DiscardHandler), platform, tel.Metrics.Meter("axiom/pg"))
	return db, tel
}

// databaseSeconds is the database's clock now, in seconds.
func databaseSeconds(t *testing.T, db *pg.DB) float64 {
	t.Helper()
	var seconds float64
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT extract(epoch FROM clock_timestamp())::float8").Scan(&seconds)
	})
	if err != nil {
		t.Fatal(err)
	}
	return seconds
}

// scrape is the process's metrics text.
func scrape(t *testing.T, tel *telemetry.Telemetry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	tel.MetricsHandler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// readOldestWaits is the queue's oldest wait gauge by queue, read from the metrics text under the Prometheus name.
func readOldestWaits(t *testing.T, body string) map[string]float64 {
	t.Helper()
	waits := map[string]float64{}
	for line := range strings.Lines(body) {
		rest, ok := strings.CutPrefix(line, oldestWaitName+`{queue="`)
		if !ok {
			continue
		}
		queue, value, ok := strings.Cut(rest, `"} `)
		if !ok {
			t.Fatalf("the metric line %q names no queue or value", line)
		}
		seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			t.Fatalf("the metric line %q is no number: %v", line, err)
		}
		waits[queue] = seconds
	}
	if len(waits) == 0 {
		t.Fatalf("the metrics lack %s:\n%s", oldestWaitName, body)
	}
	return waits
}
