package kv

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// The instruments of README「指标与告警」. Names are the module's, so an alert selects axiom.kv.*; the rules themselves
// come with the alerting output (#316).
const (
	callsMetric      = "axiom.kv.calls"
	droppedMetric    = "axiom.kv.dropped"
	durationMetric   = "axiom.kv.duration"
	breakerMetric    = "axiom.kv.breaker_open"
	memoryMetric     = "axiom.kv.memory_used_bytes"
	oomMetric        = "axiom.kv.rejected_oom"
	skewMetric       = "axiom.kv.clock_skew_seconds"
	nodesMetric      = "axiom.kv.nodes_live"
	callsDescription = "Accesses of the shared state, by use and result."
)

// resultNames are the metric's result label, by outcome. The four the README lists are what a caller sees; a timeout is
// an unavailable access, since the caller falls back either way.
var resultNames = [outcomes]string{ok: "ok", unavailable: "unavailable", timedOut: "unavailable", failed: "error", skipped: "skipped"}

// dropReason is why a sticky write was discarded before sending (README「指标与告警」).
type dropReason string

// The reasons for a discarded write, in the order the metrics prebuild their options.
const (
	dropQueue   dropReason = "queue"
	dropMemory  dropReason = "memory"
	dropAccount dropReason = "account"
)

// dropReasons is every reason, so a reason the metrics do not know cannot compile.
var dropReasons = [...]dropReason{dropQueue, dropMemory, dropAccount}

// metrics is the client's instruments, with the option slices built once: passing attributes one by one allocates a
// slice on every access, and the precheck is on the first token's path (internal/gateway's BenchmarkPrecheck).
type metrics struct {
	calls       metric.Int64Counter
	duration    metric.Float64Histogram
	breakerOpen metric.Int64Gauge
	memory      metric.Int64Gauge
	oom         metric.Int64Counter
	skew        metric.Float64Gauge
	nodes       metric.Int64Gauge
	callOpts    [len(uses)][outcomes]metric.MeasurementOption
	useOpts     [len(uses)]metric.MeasurementOption
	useIndex    map[Use]int
	dropped     metric.Int64Counter
	dropOpts    map[dropReason]metric.MeasurementOption
}

// newMetrics builds the instruments, writing nowhere when meter is nil.
func newMetrics(meter metric.Meter) metrics {
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("kv")
	}
	m := metrics{
		calls:       must(meter.Int64Counter(callsMetric, metric.WithDescription(callsDescription))),
		breakerOpen: must(meter.Int64Gauge(breakerMetric, metric.WithDescription("1 while the breaker refuses every access, 0 while it lets them through."))),
		memory:      must(meter.Int64Gauge(memoryMetric, metric.WithDescription("Memory the server reports as used, in bytes."))),
		oom:         must(meter.Int64Counter(oomMetric, metric.WithDescription("Commands the server refused because its memory is full."))),
		skew:        must(meter.Float64Gauge(skewMetric, metric.WithDescription("Seconds the local clock is ahead of the server's."))),
		nodes:       must(meter.Int64Gauge(nodesMetric, metric.WithDescription("Core nodes the heartbeat sees alive."))),
		useIndex:    make(map[Use]int, len(uses)),
		dropped:     must(meter.Int64Counter(droppedMetric, metric.WithDescription("Sticky writes discarded before sending."))),
		dropOpts:    make(map[dropReason]metric.MeasurementOption, len(dropReasons)),
	}
	for _, reason := range dropReasons {
		m.dropOpts[reason] = metric.WithAttributes(attribute.String("reason", string(reason)))
	}
	m.duration = must(meter.Float64Histogram(durationMetric,
		metric.WithDescription("How long an access of the shared state took, including a timeout."),
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(0.0005, 0.001, 0.002, 0.005, 0.01, 0.02, 0.05)))
	for i, use := range uses {
		m.useIndex[use] = i
		m.useOpts[i] = metric.WithAttributes(attribute.String("use", string(use)))
		for o := range outcomes {
			m.callOpts[i][o] = metric.WithAttributes(
				attribute.String("use", string(use)), attribute.String("result", resultNames[o]))
		}
	}
	return m
}

func (m metrics) drop(ctx context.Context, reason dropReason, n int64) {
	m.dropped.Add(ctx, n, m.dropOpts[reason])
}

// must is the instruments' error, which only a name that is not a constant can cause.
func must[I any](instrument I, err error) I {
	if err != nil {
		panic(err)
	}
	return instrument
}

// recordCall counts one access. It runs on the caller's context, which a deadline of ours has not ended, so the count is
// not dropped when the access was.
func (m metrics) recordCall(ctx context.Context, use Use, o outcome) {
	m.calls.Add(ctx, 1, m.callOpts[m.useIndex[use]][o])
}

// recordBreaker writes the breaker's state, once per change: a gauge written on every access would be a write per call
// with nothing added.
func (m metrics) recordBreaker(ctx context.Context, open bool) {
	m.breakerOpen.Record(ctx, boolInt(open))
}

// useOption is the duration histogram's label for one use.
func (m metrics) useOption(use Use) metric.MeasurementOption { return m.useOpts[m.useIndex[use]] }

// boolInt is a condition as a gauge writes it.
func boolInt(condition bool) int64 {
	if condition {
		return 1
	}
	return 0
}
