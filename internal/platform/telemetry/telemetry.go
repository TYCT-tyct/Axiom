// Package telemetry builds the process's metrics and tracing providers (internal/platform/README.md「遥测」). Modules
// receive the providers' interfaces from cmd/axiom and never touch the OpenTelemetry global, so tests that run in
// parallel each hold their own.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Telemetry is the process's providers and the Prometheus view of its metrics.
type Telemetry struct {
	Traces  *sdktrace.TracerProvider
	Metrics *sdkmetric.MeterProvider
	handler http.Handler
}

// New builds the providers. Metrics are kept in a registry of their own, not Prometheus's default one, so only what
// the program registers through Metrics is exposed. Spans go to processors, none yet: an exporter arrives with the
// first deployment that runs a collector, and it is a processor added here. With no processor nothing would read a
// span, so none is sampled, and the spans of httpserver, pg and egress build no attributes for one.
func New(processors ...sdktrace.SpanProcessor) (*Telemetry, error) {
	registry := prometheus.NewRegistry()
	reader, err := otelprom.New(otelprom.WithRegisterer(registry), otelprom.WithoutScopeInfo(), otelprom.WithoutTargetInfo())
	if err != nil {
		return nil, fmt.Errorf("telemetry: metrics: %w", err)
	}
	traceOptions := make([]sdktrace.TracerProviderOption, len(processors), len(processors)+1)
	for i, p := range processors {
		traceOptions[i] = sdktrace.WithSpanProcessor(p)
	}
	if len(processors) == 0 {
		traceOptions = append(traceOptions, sdktrace.WithSampler(sdktrace.NeverSample()))
	}
	return &Telemetry{
		Traces:  sdktrace.NewTracerProvider(traceOptions...),
		Metrics: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		handler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
	}, nil
}

// MetricsHandler serves the metrics in Prometheus's text format. Only the ops listener mounts it.
func (t *Telemetry) MetricsHandler() http.Handler { return t.handler }

// Shutdown flushes and stops the providers; the process calls it last, after the work that reports to them.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	return errors.Join(t.Traces.Shutdown(ctx), t.Metrics.Shutdown(ctx))
}
