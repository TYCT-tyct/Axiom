package routing_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"axiom/internal/routing"
)

func TestCooldownExcludesOnlyItsScopeUntilTheDeadline(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		scope routing.Scope
		after time.Duration
		want  []routing.EndpointID
	}{
		{"endpoint", routing.ScopeEndpoint, 30 * time.Second, []routing.EndpointID{ep(2), ep(3)}},
		{"supplier", routing.ScopeSupplier, 30 * time.Second, []routing.EndpointID{ep(3)}},
		{"supplier without retry time", routing.ScopeSupplier, 0, []routing.EndpointID{ep(3)}},
		{"credential stays in suppliers", routing.ScopeCredential, 30 * time.Second, []routing.EndpointID{ep(1), ep(2), ep(3)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preset := openPreset()
			preset.Strategy = routing.Cheapest
			eps := []routing.Endpoint{endpoint(t, 1), endpoint(t, 2, func(e *routing.Endpoint) { e.Supplier = sup(1) }), endpoint(t, 3)}
			h := newHarness(t, routing.Config{MaxStale: time.Hour}, preset, eps...)
			entry := h.plan(input()).Entries[0]
			until := h.now.Add(tc.after)
			if tc.after == 0 {
				until = h.now.Add(time.Minute)
			}
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
