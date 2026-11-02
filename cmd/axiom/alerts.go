package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

// Sampling never adds database work or signal validation to Commit's call path.
func sampleAlerts(ctx context.Context, db *pg.DB, r *observability.Recorder, sink alert.Sink, node string, logger *slog.Logger) func() {
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTicker(time.Minute)
		defer timer.Stop()
		var previous alertSample
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

func collectAlerts(ctx context.Context, db *pg.DB, r *observability.Recorder, sink alert.Sink, node string, previous alertSample, logger *slog.Logger) alertSample {
	depth, lost := r.AlertSample()
	err := previous.writer(sink, node, depth)
	if err != nil {
		logger.ErrorContext(ctx, "reporting writer lag alert", "err", err)
	}
	if lost > previous.lost {
		if err = sink.Raise("observability.records_lost", node, "critical", map[string]string{"value": strconv.FormatInt(lost, 10)}); err != nil {
			logger.ErrorContext(ctx, "reporting lost records alert", "err", err)
		}
	}
	attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = db.Tx(attempt, pg.PlatformScope(), func(tx pg.Tx) error {
		if err := accounts.SampleSecurityAlerts(attempt, tx); err != nil {
			return err
		}
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
	previous.lost = lost
	return previous
}

func startAlerts(ctx context.Context, db *pg.DB, r *observability.Recorder, console *consoleParts, logger *slog.Logger, signals *alert.Bus) func() {
	console.alerts = signals
	stop := sampleAlerts(ctx, db, r, signals, console.cfg.valkey.NodeID, logger)
	return func() {
		stop()
		drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), console.cfg.shutdownTimeout)
		defer cancel()
		if depth, _ := r.AlertSample(); depth == 0 {
			if err := signals.Resolve("observability.write_lag", console.cfg.valkey.NodeID); err != nil {
				logger.Error("resolving drained writer alert", "err", err)
			}
		}
		if err := signals.Close(drain); err != nil {
			logger.Error("closing alert signals", "err", err)
		}
	}
}

type alertSample struct {
	lost int64
	idle int
}

func (s *alertSample) writer(sink alert.Sink, node string, depth int) error {
	if depth > 0 {
		s.idle = 0
		return sink.Raise("observability.write_lag", node, "warning", map[string]string{"value": strconv.Itoa(depth)})
	}
	s.idle = min(s.idle+1, 2)
	if s.idle == 2 {
		return sink.Resolve("observability.write_lag", node)
	}
	return nil
}
