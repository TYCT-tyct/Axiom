package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

// Sampling never adds database work or signal validation to Enqueue's call path.
func sampleAlerts(ctx context.Context, db *pg.DB, r *observability.Recorder, sink alert.Sink, node string, logger *slog.Logger) func() {
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTicker(time.Minute)
		defer timer.Stop()
		var previous int64
		for {
			previous = collectAlerts(run, db, r, sink, node, previous, logger)
			select {
			case <-run.Done():
				return
			case <-timer.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func collectAlerts(ctx context.Context, db *pg.DB, r *observability.Recorder, sink alert.Sink, node string, previous int64, logger *slog.Logger) int64 {
	depth, lost := r.AlertSample()
	var err error
	if depth > 0 {
		err = sink.Raise("observability.write_lag", node, "warning", map[string]string{"value": strconv.Itoa(depth)})
	} else {
		err = sink.Resolve("observability.write_lag", node)
	}
	if err != nil {
		logger.ErrorContext(ctx, "reporting writer lag alert", "err", err)
	}
	if lost > previous {
		if err = sink.Raise("observability.records_lost", node, "critical", map[string]string{"value": strconv.FormatInt(lost, 10)}); err != nil {
			logger.ErrorContext(ctx, "reporting lost records alert", "err", err)
		}
	}
	attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = db.Tx(attempt, pg.PlatformScope(), func(tx pg.Tx) error {
		items, err := billing.UnknownItems(attempt, tx)
		if err != nil {
			return err
		}
		signal := alert.Signal{Kind: "billing.settlement_anomaly", Key: "unknown_items", Severity: "critical", Fields: map[string]string{"value": strconv.Itoa(len(items))}, Resolved: len(items) == 0}
		return alert.Persist(attempt, tx, uuid.NewV7(), signal)
	})
	if err != nil && ctx.Err() == nil {
		logger.ErrorContext(ctx, "sampling settlement anomalies", "err", err)
	}
	return lost
}

func startAlerts(ctx context.Context, db *pg.DB, r *observability.Recorder, node string, timeout time.Duration, logger *slog.Logger) func() {
	signals := alert.New(ctx, db, pg.PlatformScope(), logger)
	stop := sampleAlerts(ctx, db, r, signals, node, logger)
	return func() {
		stop()
		drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		if err := signals.Close(drain); err != nil {
			logger.Error("closing alert signals", "err", err)
		}
	}
}
