package quality

import (
	"context"
	"strconv"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

func (l *Loop) budgetSignal(exhausted bool) error {
	state := uint8(1)
	if exhausted {
		state = 2
	}
	if l.alerts == nil || l.budgetState == state {
		return nil
	}
	var err error
	key := "quality:" + l.node
	if exhausted {
		err = l.alerts.Raise("probe_budget_exhausted", key, "warning", nil)
	} else {
		err = l.alerts.Resolve("probe_budget_exhausted", key)
	}
	if err == nil {
		l.budgetState = state
	}
	return err
}

func (l *Loop) transitionMetric(ctx context.Context, c change) {
	if l.transitions != nil {
		l.transitions.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", "endpoint"), attribute.String("to", c.kind), attribute.String("cause", c.record.Cause), attribute.String("mode", string(c.record.Mode))))
	}
}

func healthSignal(c change) alert.Signal {
	kind := "endpoint_" + c.kind
	severity := "warning"
	if c.kind == "restored" {
		severity = "info"
	}
	if c.kind == "owned_endpoint_removed" {
		kind = c.kind
	}
	key := c.record.ID.String()
	source := "quality:" + key + ":" + strconv.FormatInt(c.record.Epoch, 10)
	if c.kind == "offline" {
		// Reuse the kind every released version already resolves. A new endpoint_offline would stay unresolved when an
		// old node restores the endpoint during a rolling upgrade, because resolution is by exact (kind, key).
		kind = "endpoint_still_down"
		// One signal per incident: the source stays the same while the endpoint is out, so a later failed probe or a
		// restart that replays the transition does not send it twice.
		source = "quality:" + key + ":offline:" + strconv.FormatInt(c.record.Opened, 10)
	}
	return alert.Signal{Source: source, Kind: kind, Key: key, Severity: severity, Fields: map[string]string{"cause": c.record.Cause}}
}

func (l *Loop) checkModels(endpoints []suppliers.Routable) error {
	if l.alerts == nil || l.monitor.mode != Enforce {
		return nil
	}
	type health struct {
		up     bool
		opened int64
	}
	models := map[uuid.UUID]health{}
	for _, ep := range endpoints {
		if !ep.Platform() {
			continue
		}
		e := l.monitor.of(ep.ID)
		e.mu.Lock()
		opened := e.record.Opened
		e.mu.Unlock()
		h := models[ep.Model]
		h.up = h.up || opened == 0
		h.opened = max(h.opened, opened)
		models[ep.Model] = h
	}
	for model, h := range models {
		down := !h.up
		if old, known := l.unavailable[model]; known && old == down {
			continue
		}
		key := model.String()
		var err error
		if down {
			err = l.alerts.RaiseOnce("quality:model:"+key+":"+strconv.FormatInt(h.opened, 10), "model_unavailable", key, "critical", nil)
		} else {
			err = l.alerts.Resolve("model_unavailable", key)
		}
		if err != nil {
			return err
		}
		l.unavailable[model] = down
	}
	return nil
}

func persistHealthSignal(ctx context.Context, tx pg.Tx, c change) error {
	if c.kind == "removed" {
		// The trip has already taken the endpoint out of the route; the alert waits for the failed re-check, so a
		// transient blip that the probe clears never pages operations (docs/product.md「端点属性」).
		return nil
	}
	signal := healthSignal(c)
	if err := alert.Persist(ctx, tx, uuid.NewV7(), signal); err != nil {
		return err
	}
	if c.kind != "restored" {
		return nil
	}
	for _, kind := range []string{"endpoint_offline", "endpoint_removed", "owned_endpoint_removed", "endpoint_still_down", "endpoint_restored"} {
		recovery := alert.Signal{Source: signal.Source + ":" + kind + ":resolved", Kind: kind, Key: signal.Key, Severity: "info", Resolved: true}
		if err := alert.Persist(ctx, tx, uuid.NewV7(), recovery); err != nil {
			return err
		}
	}
	return nil
}

// fakeAlert is the signal that an endpoint answered as another model verifyFailures times in a row. One episode
// raises one signal, and a later pass or a changed claim resolves it.
const fakeAlert = "endpoint_fake"

func (l *Loop) raiseFake(id suppliers.EndpointID, author string, failures int, at time.Time) {
	if l.alerts == nil {
		return
	}
	key := id.String()
	fields := map[string]string{"claimed_author": author, "failures": strconv.Itoa(failures)}
	// A new episode gets a new source, so observability does not take it for the one already resolved.
	if err := l.alerts.RaiseOnce("quality:"+key+":"+strconv.FormatInt(at.UnixNano(), 10), fakeAlert, key, "critical", fields); err != nil {
		l.logger.Warn("quality fake alert failed", "endpoint", key, "err", err)
	}
}

func (l *Loop) resolveFake(id suppliers.EndpointID) {
	if l.alerts == nil {
		return
	}
	if err := l.alerts.Resolve(fakeAlert, id.String()); err != nil {
		l.logger.Warn("quality fake alert resolution failed", "endpoint", id.String(), "err", err)
	}
}
