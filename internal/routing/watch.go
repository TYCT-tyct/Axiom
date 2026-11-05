package routing

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/platform/alert"
)

// PresetRefusesAll is the signal of a platform or site preset that refused, with 404 preset_filter, every call that
// reached it on this node through a whole window: its rules leave nothing those calls can be routed to, and every key
// bound to it fails the same way (docs/domains/routing.md「滚动发布、失败与性能」). The tenants' own presets are not
// watched: their owners see the refusals themselves.
const PresetRefusesAll = "routing.preset_refuses_all"

// presetWindow is how often the tallies are reported. A window is closed by the first snapshot replaced after it ends,
// so it lasts a minute and up to one rebuild more; the administrator's alert rule decides how many windows in a row
// make an incident.
const presetWindow = time.Minute

// presetTally is one platform or site preset's calls on this node in the current window. Plan writes it; report reads
// and clears it once a window. routed is a flag, read before it is set, so the calls a working preset routes share a
// read-mostly cache line instead of contending on a counter.
type presetTally struct {
	routed   atomic.Bool
	filtered atomic.Int64
	signal   signalState // report's own, under presetWatch.mu
}

// signalState is what this node last told the alert sink about a preset. signalUnknown is the state after a start: the node
// may have raised the signal before it restarted, so the first routed window resolves it.
type signalState uint8

const (
	signalUnknown signalState = iota
	signalFiring
	signalClear
)

// routedOnce marks the window as having routed a call.
func (t *presetTally) routedOnce() {
	if t != nil && !t.routed.Load() {
		t.routed.Store(true)
	}
}

// presetWatch counts the calls the presets refuse and reports the platform's and sites' presets that refuse every call.
type presetWatch struct {
	refusals    metric.Int64Counter
	unavailable []metric.AddOption
	filtered    []metric.AddOption
	alerts      alert.Sink
	node        string

	mu      sync.Mutex
	tallies map[PresetID]*presetTally // the platform's and sites' presets: never deleted, so bounded by their quotas
	started time.Time                 // when the current window began
}

func newPresetWatch(cfg Config) (*presetWatch, error) {
	meter := cfg.Meter
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("routing")
	}
	refusals, err := meter.Int64Counter("axiom.routing.preset_refusals",
		metric.WithDescription("Calls refused because of the key's routing preset, by reason (docs/domains/routing.md「滚动发布、失败与性能」)."))
	if err != nil {
		return nil, err
	}
	reason := func(r string) []metric.AddOption {
		return []metric.AddOption{metric.WithAttributes(attribute.String("reason", r))}
	}
	return &presetWatch{
		refusals: refusals, unavailable: reason("preset_unavailable"), filtered: reason(PresetFilter.String()),
		alerts: cfg.Alerts, node: cfg.Node, tallies: map[PresetID]*presetTally{}, started: cfg.Now(),
	}, nil
}

// unavailableRefused counts a call refused because the node cannot route by its key's preset.
func (w *presetWatch) unavailableRefused() { w.refusals.Add(context.Background(), 1, w.unavailable...) }

// nothing counts a plan that left no entry: a 404 the preset took part in is a preset_filter refusal, and one more
// call the preset refused. A wait or a transient refusal is the endpoints' state, not the preset's.
func (w *presetWatch) nothing(p *preset, err error) {
	none, ok := errors.AsType[*NoEndpoint](err)
	if !ok || !none.Permanent || !none.has(PresetFilter) {
		return
	}
	w.refusals.Add(context.Background(), 1, w.filtered...)
	if p.tally != nil {
		p.tally.filtered.Add(1)
	}
}

func (e *NoEndpoint) has(r Reason) bool {
	for _, x := range e.Reasons {
		if x.Reason == r {
			return true
		}
	}
	return false
}

// tally is the tally of a platform or site preset, the same one from snapshot to snapshot; nil for the others, and for
// every preset on a node with no alert sink, whose plans then count nothing but the metric. The system preset filters
// nothing, so it has none either.
func (w *presetWatch) tally(p Preset) *presetTally {
	if w.alerts == nil || p.Scope != ScopePlatform && p.Scope != ScopeSite || p.ID == SystemPreset {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	t := w.tallies[p.ID]
	if t == nil {
		t = &presetTally{}
		w.tallies[p.ID] = t
	}
	return t
}

// tick reports the window when it has ended.
func (w *presetWatch) tick(now time.Time) {
	if w.alerts == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if now.Sub(w.started) >= presetWindow {
		w.report()
		w.started = now
	}
}

// report closes the window: a preset that routed a call resolves its signal, one that refused every call it saw raises
// it with their count, and one that saw no call keeps its state. A signal the bounded sink refused keeps the state too,
// so the next window tries again.
func (w *presetWatch) report() {
	for id, t := range w.tallies {
		routed, filtered := t.routed.Swap(false), t.filtered.Swap(0)
		key := w.node + "/" + uuid.UUID(id).String()
		switch {
		case routed && t.signal != signalClear:
			if w.alerts.Resolve(PresetRefusesAll, key) == nil {
				t.signal = signalClear
			}
		case !routed && filtered > 0:
			if w.alerts.Raise(PresetRefusesAll, key, "warning", map[string]string{"value": strconv.FormatInt(filtered, 10)}) == nil {
				t.signal = signalFiring
			}
		}
	}
}
