package catalog

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/metric"

	"axiom/internal/platform/pg"
)

const (
	syncAgeMetric      = "axiom.catalog.sync.last_success_age"
	syncFailuresMetric = "axiom.catalog.sync.consecutive_failures"
	syncObserveTimeout = 2 * time.Second
)

func (j *syncJob) registerStatusMetrics() error {
	age, err := j.meter.Float64ObservableGauge(syncAgeMetric, metric.WithUnit("s"),
		metric.WithDescription("Seconds since the last complete price sync, or the first failure if none succeeded."))
	if err != nil {
		return fmt.Errorf("catalog: register sync age gauge: %w", err)
	}
	failures, err := j.meter.Int64ObservableGauge(syncFailuresMetric,
		metric.WithDescription("Consecutive failed price sync rounds, read from the persistent record."))
	if err != nil {
		return fmt.Errorf("catalog: register sync failures gauge: %w", err)
	}
	_, err = j.meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		return j.observeState(ctx, o, age, failures)
	}, age, failures)
	if err != nil {
		return fmt.Errorf("catalog: register sync state collection: %w", err)
	}
	return nil
}

func (j *syncJob) observeState(ctx context.Context, o metric.Observer, age metric.Float64ObservableGauge, failures metric.Int64ObservableGauge) error {
	ctx, cancel := context.WithTimeout(ctx, syncObserveTimeout)
	defer cancel()
	var state GetSyncStateRow
	var at time.Time
	err := j.cat.db.ReadTx(ctx, j.cat.scope, func(tx pg.Tx) error {
		var err error
		state, err = New().GetSyncState(ctx, tx)
		if err != nil {
			return err
		}
		at, err = j.timestamp(ctx, tx)
		return err
	})
	if err != nil {
		return fmt.Errorf("catalog: observe sync state: %w", err)
	}
	var seconds float64
	if since := syncStateSince(state); since != nil {
		seconds = max(0, at.Sub(*since).Seconds())
	}
	o.ObserveFloat64(age, seconds)
	o.ObserveInt64(failures, state.Failures)
	return nil
}

// A fresh database has no success time; the first failure starts its age instead.
func syncStateSince(state GetSyncStateRow) *time.Time {
	if state.LastSuccessAt != nil {
		return state.LastSuccessAt
	}
	return state.FailingSince
}
