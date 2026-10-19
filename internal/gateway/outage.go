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
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

// The dependencies whose outage is told. A request that gets through ends the outage of the ones it used, no more: a
// listing of the models uses the database, the site snapshot and the catalog, but not routing.
const (
	depDatabase = "database" // the database cannot be reached
	depAccounts = "accounts" // the site snapshot is older than it may be
	depCatalog  = "catalog"  // the catalog snapshot is missing or older than it may be
	depRouting  = "routing"  // the routing snapshot is older than it may be
)

// outageOf says whether err is a dependency being out, and why. Every request meets the same outage until it ends, so
// the log tells it once (outage.refuse); the reason is a fixed text, as pg.Unavailable's is, never err's own.
func outageOf(err error) (dep, reason string, ok bool) {
	if reason, ok := pg.Unavailable(err); ok {
		return depDatabase, reason, true
	}
	for _, stale := range []struct {
		dep string
		err error
	}{{depAccounts, accounts.ErrStale}, {depCatalog, catalog.ErrStale}, {depRouting, routing.ErrStale}} {
		if errors.Is(err, stale.err) {
			return stale.dep, stale.err.Error(), true
		}
	}
	return "", "", false
}

// outage tells dependency outages as changes of state: one line when a dependency's outage begins or its reason
// changes, one when it ends, with the count of the requests it refused in between in the second line and, as it goes,
// in a metric. Without it a database that is down logs a line per request, in proportion to the traffic (#294), and
// drowns the one line that matters.
//
// Invariants: a dependency is in down exactly while its outage is told; active is the size of down; a request is
// counted in the metric whether or not it is logged; a new reason during an outage ends the told one and begins
// another.
type outage struct {
	logger  *slog.Logger
	refusal map[string]metric.MeasurementOption // the counter's attributes per dependency, built once so that counting allocates nothing
	count   metric.Int64Counter
	active  atomic.Int32 // the fast path of recovered: a request that finds no outage does not take mu

	mu   sync.Mutex
	down map[string]*standing
}

// standing is an outage that has been told.
type standing struct {
	reason  string
	since   time.Time
	refused int64
}

func newOutage(logger *slog.Logger, meter metric.Meter) *outage {
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("gateway")
	}
	count, err := meter.Int64Counter("axiom.gateway.outage.refusals",
		metric.WithDescription("Requests refused because a dependency was out, by dependency (database, accounts, catalog, routing)."))
	if err != nil {
		panic(err) // the instrument name is a constant
	}
	o := &outage{logger: logger, count: count, refusal: map[string]metric.MeasurementOption{}, down: map[string]*standing{}}
	for _, dep := range []string{depDatabase, depAccounts, depCatalog, depRouting} {
		o.refusal[dep] = metric.WithAttributeSet(attribute.NewSet(attribute.String("dependency", dep)))
	}
	return o
}

// refuse counts a request refused because dep is out for reason, and tells the outage if it is not the one told.
func (o *outage) refuse(ctx context.Context, dep, reason string) {
	o.count.Add(ctx, 1, o.refusal[dep])
	o.mu.Lock()
	defer o.mu.Unlock()
	s := o.down[dep]
	if s != nil && s.reason == reason {
		s.refused++
		return
	}
	if s != nil {
		o.end(dep, s, "reason changed")
	} else {
		o.active.Add(1)
	}
	o.down[dep] = &standing{reason: reason, since: time.Now(), refused: 1}
	o.logger.Error("gateway: a dependency is unavailable; requests that need it are refused until it is back",
		"dependency", dep, "reason", reason)
}

// recovered is called when a request has used dep successfully: if its outage was told, it is over.
func (o *outage) recovered(dep string) {
	if o.active.Load() == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if s := o.down[dep]; s != nil {
		o.end(dep, s, "recovered")
		delete(o.down, dep)
		o.active.Add(-1)
	}
}

// end tells how an outage ended and what it cost. The caller holds mu.
func (o *outage) end(dep string, s *standing, how string) {
	o.logger.Warn("gateway: a dependency outage ended", "dependency", dep, "reason", s.reason, "how", how,
		"refused", s.refused, "duration", time.Since(s.since).Round(time.Second).String())
}
