package gateway

import (
	"cmp"
	"context"
	"net/http"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
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

func TestSupplierPauseWithoutRetryTimeEndsAfterThePauseTime(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		now := time.Now()
		h := failFirstEntry(t, ctx, suppliers.ScopeSupplier, &now)
		for _, e := range h.call.Plan.Entries[:2] {
			if !h.router.Blocked(e) {
				t.Fatal("supplier was not paused node-locally")
			}
		}
		now = now.Add(5*time.Second - time.Nanosecond) // the default Config.Pause
		if !h.router.Blocked(h.call.Plan.Entries[0]) {
			t.Fatal("supplier was released early")
		}
		now = now.Add(time.Nanosecond)
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
		{"supplier default", suppliers.ScopeSupplier, 0},
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
	h := newHarness(t, ctx, options{entries: 2, routing: routing.Config{MaxStale: time.Hour, Now: func() time.Time { return now }, Rand: func() float64 { return 0.5 }}})
	firstChoice := twoAlikeEndpoints(t, h.router)
	f := &suppliers.Failure{Scope: tc.scope, Cause: suppliers.RateLimited{RetryAfter: tc.after}}
	h.up.endpoint(1).script = []scripted{func(context.Context) (*http.Response, *suppliers.Failure) { return nil, f }, whole(h)}
	h.up.endpoint(2).script = []scripted{whole(h)}
	h.run()
	after := cmp.Or(tc.after, 5*time.Second) // the default Config.Pause, not the minute of Recovery
	now = now.Add(after - time.Nanosecond)
	e := h.call.Plan.Entries[0]
	blocked := tc.scope != suppliers.ScopeCredential
	if h.router.Blocked(e) != blocked || h.router.Blocked(h.call.Plan.Entries[1]) {
		t.Fatal("rate limit paused the wrong scope")
	}
	// A fixed random draw makes the damped (429) endpoint lose to its twin, paused or not, so even the credential scope
	// that pauses nothing shows the throttle.
	if firstChoice() != ep(2) {
		t.Fatal("the rate-limited endpoint was still preferred")
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

func TestFrozenRouteRecordsRestedSupplierAsBalance(t *testing.T) {
	for _, rested := range []int{1, 2} {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, options{entries: 2, noSlot: []int{1, 2}})
			// The refusal happens after planning, before this request acquires a credential.
			h.up.beforeAcquire = func(id suppliers.EndpointID) {
				if id == suppliers.EndpointID(ep(rested)) {
					h.up.endpoint(rested).resting = true
				}
			}
			h.run()
			if h.w.status != http.StatusServiceUnavailable {
				t.Fatalf("rested supplier mixed with busy credentials: status %d, want 503", h.w.status)
			}
			excluded := h.call.Plan.Record.Excluded
			if len(excluded) != 2 || excluded[rested-1].Reason != routing.SupplierBalance {
				t.Fatalf("frozen route exclusions: %+v", excluded)
			}
			if got := len(h.ended().outcome.Attempts); got != 0 {
				t.Fatalf("unacquired route made %d attempts", got)
			}
		})
	}
}

func TestProviderNoFallbackKeepsModelRecovery(t *testing.T) {
	for _, global := range []bool{false, true} {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, options{entries: 3})
			primary, alternate := catalog.ModelID{1}, catalog.ModelID{2}
			var candidates []routing.Endpoint
			for i, e := range h.call.Plan.Entries {
				model := []catalog.ModelID{primary, primary, alternate}[i]
				candidates = append(candidates, routing.Endpoint{
					ID: e.Endpoint, Supplier: e.Supplier, Model: model, Enabled: true, BalanceOK: true, Egresses: 1,
					Class: routing.ClassOfficial, Policy: routing.PolicyNoRetention, Health: 1, Quality: 1, Confidence: 1,
					Context: 100000, MaxOutput: e.MaxOutput, Streams: true, Native: routing.Of(h.call.Format), Served: routing.Of(h.call.Format), Caps: ^protocol.Capabilities(0),
					ServiceTiers: 1 << catalog.ServiceTierStandard, Price: e.Price, Target: e.Target,
				})
			}
			snap, err := h.router.NewSnapshot(time.Now(), candidates, []routing.Preset{{Strategy: routing.Cheapest, Classes: routing.AllClasses, Policies: routing.AllPolicies}})
			if err != nil {
				t.Fatal(err)
			}
			h.router.Replace(snap)
			h.call.Plan, _, err = h.router.Plan(routing.Input{Req: &h.call.Request, Models: []catalog.ModelID{primary, alternate}, Override: routing.Override{NoFallbacks: true, SortGlobal: global}})
			if err != nil {
				t.Fatal(err)
			}
			upstreamFailure := failing(routing.ScopeEndpoint, true, suppliers.Failure{Cause: suppliers.UpstreamError{}})
			h.up.endpoint(1).script = []scripted{func(context.Context) (*http.Response, *suppliers.Failure) { return nil, upstreamFailure }}
			h.up.endpoint(2).script = []scripted{whole(h)}
			h.up.endpoint(3).script = []scripted{whole(h)}
			h.run()
			got := [3]int{h.up.endpoint(1).sends, h.up.endpoint(2).sends, h.up.endpoint(3).sends}
			if got != ([3]int{1, 0, 1}) || h.w.status != http.StatusOK {
				t.Fatalf("global=%v: sends %v, status %d", global, got, h.w.status)
			}
		})
	}
}
