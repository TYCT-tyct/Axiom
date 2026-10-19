package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

// What a refused request was refused for, when the cause is a dependency being out and not the request.
const (
	causeDatabase = "database" // the database cannot be reached
	causeSnapshot = "snapshot" // a snapshot the request needs is older than it may be, so the node cannot serve now
)

// outageOf says whether err is a dependency being out, and why. Every request meets the same outage until it ends, so
// the log tells it once (outage.refuse); the reason is a fixed text, as pg.Unavailable's is, never err's own.
func outageOf(err error) (cause, reason string, ok bool) {
	if reason, ok := pg.Unavailable(err); ok {
		return causeDatabase, reason, true
	}
	for _, stale := range []error{accounts.ErrStale, catalog.ErrStale, routing.ErrStale} {
		if errors.Is(err, stale) {
			return causeSnapshot, stale.Error(), true
		}
	}
	return "", "", false
}

// outage tells a dependency outage as a change of state: once when it begins, once when it ends, with the count of
// the requests it refused in between in the second line and, as it goes, in a metric. Without it a database that is
// down logs a line per request, in proportion to the traffic (#294), and drowns the one line that matters.
//
// Invariants: down is true exactly while cause is not empty; a request is counted in the metric whether or not it is
// logged; a change of cause during an outage is told as a new outage without an end in between.
type outage struct {
	logger  *slog.Logger
	refusal map[string]metric.MeasurementOption // the counter's attributes per cause, built once so that counting allocates nothing
	count   metric.Int64Counter
	down    atomic.Bool // the fast path of recovered: a request that finds no outage does not take mu

	mu      sync.Mutex
	cause   string
	since   time.Time
	refused int64
}

func newOutage(logger *slog.Logger, meter metric.Meter) *outage {
	count, err := meter.Int64Counter("axiom.gateway.outage.refusals",
		metric.WithDescription("Requests refused because a dependency was out, by cause (database, snapshot)."))
	if err != nil {
		panic(err) // the instrument name is a constant
	}
	o := &outage{logger: logger, count: count, refusal: map[string]metric.MeasurementOption{}}
	for _, cause := range []string{causeDatabase, causeSnapshot} {
		o.refusal[cause] = metric.WithAttributeSet(attribute.NewSet(attribute.String("cause", cause)))
	}
	return o
}

// refuse counts a request refused for cause, and tells the outage if it is not the one already told.
func (o *outage) refuse(ctx context.Context, cause, reason string) {
	o.count.Add(ctx, 1, o.refusal[cause])
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cause == cause {
		o.refused++
		return
	}
	if o.cause != "" {
		o.end("changed to " + cause)
	}
	o.cause, o.since, o.refused = cause, time.Now(), 1
	o.down.Store(true)
	o.logger.Error("gateway: a dependency is unavailable; requests are refused until it is back",
		"cause", cause, "reason", reason)
}

// recovered is called when a request gets through admission: if an outage was told, it is over.
func (o *outage) recovered() {
	if !o.down.Load() {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cause != "" {
		o.end("recovered")
		o.cause = ""
		o.down.Store(false)
	}
}

// end tells how the outage in o ended and what it cost. The caller holds mu.
func (o *outage) end(how string) {
	o.logger.Warn("gateway: a dependency outage ended", "cause", o.cause, "how", how,
		"refused", o.refused, "duration", time.Since(o.since).Round(time.Second).String())
}
