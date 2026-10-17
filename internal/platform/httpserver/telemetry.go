package httpserver

import (
	"context"
	"maps"
	"net/http"
	"sync"
	"sync/atomic"
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
	series   *seriesCache
}

// labels is one combination of the metrics' labels. The routes are the registered patterns plus "unmatched", so there
// are few of them (the cap in internal/platform/README.md「遥测」).
type labels struct{ route, class, outcome string }

// seriesCache holds each combination's measurement options, made once: building them for every request allocates,
// and the metrics must not (BenchmarkRequestTelemetry). Reads are one atomic load; the map is replaced, never
// changed, when a combination is first seen.
type seriesCache struct {
	mu   sync.Mutex
	seen atomic.Pointer[map[labels]series]
}

type series struct {
	add    []metric.AddOption
	record []metric.RecordOption
}

func newSeriesCache() *seriesCache {
	c := new(seriesCache)
	c.seen.Store(&map[labels]series{})
	return c
}

func (c *seriesCache) get(l labels) series {
	if s, ok := (*c.seen.Load())[l]; ok {
		return s
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	known := *c.seen.Load()
	if s, ok := known[l]; ok {
		return s
	}
	opt := metric.WithAttributeSet(attribute.NewSet(attribute.String("route", l.route),
		attribute.String("status_class", l.class), attribute.String("outcome", l.outcome)))
	s := series{[]metric.AddOption{opt}, []metric.RecordOption{opt}}
	next := maps.Clone(known)
	next[l] = s
	c.seen.Store(&next)
	return s
}

var statusClasses = [...]string{"none", "1xx", "2xx", "3xx", "4xx", "5xx", "6xx", "7xx", "8xx", "9xx"}

// statusClass names the class of a status; none when no status was sent. net/http sends only 100 to 999, but the
// recorder notes a status before net/http refuses it, so any number can arrive.
func statusClass(status int) string {
	if status < 100 || status > 999 {
		return statusClasses[0]
	}
	return statusClasses[status/100]
}

// serverSpan is the options of the request's span, shared so that no slice is made for each.
var serverSpan = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindServer)}

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
	return Telemetry{tp.Tracer("axiom/httpserver"), requests, duration, newSeriesCache()}, nil
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
func (t Telemetry) start(ctx context.Context, requestID string) (context.Context, func(route string, status int, aborted bool, elapsed time.Duration)) {
	if t.tracer == nil {
		return ctx, func(string, int, bool, time.Duration) {}
	}
	ctx, span := t.tracer.Start(ctx, "http.request", serverSpan...)
	if span.IsRecording() {
		span.SetAttributes(attribute.String("request_id", requestID))
	}
	return ctx, func(route string, status int, aborted bool, elapsed time.Duration) {
		if route == "" {
			route = "unmatched" // a pattern is a registered one; whatever else was asked is one label
		}
		class, outcome := statusClass(status), "completed" // none: the connection ended before any status was sent
		if aborted {
			outcome = "aborted" // the status may be a 200 already sent: the response is still broken
		}
		s := t.series.get(labels{route, class, outcome})
		t.requests.Add(ctx, 1, s.add...)
		t.duration.Record(ctx, elapsed.Seconds(), s.record...)
		if span.IsRecording() {
			span.SetName(route)
			span.SetAttributes(attribute.Int("http.response.status_code", status), attribute.String("outcome", outcome))
			if aborted || status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, outcome+" "+class)
			}
		}
		span.End()
	}
}
