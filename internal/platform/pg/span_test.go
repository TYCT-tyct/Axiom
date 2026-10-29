package pg

import (
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"axiom/internal/platform/telemetry"
)

// BenchmarkTransactionSpan is what the db.tx span costs for one transaction: "unsampled" is the provider serve builds
// while no exporter is attached (telemetry.New samples nothing), "sampled" the same with a span processor, as when an
// exporter is. An unsampled span records nothing, so it builds no attributes; bench-compare fails if either grows.
func BenchmarkTransactionSpan(b *testing.B) {
	for name, processors := range map[string][]sdktrace.SpanProcessor{
		"unsampled": nil,
		"sampled":   {sdktrace.NewSimpleSpanProcessor(tracetest.NewNoopExporter())},
	} {
		node, err := telemetry.New(processors...)
		if err != nil {
			b.Fatal(err)
		}
		db := &DB{tracer: node.Traces.Tracer("axiom/pg")}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, span := startSpan(b.Context(), db.tracer)
				span.End()
			}
		})
	}
}
