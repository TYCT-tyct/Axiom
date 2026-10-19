package gateway

import (
	"cmp"
	"context"
	"net/http"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

func TestSupplierFailureSkipsEveryRemainingEntryOfThatSupplier(t *testing.T) {
	for _, tc := range []struct {
		scope suppliers.Scope
		sends [3]int // entries 1 and 2 are one supplier's, entry 3 another's
	}{
		{suppliers.ScopeSupplier, [3]int{1, 0, 1}},
		{suppliers.ScopeEndpoint, [3]int{1, 1, 0}},
	} {
		run(t, func(t *testing.T, ctx context.Context) {
			now := time.Now()
			h := failFirstEntry(t, ctx, tc.scope, &now)
			got := [3]int{h.up.endpoint(1).sends, h.up.endpoint(2).sends, h.up.endpoint(3).sends}
			if got != tc.sends || h.w.status != http.StatusOK {
				t.Fatalf("scope %d: sends %v, want %v; status %d", tc.scope, got, tc.sends, h.w.status)
			}
			attempts := h.ended().outcome.Attempts
			if len(attempts) != 2 || attempts[0].Result != billing.NotExecuted || attempts[1].Entry != tc.sends[2]+1 {
				t.Fatalf("attempts lost their reserved entry indices: %+v", attempts)
			}
		})
	}
}

func TestSupplierPauseEndsAfterTheRecoveryTime(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		now := time.Now()
		h := failFirstEntry(t, ctx, suppliers.ScopeSupplier, &now)
		for _, e := range h.call.Plan.Entries[:2] {
			if !h.router.Blocked(e) {
				t.Fatal("supplier was not paused node-locally")
			}
		}
		now = now.Add(time.Minute)
		if h.router.Blocked(h.call.Plan.Entries[0]) {
			t.Fatal("supplier did not recover")
		}
	})
}

// failFirstEntry runs a call whose first entry fails unexecuted at scope; entries 1 and 2 belong to one supplier.
func failFirstEntry(t *testing.T, ctx context.Context, scope suppliers.Scope, now *time.Time) *harness {
	t.Helper()
	h := newHarness(t, ctx, options{
		entries: 3, maxAttempts: 2,
		supplier: func(n int) int {
			if n == 2 {
				return 1
			}
			return n
		},
		routing: routing.Config{Now: func() time.Time { return *now }},
	})
	f := &suppliers.Failure{Scope: scope, Executed: suppliers.NotExecuted}
	h.up.endpoint(1).script = []scripted{func(context.Context) (*http.Response, *suppliers.Failure) { return nil, f }}
	h.up.endpoint(2).script = []scripted{whole(h)}
	h.up.endpoint(3).script = []scripted{whole(h)}
	h.run()
	return h
}

func TestRateLimitPausesOnlyTheNamedScope(t *testing.T) {
	for _, tc := range []rateLimitCase{
		{"endpoint retry time", suppliers.ScopeEndpoint, 30 * time.Second},
		{"endpoint default", suppliers.ScopeEndpoint, 0},
		{"supplier retry time", suppliers.ScopeSupplier, 30 * time.Second},
		{"credential handled by acquire", suppliers.ScopeCredential, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run(t, func(t *testing.T, ctx context.Context) { tc.check(t, ctx) })
		})
	}
}

type rateLimitCase struct {
	name  string
	scope suppliers.Scope
	after time.Duration
}

// check rate-limits the first of two alike endpoints and watches the pause hold until, and only until, its end.
func (tc rateLimitCase) check(t *testing.T, ctx context.Context) {
	now := time.Now()
	h := newHarness(t, ctx, options{entries: 2, routing: routing.Config{MaxStale: time.Hour, Now: func() time.Time { return now }}})
	firstChoice := twoAlikeEndpoints(t, h.router)
	f := &suppliers.Failure{Scope: tc.scope, Cause: suppliers.RateLimited{RetryAfter: tc.after}}
	h.up.endpoint(1).script = []scripted{func(context.Context) (*http.Response, *suppliers.Failure) { return nil, f }, whole(h)}
	h.up.endpoint(2).script = []scripted{whole(h)}
	h.run()
	after := cmp.Or(tc.after, time.Minute) // the default Recovery
	now = now.Add(after - time.Nanosecond)
	e := h.call.Plan.Entries[0]
	blocked := tc.scope != suppliers.ScopeCredential
	if h.router.Blocked(e) != blocked || h.router.Blocked(h.call.Plan.Entries[1]) {
		t.Fatal("rate limit paused the wrong scope")
	}
	if blocked && firstChoice() != ep(2) {
		t.Fatal("paused endpoint was still planned")
	}
	now = now.Add(time.Nanosecond)
	if h.router.Blocked(e) {
		t.Fatal("retry time did not reopen the endpoint")
	}
}

func TestAlreadyPlannedCooldownSkipsAcquireAndRetryBudget(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 3, maxAttempts: 2, routing: routing.Config{RetryFloor: 1}})
		h.router.Block(h.call.Plan.Entries[1], routing.ScopeEndpoint, 30*time.Second)
		h.up.endpoint(1).script = []scripted{failure(routing.ScopeEgress, true)}
		h.up.endpoint(3).script = []scripted{whole(h)}
		h.run()
		if h.up.endpoint(1).sends != 1 || h.up.endpoint(2).acquired != 0 || h.up.endpoint(3).sends != 1 || h.w.status != 200 {
			t.Fatal("a dead route or paused entry consumed an attempt")
		}
		if !h.router.TakeRetry(sup(2)) || h.router.TakeRetry(sup(3)) {
			t.Fatal("retry budget was not spent only on the attempt sent")
		}
	})
}

func TestSupplierStaysExcludedForTheRequestAfterItsLocalPause(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		now := time.Now()
		h := newHarness(t, ctx, options{
			entries: 3,
			supplier: func(n int) int {
				if n == 3 {
					return 1
				}
				return n
			},
			routing: routing.Config{Now: func() time.Time { return now }},
		})
		h.up.endpoint(1).script = []scripted{failure(routing.ScopeSupplier, true)}
		h.up.endpoint(2).script = []scripted{func(context.Context) (*http.Response, *suppliers.Failure) {
			now = now.Add(2 * time.Minute)
			return nil, &suppliers.Failure{Scope: suppliers.ScopeEndpoint}
		}}
		h.up.endpoint(3).script = []scripted{whole(h)}
		h.run()
		if h.router.Blocked(h.call.Plan.Entries[0]) || h.up.endpoint(3).acquired != 0 || len(h.ended().outcome.Attempts) != 2 {
			t.Fatal("expired node-local pause reopened a supplier for the failed request")
		}
	})
}
