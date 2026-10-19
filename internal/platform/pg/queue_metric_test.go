package pg_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/telemetry"
)

// TestTheOldestWaitingJobOfEachQueueIsMeasured: the queue exports how long each queue's oldest available job has been
// ready to run, so an alert can tell which queue's workers fall behind. The jobs are written ready in the past, so the
// measured wait follows from the rows and not from how long the test takes, and a queue with nothing waiting reads 0.
func TestTheOldestWaitingJobOfEachQueueIsMeasured(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	_ = db.NewQueue(slog.New(slog.DiscardHandler), platform, tel.Metrics.Meter("axiom/pg"))
	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
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

	waits := readOldestWaits(t, tel)
	if got := waits[pg.QueueMoney]; got < 90 || got > 120 {
		t.Errorf("the money queue's oldest wait is %v seconds, want about 90", got)
	}
	if got := waits[pg.QueueArchive]; got < 10 || got > 60 {
		t.Errorf("the archive queue's oldest wait is %v seconds, want about 10", got)
	}
	for _, empty := range []string{pg.QueueNotifications, pg.QueueSyncs} {
		if got, ok := waits[empty]; !ok || got != 0 {
			t.Errorf("the %s queue, with nothing waiting, reads %v (present: %v), want 0", empty, got, ok)
		}
	}
}

// readOldestWaits is the queue's oldest wait gauge by queue, read from the process's metrics text under the Prometheus name.
func readOldestWaits(t *testing.T, tel *telemetry.Telemetry) map[string]float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	tel.MetricsHandler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	const name = "axiom_pg_queue_oldest_job_wait_seconds"
	waits := map[string]float64{}
	for line := range strings.Lines(rec.Body.String()) {
		rest, ok := strings.CutPrefix(line, name+`{queue="`)
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
		t.Fatalf("the metrics lack %s:\n%s", name, rec.Body.String())
	}
	return waits
}
