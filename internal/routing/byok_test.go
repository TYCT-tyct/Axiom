package routing_test

import (
	"testing"
	"time"

	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// TestByokComesFirst: an account's own key is tried before the site's endpoints, whatever the two cost, and the site's
// endpoints stay as the fallbacks after it.
func TestByokComesFirst(t *testing.T) {
	t.Parallel()
	shared := endpoint(t, 1) // cheaper than the key's, so price alone would choose it
	own := endpoint(t, 2, func(e *routing.Endpoint) { e.Byok, e.ByokFallback = true, true })
	h := newHarness(t, routing.Config{}, openPreset(), shared, own)
	p := h.plan(input())
	if first(p) != own.ID {
		t.Fatalf("the plan tries %v first, want the account's own key %v", first(p), own.ID)
	}
	if len(p.Entries) < 2 || p.Entries[1].Endpoint != shared.ID {
		t.Fatalf("the plan's fallbacks are %+v, want the site's endpoint %v after the key", p.Entries, shared.ID)
	}
}

// TestByokNoFallback: a key that forbids falling back leaves the site's endpoints out for a model it covers, so a key
// that cannot serve the call answers NoEndpoint instead of spending the site's money. Another model's key does not.
func TestByokNoFallback(t *testing.T) {
	t.Parallel()
	shared := endpoint(t, 1)
	own := endpoint(t, 2, func(e *routing.Endpoint) { e.Byok, e.ByokFallback = true, false })
	h := newHarness(t, routing.Config{}, openPreset(), shared, own)
	p := h.plan(input())
	if len(p.Entries) != 1 || first(p) != own.ID {
		t.Fatalf("the plan is %+v, want only the account's own key %v", p.Entries, own.ID)
	}

	// The key cannot serve the call: its capability is missing. Waiting would not help, and the site's endpoint is
	// forbidden, so the answer is permanent.
	h.replace(openPreset(), shared, endpoint(t, 2, func(e *routing.Endpoint) {
		e.Byok, e.ByokFallback, e.Caps = true, false, 0
	}))
	in := input()
	in.Req.Needs = protocol.Tools
	none := h.excluded(in)
	if !none.Permanent {
		t.Fatalf("the answer is %+v, want a permanent NoEndpoint", none)
	}
	var saw bool
	for _, r := range none.Reasons {
		saw = saw || r.Reason == routing.ByokNoFallback
	}
	if !saw {
		t.Fatalf("the reasons are %+v, want byok_no_fallback among them", none.Reasons)
	}
}

// TestByokNoFallbackIsPerModel: a key that covers another model leaves this model's site endpoints to serve it.
func TestByokNoFallbackIsPerModel(t *testing.T) {
	t.Parallel()
	shared := endpoint(t, 1)
	otherKey := endpoint(t, 2, func(e *routing.Endpoint) {
		e.Model, e.Byok, e.ByokFallback = other, true, false
	})
	h := newHarness(t, routing.Config{}, openPreset(), shared, otherKey)
	p := h.plan(input())
	if len(p.Entries) != 1 || first(p) != shared.ID {
		t.Fatalf("the plan is %+v, want the site's endpoint %v for the model the key does not cover", p.Entries, shared.ID)
	}
}

// TestByokRuleSurvivesRejection: the account's no-fallback choice is a snapshot rule, not an endpoint. When the key's
// credential is rejected its endpoint is gone, but the rule still keeps the platform's endpoint out of the plan.
func TestByokRuleSurvivesRejection(t *testing.T) {
	t.Parallel()
	shared := endpoint(t, 1)
	h := newHarness(t, routing.Config{}, openPreset(), shared)
	h.now = h.now.Add(time.Nanosecond)
	snap, err := h.r.NewSnapshot(h.now, []routing.Endpoint{shared}, []routing.Preset{openPreset()},
		routing.ByokRule{Account: acct, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	h.r.Replace(snap)
	none := h.excluded(input())
	if !none.Permanent {
		t.Fatalf("the answer is %+v, want a permanent NoEndpoint", none)
	}
	var saw bool
	for _, r := range none.Reasons {
		saw = saw || r.Reason == routing.ByokNoFallback
	}
	if !saw {
		t.Fatalf("the reasons are %+v, want byok_no_fallback after the key was rejected", none.Reasons)
	}
}
