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

// presetTally is one platform or site preset's calls on this node in the current window. Router.Planned writes it once
// a call; report reads and clears it once a window. routed is a flag, read before it is set, so the calls a working
// preset routes share a read-mostly cache line instead of contending on a counter.
type presetTally struct {
	routed   atomic.Bool
	filtered atomic.Int64
	// The rest is report's own, under presetWatch.mu: what the sink last accepted, and the signal a window owes it, kept
	// until the sink takes it or a later window with calls replaces it.
	sent  signalState
	owed  signalState // signalUnknown when nothing is owed
	value int64       // the refusals of the window the owed raise reports
}

// signalState is what the sink was told about a preset: firing or clear. signalUnknown is the state after a start: the
// node may have raised the signal before it restarted, so the first routed window resolves it.
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
	// unavailable and filtered are the calls refused by reason, which the meter reads when it is collected: Plan has
	// no context to record a measurement with, and an atomic add is all the call path pays.
	unavailable, filtered atomic.Int64
	alerts                alert.Sink
	node                  string

	mu      sync.Mutex
	tallies map[PresetID]*presetTally // the platform's and sites' presets: never deleted, so bounded by their quotas
	started time.Time                 // when the current window began
}

func newPresetWatch(cfg Config) (*presetWatch, error) {
	w := &presetWatch{alerts: cfg.Alerts, node: cfg.Node, tallies: map[PresetID]*presetTally{}, started: cfg.Now()}
	meter := cfg.Meter
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("routing")
	}
	unavailable := metric.WithAttributes(attribute.String("reason", "preset_unavailable"))
	filtered := metric.WithAttributes(attribute.String("reason", PresetFilter.String()))
	_, err := meter.Int64ObservableCounter("axiom.routing.preset_refusals",
		metric.WithDescription("Calls refused because of the key's routing preset, by reason (docs/domains/routing.md「滚动发布、失败与性能」)."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(w.unavailable.Load(), unavailable)
			o.Observe(w.filtered.Load(), filtered)
			return nil
		}))
	return w, err
}

// Planned counts how a call's route came out: p and err are what the call's last Plan returned, or the error that
// ended its admission after it. A call may be planned more than once (a backend search plans again with the results'
// bound), and only the plan it is admitted by says whether the preset served it, so the caller tells the router once,
// with the last. A route counts as routed; ErrPresetUnavailable as a preset_unavailable refusal; a permanent 404 the
// preset took part in as a preset_filter refusal. A wait, a transient refusal or any other error is the endpoints' or
// the request's, not the preset's, and counts nothing.
func (r *Router) Planned(p Plan, err error) {
	w := r.watch
	switch {
	case err == nil:
		p.tally.routedOnce()
	case errors.Is(err, ErrPresetUnavailable):
		w.unavailable.Add(1)
	default:
		none, ok := errors.AsType[*NoEndpoint](err)
		if !ok || !none.Permanent || !none.has(PresetFilter) {
			return
		}
		w.filtered.Add(1)
		if p.tally != nil {
			p.tally.filtered.Add(1)
		}
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

// report closes the window: a preset that routed a call owes a resolution, unless the sink already holds one; one that
// refused every call it saw owes a raise with their count; one that saw no call keeps what it owed. Then it sends what
// is owed. A signal the bounded sink refused stays owed, so a later window sends it, with no call in between too; a
// later window with calls replaces it, because it describes the preset as it is now.
func (w *presetWatch) report() {
	for id, t := range w.tallies {
		routed, filtered := t.routed.Swap(false), t.filtered.Swap(0)
		switch {
		case routed:
			t.owed = signalClear
		case filtered > 0:
			t.owed, t.value = signalFiring, filtered
		}
		if t.owed == signalClear && t.sent == signalClear {
			t.owed = signalUnknown // a resolution the sink already holds; a raise is sent each window, with its count
		}
		if t.owed != signalUnknown && w.send(id, t) == nil {
			t.sent, t.owed = t.owed, signalUnknown
		}
	}
}

func (w *presetWatch) send(id PresetID, t *presetTally) error {
	key := w.node + "/" + uuid.UUID(id).String()
	if t.owed == signalClear {
		return w.alerts.Resolve(PresetRefusesAll, key)
	}
	return w.alerts.Raise(PresetRefusesAll, key, "warning", map[string]string{"value": strconv.FormatInt(t.value, 10)})
}
