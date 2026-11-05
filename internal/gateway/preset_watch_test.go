package gateway

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/routing"
)

// signalLog records the signals handed to the alert sink, one line each.
type signalLog struct {
	mu   sync.Mutex
	sent []string
}

func (s *signalLog) Raise(kind, key, _ string, fields map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, fmt.Sprintf("raise %s %s %s", kind, key, fields["value"]))
	return nil
}

func (s *signalLog) RaiseOnce(_, kind, key, severity string, fields map[string]string) error {
	return s.Raise(kind, key, severity, fields)
}

func (s *signalLog) Resolve(kind, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, "resolve "+kind+" "+key)
	return nil
}

// TestABackendSearchCountsItsLastPlan: a backend search plans twice, and only the plan the call is admitted by says
// whether the key's preset served it. Here the first plan takes alpha, whose small context fits the request without
// the search's results; the second, with their bound, needs beta, which the platform preset filters out, so the call
// is refused 404 and the window reports the preset as refusing every call, not as having routed one.
func TestABackendSearchCountsItsLastPlan(t *testing.T) {
	t.Parallel()
	var clock atomic.Int64 // set past the snapshot the rig installs at the wall time, so the test's snapshots replace it
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	sink := &signalLog{}
	smallSpec := `{"items":["input","output"],"tiers":[],"context_length":8192,"max_output_tokens":1000,"streams":true,"abilities":[]}`
	price := `{"v":1,"currency":"USD",
		"rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","image_in":"0.000001","web_search":"0.01"},
		"service_tiers":{"priority":{"rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","image_in":"0.000001","web_search":"0.01"}}}}`
	r := newRig(t, func(o *rigOptions) {
		o.alphaSpec, o.price, o.search = smallSpec, price, &recordingSearcher{}
		o.routing = routing.Config{Alerts: sink, Node: "n1", Now: now}
		o.endpoints = func(name string, e *routing.Endpoint) {
			switch {
			case strings.HasPrefix(name, "alpha-"):
				e.Context = 8192
			case name == "beta-1":
				e.Class = routing.ClassRelay
			}
		}
	})
	official := routing.Preset{ID: routing.PresetID(uuid.NewV7()), Scope: routing.ScopePlatform, Strategy: routing.Cheapest}
	official.Classes, official.Policies = routing.ClassSet{routing.ClassOfficial}, routing.AllPolicies
	open := routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}
	snapshot := func() {
		snap, err := r.router.NewSnapshot(now(), r.routes, []routing.Preset{open, official})
		if err != nil {
			t.Fatal(err)
		}
		r.router.Replace(snap)
	}
	clock.Store(time.Now().Add(time.Second).UnixNano())
	snapshot()
	r.publish()
	a := r.newAccount(1e9)
	bindPreset(r, a, official.ID)
	body := `{"model":"acme/alpha","models":["acme/beta"],"plugins":[{"id":"web","engine":"test"}],"messages":[{"role":"system","content":"` +
		strings.Repeat("x", 20000) + `"},{"role":"user","content":"what is axiom"}]}`
	resp, out := r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(body))
	if code, _ := errorOf(t, out); resp.StatusCode != http.StatusNotFound || code != "no_endpoint" || !strings.Contains(out, "preset_filter") {
		t.Fatalf("status = %d, body = %s, want 404 no_endpoint for the preset", resp.StatusCode, out)
	}
	clock.Add(int64(time.Minute))
	snapshot() // the first replace after the window ends reports it
	sink.mu.Lock()
	defer sink.mu.Unlock()
	want := "raise " + routing.PresetRefusesAll + " n1/" + uuid.UUID(official.ID).String() + " 1"
	if len(sink.sent) != 1 || sink.sent[0] != want {
		t.Errorf("sent %q, want [%q]", sink.sent, want)
	}
}
