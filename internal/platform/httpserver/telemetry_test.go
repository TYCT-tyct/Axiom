package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"axiom/internal/platform/telemetry"
)

// TestAbortedResponseIsRecordedAsAborted: a handler that has sent 200 and then aborts leaves a broken response, which
// telemetry reports as aborted (an error span and its own outcome) while it keeps the status that was sent; a handler
// that finishes is completed.
func TestAbortedResponseIsRecordedAsAborted(t *testing.T) {
	t.Parallel()
	spans := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	reader := sdkmetric.NewManualReader()
	metrics := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	tel, err := NewTelemetry(traces, metrics)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		http.NewResponseController(w).Flush() //nolint:errcheck,gosec // the test only needs the status to be out
		panic(http.ErrAbortHandler)
	})
	mux.HandleFunc("GET /fine", func(http.ResponseWriter, *http.Request) {})
	node := httptest.NewServer(Public(mux, slog.New(slog.DiscardHandler), time.Minute, WithTelemetry(tel)))
	defer node.Close()
	for _, path := range []string{"/stream", "/fine"} {
		resp, err := http.Get(node.URL + path) //nolint:noctx // a local test server
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck,gosec // the stream breaks off by design
		resp.Body.Close()              //nolint:errcheck,gosec // as above
	}

	seen := requestCounts(t, reader)
	for _, want := range []string{"GET /stream aborted 2xx", "GET /fine completed 2xx"} {
		if seen[want] != 1 {
			t.Errorf("requests counted %v, want one %q", seen, want)
		}
	}
	for _, s := range spans.Ended() {
		wantError := s.Name() == "GET /stream"
		if (s.Status().Code == codes.Error) != wantError {
			t.Errorf("span %s has status %v, error expected: %v", s.Name(), s.Status().Code, wantError)
		}
	}
}

// requestCounts reads the request counter as "route outcome status_class" to count.
func requestCounts(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	for _, sm := range data.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok || m.Name != "axiom.http.server.requests" {
				continue
			}
			for _, dp := range sum.DataPoints {
				route, _ := dp.Attributes.Value("route")
				outcome, _ := dp.Attributes.Value("outcome")
				class, _ := dp.Attributes.Value("status_class")
				seen[route.AsString()+" "+outcome.AsString()+" "+class.AsString()] = dp.Value
			}
		}
	}
	return seen
}

// BenchmarkRequestTelemetry is what reporting one request costs: "off" is a zero Telemetry, "on" the providers the
// node builds (no span processor, as serve has none yet). The difference is the call path's price for the span and
// the two metrics; bench-compare fails if it grows.
func BenchmarkRequestTelemetry(b *testing.B) {
	node, err := telemetry.New()
	if err != nil {
		b.Fatal(err)
	}
	on, err := NewTelemetry(node.Traces, node.Metrics)
	if err != nil {
		b.Fatal(err)
	}
	for name, tel := range map[string]Telemetry{"off": {}, "on": on} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, done := tel.start(b.Context(), "0123456789abcdef")
				done("GET /v1/models", http.StatusOK, false, 42*time.Millisecond)
			}
		})
	}
}
