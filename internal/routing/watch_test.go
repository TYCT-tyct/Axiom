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

// signals records what the router tells the alert sink, one line per signal.
type signals struct {
	mu   sync.Mutex
	sent []string
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
	s.sent = append(s.sent, line)
	return nil
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

// window is one minute of calls to a preset of the given rules, and the signals its end sends for a watched preset.
type window struct {
	name           string
	rules          string
	routed, refuse int
	want           []string
}

// TestAPresetThatRefusesEveryCallIsSignalled: a window in which a platform or site preset refused every call it saw
// raises the signal with their count, under the node's key; a window that routed one resolves it, the first time
// after a start too, since the node may have raised it before; a window with no call changes nothing. The tenants'
// presets are not watched.
func TestAPresetThatRefusesEveryCallIsSignalled(t *testing.T) {
	t.Parallel()
	key := "n1/" + uuid.UUID(pre).String()
	raised := func(n int) []string {
		return []string{fmt.Sprintf("raise %s %s warning %d", routing.PresetRefusesAll, key, n)}
	}
	resolved := []string{"resolve " + routing.PresetRefusesAll + " " + key}
	windows := []window{
		{"routing after a start resolves", allowed, 1, 0, resolved},
		{"routing again sends nothing", allowed, 2, 0, nil},
		{"refusing every call raises their count", refuseAll, 0, 3, raised(3)},
		{"refusing on raises again", refuseAll, 0, 1, raised(1)},
		{"no call sends nothing", refuseAll, 0, 0, nil},
		{"a window that also routed resolves", allowed, 1, 0, resolved},
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
			if routed := h.route(presetInput()) != nil; routed != (i < w.routed) {
				t.Fatalf("%s: call %d routed %v", w.name, i, routed)
			}
		}
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
		if h.route(presetInput()) != nil {
			t.Fatal("the preset routed a call")
		}
	}
	unknown := presetInput()
	unknown.Admission.Preset = routing.PresetID(id(901))
	if _, _, err := h.r.Plan(unknown); !errors.Is(err, routing.ErrPresetUnavailable) {
		t.Fatalf("a preset the snapshot does not hold: %v", err)
	}
	h.replace(scoped(t, routing.ScopeAccount, allowed), endpoint(t, 1))
	h.plan(presetInput())
	for reason, want := range map[string]string{"preset_filter": "2", "preset_unavailable": "1"} {
		if got := telemetrytest.Metric(t, tel, `axiom_routing_preset_refusals_total{reason="`+reason+`"}`); got != want {
			t.Errorf("%s: %s, want %s", reason, got, want)
		}
	}
}
