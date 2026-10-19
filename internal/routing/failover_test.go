package routing_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"axiom/internal/protocol"
	"axiom/internal/routing"
)

func TestCooldownExcludesOnlyItsScopeUntilTheDeadline(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		scope routing.Scope
		after time.Duration
		pause time.Duration // how long the pause lasts; Config.Pause (5s) when no wait is stated, at most two hours
		want  []routing.EndpointID
	}{
		{"endpoint", routing.ScopeEndpoint, 30 * time.Second, 30 * time.Second, []routing.EndpointID{ep(2), ep(3)}},
		{"endpoint without retry time", routing.ScopeEndpoint, 0, 5 * time.Second, []routing.EndpointID{ep(2), ep(3)}},
		{"supplier", routing.ScopeSupplier, 30 * time.Second, 30 * time.Second, []routing.EndpointID{ep(3)}},
		{"supplier without retry time", routing.ScopeSupplier, 0, 5 * time.Second, []routing.EndpointID{ep(3)}},
		{"retry time past the ceiling", routing.ScopeEndpoint, 24 * time.Hour, 2 * time.Hour, []routing.EndpointID{ep(2), ep(3)}},
		{"credential stays in suppliers", routing.ScopeCredential, 30 * time.Second, 30 * time.Second, []routing.EndpointID{ep(1), ep(2), ep(3)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preset := openPreset()
			preset.Strategy = routing.Cheapest
			eps := []routing.Endpoint{endpoint(t, 1), endpoint(t, 2, func(e *routing.Endpoint) { e.Supplier = sup(1) }), endpoint(t, 3)}
			h := newHarness(t, routing.Config{MaxStale: 3 * time.Hour}, preset, eps...)
			entry := h.plan(input()).Entries[0]
			until := h.now.Add(tc.pause)
			h.r.Block(entry, tc.scope, tc.after)
			// Even removal and reappearance must not reopen a blocked endpoint or supplier.
			h.replace(preset, eps[2])
			h.replace(preset, eps...)
			h.now = until.Add(-time.Nanosecond)
			if got := h.plan(input()).Record.Route; !slices.Equal(got, tc.want) {
				t.Fatalf("before deadline: %v, want %v", got, tc.want)
			}
			if got := h.r.Blocked(entry); got != (tc.scope != routing.ScopeCredential) {
				t.Fatalf("an already planned entry: blocked=%v", got)
			}
			h.now = until
			if got := h.plan(input()).Record.Route; len(got) != 3 || h.r.Blocked(entry) {
				t.Fatalf("did not recover at deadline: %v", got)
			}
		})
	}
}

func TestCooldownCannotBeShortenedAndDoesNotClearInflight(t *testing.T) {
	t.Parallel()
	for _, scope := range []routing.Scope{routing.ScopeEndpoint, routing.ScopeSupplier} {
		e := endpoint(t, 1, func(e *routing.Endpoint) { e.MaxConcurrent = 1 })
		h := newHarness(t, routing.Config{MaxStale: time.Hour}, openPreset(), e)
		entry := h.plan(input()).Entries[0]
		h.r.Begin(e.ID)
		h.r.Block(entry, scope, time.Minute)
		h.r.Block(entry, scope, time.Second)
		h.replace(openPreset(), e)
		h.now = t0.Add(30 * time.Second)
		_, wait, err := h.r.Plan(input())
		if wait != nil || err == nil {
			t.Fatalf("blocked full endpoint should be unavailable, wait %v, err %v", wait, err)
		}
		var no *routing.NoEndpoint
		if !errors.As(err, &no) || no.Permanent || no.Reasons[0].Reason != routing.CoolingDown {
			t.Fatalf("cooldown is not a temporary exclusion: %v", err)
		}
		h.now = t0.Add(time.Minute)
		_, wait, err = h.r.Plan(input())
		if wait == nil || err != nil {
			t.Fatalf("snapshot lost inflight count: wait %v, err %v", wait, err)
		}
		h.r.End(e.ID)
		h.plan(input())
	}
}

// TestAPausedEndpointThatCannotServeTheRequestIsLeftOutForeverNotForNow: the permanent reason wins, or a request no
// endpoint can ever serve would be answered 503 and retried instead of 404.
func TestAPausedEndpointThatCannotServeTheRequestIsLeftOutForeverNotForNow(t *testing.T) {
	t.Parallel()
	h := newHarness(t, routing.Config{MaxStale: time.Hour}, openPreset(), endpoint(t, 1, func(e *routing.Endpoint) { e.Caps = 0 }))
	h.r.Block(h.plan(input()).Entries[0], routing.ScopeEndpoint, 30*time.Second)
	in := input()
	in.Req.Needs = protocol.Tools
	_, _, err := h.r.Plan(in)
	var no *routing.NoEndpoint
	if !errors.As(err, &no) || !no.Permanent || no.Reasons[0].Reason != routing.MissingCapability {
		t.Fatalf("a paused endpoint lacking the capability: %v", err)
	}
}
