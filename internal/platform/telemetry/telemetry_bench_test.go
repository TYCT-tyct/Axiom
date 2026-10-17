package telemetry_test

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"axiom/internal/platform/telemetry"
)

// BenchmarkRecord is what recording a metric on the call path costs, with a recording span in the context as there is
// on a public request (the SDK offers its value to the exemplar reservoir when the span is sampled). The rule
// (internal/platform/README.md「遥测」): a measurement allocates nothing, so a counter with no labels, or with labels
// from options built once, is free for the call path. bench-compare fails if any of these starts to allocate.
func BenchmarkRecord(b *testing.B) {
	tel, err := telemetry.New(sdktrace.NewSimpleSpanProcessor(tracetest.NewNoopExporter())) // a processor makes the span sampled
	if err != nil {
		b.Fatal(err)
	}
	meter := tel.Metrics.Meter("axiom/bench")
	counter, err := meter.Int64Counter("axiom.bench.events")
	if err != nil {
		b.Fatal(err)
	}
	duration, err := meter.Float64Histogram("axiom.bench.duration", metric.WithUnit("s"))
	if err != nil {
		b.Fatal(err)
	}
	ctx, span := tel.Traces.Tracer("axiom/bench").Start(b.Context(), "request")
	defer span.End()
	// Options passed one by one make a slice for each call, because the instrument is an interface; a slice made once
	// does not.
	labelled := metric.WithAttributeSet(attribute.NewSet(attribute.String("route", "GET /v1/models"),
		attribute.String("status_class", "2xx"), attribute.String("outcome", "completed")))
	add, record := []metric.AddOption{labelled}, []metric.RecordOption{labelled}

	b.Run("counter", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			counter.Add(ctx, 1)
		}
	})
	b.Run("counter labelled", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			counter.Add(ctx, 1, add...)
		}
	})
	b.Run("histogram labelled", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			duration.Record(ctx, 0.042, record...)
		}
	})
}
