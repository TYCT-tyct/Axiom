package gateway

import (
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"axiom/internal/platform/telemetry"
)

// BenchmarkRefused is what counting a 429 costs on the call path, with a recording span in the context as there is on a
// public request. The rule (internal/platform/README.md「遥测」): a measurement allocates nothing once the reason has
// been seen, so a node refusing a flood does not also fill the heap. bench-compare fails if it starts to.
func BenchmarkRefused(b *testing.B) {
	tel, err := telemetry.New(sdktrace.NewSimpleSpanProcessor(tracetest.NewNoopExporter())) // a processor makes the span sampled
	if err != nil {
		b.Fatal(err)
	}
	bp, err := New(DefaultConfig(), tel.Metrics.Meter("axiom/bench"))
	if err != nil {
		b.Fatal(err)
	}
	ctx, span := tel.Traces.Tracer("axiom/bench").Start(b.Context(), "request")
	defer span.End()
	bp.refused(ctx, errIPInFlight) // the first count of a reason makes its series
	b.ReportAllocs()
	for b.Loop() {
		bp.refused(ctx, errIPInFlight)
	}
}
