package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func queuedWebhook(t *testing.T, db *pg.DB, now time.Time) uuid.UUID {
	t.Helper()
	persistAlert(t, db, "1", false)
	events := alertCycle(t, db, now)
	id := events[0].Receipts[0].ID
	alertTx(t, db, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE observability.alert_receipts SET state='queued',payload='{}' WHERE id=$1`, id)
		return err
	})
	return id
}

func TestWebhookExhaustionCompletesWithFailedReceipt(t *testing.T) {
	db, database := openDBWithURL(t)
	keys := webhookTestKeys(t)
	clock := &webhookClock{at: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	exit := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	defer exit.CloseIdleConnections()
	configureWebhook(t, db, server.URL, keys, clock.Now())
	id := queuedWebhook(t, db, clock.Now())
	worker := &webhookWorker{alertWorker: &alertWorker{db: db, scope: platformScope, now: clock.Now}, keys: keys, exit: exit}
	var row *rivertype.JobRow
	for attempt := 1; attempt <= 5; attempt++ {
		result, err := pgtest.WorkJob(t, database, river.WorkFunc(worker.work), webhookArgs{Receipt: id}, row, clock)
		row = result.Job
		if attempt < 5 && err == nil {
			t.Fatal("transient failure did not retry")
		}
		if attempt == 5 && (err != nil || row.State != rivertype.JobStateCompleted) {
			t.Fatalf("handled exhaustion was discarded: %v %+v", err, row)
		}
		clock.at = row.ScheduledAt.Add(time.Minute)
	}
	assertWebhookReceiptState(t, db, id, "failed")
	alertTx(t, db, func(tx pg.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='queue.discarded'`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("handled failure emitted queue.discarded")
		}
		return nil
	})
}

func TestWebhookPermanentFailureDoesNotUseRetries(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing key", true: "invalid destination"}[invalid], func(t *testing.T) {
			db := openDB(t)
			keys := webhookTestKeys(t)
			now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
			configureWebhook(t, db, "https://receiver.example/hook", keys, now)
			id := queuedWebhook(t, db, now)
			alertTx(t, db, func(tx pg.Tx) error {
				if invalid {
					_, err := tx.Exec(t.Context(), `UPDATE observability.alert_receipts SET channel=jsonb_set(channel,'{webhook_url}','"http://receiver.example"') WHERE id=$1`, id)
					return err
				}
				_, err := tx.Exec(t.Context(), `DELETE FROM observability.webhook_keys`)
				return err
			})
			worker := &webhookWorker{alertWorker: &alertWorker{db: db, scope: platformScope, now: func() time.Time { return now }}, keys: keys}
			if err := worker.work(t.Context(), &river.Job[webhookArgs]{JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 5}, Args: webhookArgs{Receipt: id}}); err != nil {
				t.Fatal(err)
			}
			assertWebhookReceiptState(t, db, id, "failed")
		})
	}
}

// The response has already arrived when the trace ends. Expire its request context at that exact boundary,
// independently of real network speed, so a successful receipt must use the outer job context.
type expireResponse struct {
	*tracetest.SpanRecorder
	requestDone <-chan struct{}
}

func (s *expireResponse) OnStart(ctx context.Context, span sdktrace.ReadWriteSpan) {
	s.requestDone = ctx.Done()
	s.SpanRecorder.OnStart(ctx, span)
}

func (s *expireResponse) OnEnd(span sdktrace.ReadOnlySpan) {
	<-s.requestDone
	s.SpanRecorder.OnEnd(span)
}

func TestWebhookResponseDeadlineDoesNotCancelReceipt(t *testing.T) {
	db := openDB(t)
	keys := webhookTestKeys(t)
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	processor := &expireResponse{SpanRecorder: tracetest.NewSpanRecorder()}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(processor))
	exit := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()), egress.WithTracerProvider(tp))
	defer exit.CloseIdleConnections()
	configureWebhook(t, db, server.URL, keys, now)
	id := queuedWebhook(t, db, now)
	worker := &webhookWorker{alertWorker: &alertWorker{db: db, scope: platformScope, now: func() time.Time { return now }}, keys: keys, exit: exit}
	synctest.Test(t, func(t *testing.T) {
		if err := worker.work(t.Context(), &river.Job[webhookArgs]{JobRow: &rivertype.JobRow{Attempt: 5, MaxAttempts: 5}, Args: webhookArgs{Receipt: id}}); err != nil {
			t.Fatalf("response accepted but receipt lost: %v", err)
		}
	})
	if len(processor.Ended()) != 1 {
		t.Fatal("deadline boundary was not exercised")
	}
	assertWebhookReceiptState(t, db, id, "delivered")
}

func assertWebhookReceiptState(t *testing.T, db *pg.DB, id uuid.UUID, want string) {
	t.Helper()
	alertTx(t, db, func(tx pg.Tx) error {
		var state string
		if err := tx.QueryRow(t.Context(), `SELECT state FROM observability.alert_receipts WHERE id=$1`, id).Scan(&state); err != nil {
			return err
		}
		if state != want {
			t.Fatalf("receipt %s, want %s", state, want)
		}
		return nil
	})
}
