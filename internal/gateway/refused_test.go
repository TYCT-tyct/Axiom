package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/protocol"
)

// countingMeter is a meter whose 429 counter the test can read: the count by reason.
func countingMeter(ctx context.Context, t testing.TB) (*sdkmetric.MeterProvider, func() map[string]int64) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.WithoutCancel(ctx)) }) //nolint:errcheck // the test is over
	return provider, func() map[string]int64 {
		var data metricdata.ResourceMetrics
		if err := reader.Collect(ctx, &data); err != nil {
			t.Fatal(err)
		}
		return rateLimitedByReason(data)
	}
}

func rateLimitedByReason(data metricdata.ResourceMetrics) map[string]int64 {
	counts := map[string]int64{}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok || m.Name != "axiom.gateway.rate_limited" {
				continue
			}
			for _, point := range sum.DataPoints {
				reason, _ := point.Attributes.Value("reason")
				counts[reason.AsString()] += point.Value
			}
		}
	}
	return counts
}

// wantRefused checks the count of 429s by reason: exactly these, none under another reason.
func wantRefused(t testing.TB, got map[string]int64, want map[string]int64) {
	t.Helper()
	for reason, n := range want {
		if got[reason] != n {
			t.Errorf("429s counted as %q: %d, want %d (all: %v)", reason, got[reason], n, got)
		}
	}
	for reason, n := range got {
		if _, ok := want[reason]; !ok {
			t.Errorf("%d 429s counted as %q that the test did not expect (all: %v)", n, reason, got)
		}
	}
}

// TestEachRateLimitIsCountedByItsReason: every in-process limit that answers 429 is produced by its own primitive and
// goes out through the error exit, which counts it by the limit that fired and answers the one code the docs give
// (docs/domains/gateway.md「错误」). The limits that need the database (the key's rate, the count's rate, the
// credentials) are asserted in the tests that exercise them.
func TestEachRateLimitIsCountedByItsReason(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QueueMax, cfg.QueuePerKey = 2, 1
	for reason, produce := range map[string]func(t *testing.T, ctx context.Context, bp *Backpressure) error{
		"ip_in_flight": func(_ *testing.T, _ context.Context, bp *Backpressure) error {
			_, err := bp.IPCalls.Acquire("ip", 0)
			return err
		},
		"account_in_flight": func(_ *testing.T, _ context.Context, bp *Backpressure) error {
			if _, err := bp.InFlight.Acquire("a", 1); err != nil {
				return err
			}
			_, err := bp.InFlight.Acquire("a", 1)
			return err
		},
		"account_gate": func(t *testing.T, ctx context.Context, bp *Backpressure) error {
			started, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			go func() {
				item := &gateItem{done: make(chan struct{})}
				//nolint:errcheck,gosec // the holder only keeps the gate busy; the admission behind it is what is counted
				bp.Funds.Complete(ctx, "a", item, func(context.Context, []*gateItem) error {
					close(started)
					<-release
					return nil
				})
			}()
			<-started
			// The admission waits AccountGateWait for the gate the completion holds.
			return submitAdmission(ctx, bp.Funds, "a")
		},
		"queue_key_full": func(t *testing.T, ctx context.Context, bp *Backpressure) error {
			wait(ctx, bp.Queue, "a").mustWait(t)
			_, err := bp.Queue.Wait(ctx, "a", bp.Queue.Epoch(), time.Now().Add(queueWait))
			return err
		},
		"queue_full": func(t *testing.T, ctx context.Context, bp *Backpressure) error {
			wait(ctx, bp.Queue, "a").mustWait(t)
			wait(ctx, bp.Queue, "b").mustWait(t)
			_, err := bp.Queue.Wait(ctx, "c", bp.Queue.Epoch(), time.Now().Add(queueWait))
			return err
		},
		"queue_wait": func(t *testing.T, ctx context.Context, bp *Backpressure) error {
			_, err := bp.Queue.Wait(ctx, "a", bp.Queue.Epoch(), time.Now()) // its time is spent
			return err
		},
	} {
		t.Run(reason, func(t *testing.T) {
			run(t, func(t *testing.T, ctx context.Context) {
				meter, counts := countingMeter(ctx, t)
				bp, err := New(cfg, meter.Meter("test"))
				if err != nil {
					t.Fatal(err)
				}
				h := &Handler{}
				h.Limits, h.Logger = bp, slog.New(slog.DiscardHandler)
				rec := httptest.NewRecorder()
				h.fail(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", nil), protocol.Messages, produce(t, ctx, bp))
				if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), `"error_type":"rate_limited"`) {
					t.Errorf("answered %d %s", rec.Code, rec.Body)
				}
				wantRefused(t, counts(), map[string]int64{reason: 1})
			})
		})
	}
}
