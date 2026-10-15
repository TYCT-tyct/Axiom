package httpserver

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Telemetry is what Public reports each request to: a server span carrying the request ID, which the transaction and
// outbound-request spans of the request become children of, and two metrics labelled by route pattern and status
// class only (docs/platform README「遥测」). Nothing from the request's path, query, headers or body goes in: they may
// hold identifiers, keys or conversation text, and as metric labels they would be unbounded.
type Telemetry struct {
	tracer   trace.Tracer
	requests metric.Int64Counter
	duration metric.Float64Histogram
}

// NewTelemetry makes the instruments in the given providers.
func NewTelemetry(tp trace.TracerProvider, mp metric.MeterProvider) (Telemetry, error) {
	meter := mp.Meter("axiom/httpserver")
	requests, err := meter.Int64Counter("axiom.http.server.requests", metric.WithDescription("Public requests served."))
	if err != nil {
		return Telemetry{}, err
	}
	duration, err := meter.Float64Histogram("axiom.http.server.duration", metric.WithUnit("s"),
		// From a probe's few milliseconds to a streamed answer's minutes.
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300),
		metric.WithDescription("Time from a public request's arrival to the end of its response."))
	if err != nil {
		return Telemetry{}, err
	}
	return Telemetry{tp.Tracer("axiom/httpserver"), requests, duration}, nil
}

// Option adjusts Public.
type Option func(*options)

type options struct{ telemetry Telemetry }

func apply(opts []Option) (o options) {
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithTelemetry reports every request to t.
func WithTelemetry(t Telemetry) Option { return func(o *options) { o.telemetry = t } }

// start opens the request's span as a child of whatever ctx carries, and returns the function that closes it and
// counts the request. A zero Telemetry does nothing.
func (t Telemetry) start(ctx context.Context, requestID string) (context.Context, func(route string, status int, elapsed time.Duration)) {
	if t.tracer == nil {
		return ctx, func(string, int, time.Duration) {}
	}
	ctx, span := t.tracer.Start(ctx, "http.request", trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("request_id", requestID)))
	return ctx, func(route string, status int, elapsed time.Duration) {
		if route == "" {
			route = "unmatched" // a pattern is a registered one; whatever else was asked is one label
		}
		class := "aborted"
		if status != 0 {
			class = strconv.Itoa(status/100) + "xx"
		}
		attrs := metric.WithAttributes(attribute.String("route", route), attribute.String("status_class", class))
		t.requests.Add(ctx, 1, attrs)
		t.duration.Record(ctx, elapsed.Seconds(), attrs)
		span.SetName(route)
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status == 0 || status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, class)
		}
		span.End()
	}
}
