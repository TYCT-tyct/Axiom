package routing_test

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/telemetry"
	"axiom/internal/platform/telemetry/telemetrytest"
	"axiom/internal/routing"
)

// signals records what the router tells the alert sink, one line per signal. It refuses the next refuse signals, as a
// full buffer does, and records none of them.
type signals struct {
	mu     sync.Mutex
	sent   []string
	refuse int
}

func (s *signals) Raise(kind, key, severity string, fields map[string]string) error {
	return s.add(fmt.Sprintf("raise %s %s %s %s", kind, key, severity, fields["value"]))
}

func (s *signals) RaiseOnce(_, kind, key, severity string, fields map[string]string) error {
	return s.Raise(kind, key, severity, fields)
}

func (s *signals) Resolve(kind, key string) error { return s.add("resolve " + kind + " " + key) }

func (s *signals) add(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse > 0 {
		s.refuse--
		return errors.New("the buffer is full")
	}
	s.sent = append(s.sent, line)
	return nil
}

// refuseNext makes the sink refuse the next n signals.
func (s *signals) refuseNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = n
}

// call plans one call and tells the router how it came out, as admission does; it reports whether it was routed.
func call(h *harness, in routing.Input) bool {
	p, _, err := h.r.Plan(in)
	h.r.Planned(p, err)
	return err == nil
}

// take is what was sent since the last take.
func (s *signals) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.sent
	s.sent = nil
	return out
}

// refuseAll leaves the test endpoint out: its completion costs 2 per million tokens.
const refuseAll = `{"default":{"max_price":{"completion":"0.5"}}}`

// scoped is the preset of the scope the rules make; a platform preset limits prices in dollars.
func scoped(t *testing.T, scope routing.PresetScope, rules string) routing.Preset {
	t.Helper()
	p := layered(scope)
	if scope == routing.ScopePlatform {
		p.Currency = usd
	}
	if err := p.ReadRules([]byte(rules)); err != nil {
		t.Fatal(err)
	}
	return p
}

// watched is a router that reports to sink as node n1, with one endpoint and p.
func watched(t *testing.T, sink *signals, tel *telemetry.Telemetry, p routing.Preset) *harness {
	t.Helper()
	cfg := routing.Config{Alerts: sink, Node: "n1"}
	if tel != nil {
		cfg.Meter = tel.Metrics.Meter("axiom/routing")
	}
	return newHarness(t, cfg, p, endpoint(t, 1))
}

// window is one minute of calls to a preset of the given rules, and the signals its end sends for a watched preset;
// the sink refuses the first sinkRefuses of them.
type window struct {
	name           string
	rules          string
	routed, refuse int
	sinkRefuses    int
	want           []string
}

// TestAPresetThatRefusesEveryCallIsSignalled: a window in which a platform or site preset refused every call it saw
// raises the signal with their count, under the node's key; a window that routed one resolves it, the first time
// after a start too, since the node may have raised it before; a window with no call changes nothing. A signal the
// sink refuses is sent by a later window, one with no call too, unless a window with calls replaces it. The tenants'
// presets are not watched.
func TestAPresetThatRefusesEveryCallIsSignalled(t *testing.T) {
	t.Parallel()
	key := "n1/" + uuid.UUID(pre).String()
	raised := func(n int) []string {
		return []string{fmt.Sprintf("raise %s %s warning %d", routing.PresetRefusesAll, key, n)}
	}
	resolved := []string{"resolve " + routing.PresetRefusesAll + " " + key}
	windows := []window{
		{"routing after a start resolves", allowed, 1, 0, 0, resolved},
		{"routing again sends nothing", allowed, 2, 0, 0, nil},
		{"refusing every call raises their count", refuseAll, 0, 3, 0, raised(3)},
		{"refusing on raises again", refuseAll, 0, 1, 0, raised(1)},
		{"no call sends nothing", refuseAll, 0, 0, 0, nil},
		{"a window that also routed resolves", allowed, 1, 0, 0, resolved},
		{"a raise the sink refuses", refuseAll, 0, 2, 1, nil},
		{"is sent by a window with no call", refuseAll, 0, 0, 0, raised(2)},
		{"a resolution the sink refuses", allowed, 1, 0, 1, nil},
		{"is sent by a window with no call", allowed, 0, 0, 0, resolved},
		{"a refused raise", refuseAll, 0, 1, 1, nil},
		{"replaced by a routed window owes nothing, the sink holding no event", allowed, 1, 0, 0, nil},
	}
	for _, scope := range []routing.PresetScope{routing.ScopePlatform, routing.ScopeSite, routing.ScopeAccount, routing.ScopeWorkspace} {
		t.Run(scope.String(), func(t *testing.T) {
			t.Parallel()
			checkWindows(t, scope, windows, scope == routing.ScopePlatform || scope == routing.ScopeSite)
		})
	}
}

// checkWindows runs the windows in order on one preset of the scope and checks what each end sent: the window's want
// when the scope is watched, nothing when it is not.
func checkWindows(t *testing.T, scope routing.PresetScope, windows []window, watchedScope bool) {
	t.Helper()
	sink := &signals{}
	h := watched(t, sink, nil, scoped(t, scope, allowed))
	for _, w := range windows {
		h.replace(scoped(t, scope, w.rules), endpoint(t, 1))
		for i := range w.routed + w.refuse {
			if routed := call(h, presetInput()); routed != (i < w.routed) {
				t.Fatalf("%s: call %d routed %v", w.name, i, routed)
			}
		}
		sink.refuseNext(w.sinkRefuses)
		h.now = h.now.Add(time.Minute)
		h.replace(scoped(t, scope, w.rules), endpoint(t, 1)) // the first replace after the window ends reports it
		want := w.want
		if !watchedScope {
			want = nil
		}
		if got := sink.take(); !slices.Equal(got, want) {
			t.Errorf("%s: sent %q, want %q", w.name, got, want)
		}
	}
}

// TestPresetRefusalsAreCounted: a call refused because its preset leaves no endpoint, and one whose preset the node
// cannot route by, are counted by reason under the name an operator's scrape reads; a routed call is not.
func TestPresetRefusalsAreCounted(t *testing.T) {
	t.Parallel()
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	h := watched(t, &signals{}, tel, scoped(t, routing.ScopeAccount, refuseAll))
	for range 2 {
		if call(h, presetInput()) {
			t.Fatal("the preset routed a call")
		}
	}
	unknown := presetInput()
	unknown.Admission.Preset = routing.PresetID(id(901))
	p, _, err := h.r.Plan(unknown)
	if !errors.Is(err, routing.ErrPresetUnavailable) {
		t.Fatalf("a preset the snapshot does not hold: %v", err)
	}
	h.r.Planned(p, err)
	h.replace(scoped(t, routing.ScopeAccount, allowed), endpoint(t, 1))
	if !call(h, presetInput()) {
		t.Fatal("the open preset refused a call")
	}
	for reason, want := range map[string]string{"preset_filter": "2", "preset_unavailable": "1"} {
		if got := telemetrytest.Metric(t, tel, `axiom_routing_preset_refusals_total{reason="`+reason+`"}`); got != want {
			t.Errorf("%s: %s, want %s", reason, got, want)
		}
	}
}
