package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// A cache read that costs less than input is when sticking pays; the first endpoint charges twice what the second does.
const (
	cacheDear  = `{"v":1,"currency":"USD","rates":{"input":"0.000002","output":"0.000004","cache_read":"0.000001"}}`
	cacheCheap = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_read":"0.0000005"}}`
)

// stickyRouter routes one model over ep(1), dear, and ep(2), cheap, by the cheapest strategy: with no session stuck,
// ep(2) is always first.
func stickyRouter(t *testing.T, ttl time.Duration) (*routing.Router, catalog.ModelID) {
	t.Helper()
	router, err := routing.New(routing.Config{})
	if err != nil {
		t.Fatal(err)
	}
	model := catalog.ModelID{1}
	all := routing.Of(protocol.Messages)
	var endpoints []routing.Endpoint
	for n, price := range []string{cacheDear, cacheCheap} {
		schedule, err := catalog.ParseSchedule([]byte(price))
		if err != nil {
			t.Fatal(err)
		}
		endpoints = append(endpoints, routing.Endpoint{
			ID: ep(n + 1), Supplier: sup(n + 1), Model: model, Enabled: true, BalanceOK: true, Egresses: 1, Class: routing.ClassOfficial,
			Policy: routing.PolicyNoRetention, Verified: true, Quality: 1, Confidence: 1, Health: 1, Throughput: 100,
			ServiceTiers: 1 << catalog.ServiceTierStandard, Native: all, Served: all, Caps: ^protocol.Capabilities(0),
			Context: 1_000_000, MaxOutput: 1000, Streams: true, Cancels: true, Price: routing.PriceRef{Version: uuid.UUID{1}, Schedule: schedule},
		})
	}
	snap, err := router.NewSnapshot(time.Now(), endpoints, []routing.Preset{{
		Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest, StickyTTL: ttl,
	}})
	if err != nil {
		t.Fatal(err)
	}
	router.Replace(snap)
	return router, model
}

// sessionCall is a call of the session as the handler would admit it: it reads the endpoint the session stuck to, and its
// route is the router's order of the harness's two endpoints. It is run by an executor that holds the router.
func sessionCall(t *testing.T, ctx context.Context, router *routing.Router, model catalog.ModelID, key routing.SessionKey) *harness {
	t.Helper()
	h := newHarness(t, ctx, options{entries: 2, mutate: func(c *Call) {
		c.Session = key
		c.sticky, _ = router.Stuck(key)
		plan, wait, err := router.Plan(routing.Input{Req: &c.Request, Models: []catalog.ModelID{model}, Sticky: c.sticky})
		if wait != nil || err != nil {
			t.Fatalf("plan: %v %v", wait, err)
		}
		byID := map[routing.EndpointID]routing.Entry{}
		for _, e := range c.Plan.Entries {
			byID[e.Endpoint] = e
		}
		c.Plan.Entries = nil
		for _, e := range plan.Entries {
			c.Plan.Entries = append(c.Plan.Entries, byID[e.Endpoint])
		}
		c.Plan.StickyTTL = plan.StickyTTL
	}})
	h.exec.Router = router
	return h
}

// served is the endpoint that answered a call, 0 for none.
func served(h *harness) (n int) {
	for i := 1; i <= 2; i++ {
		if h.up.endpoint(i).sends > 0 && h.w.status == http.StatusOK {
			n = i
		}
	}
	return n
}

// TestASessionStaysOnTheEndpointThatServedIt: the first call falls back to the dear endpoint, and the session follows it
// there, though the cheap one is first for anyone else; a session that never stuck still starts on the cheap one.
func TestASessionStaysOnTheEndpointThatServedIt(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		router, model := stickyRouter(t, 0)
		session, other := routing.SessionKey{1}, routing.SessionKey{2}

		first := sessionCall(t, ctx, router, model, session)
		first.up.eps[uuidOf(ep(2))].script = []scripted{failure(routing.ScopeEndpoint, false)}
		first.up.eps[uuidOf(ep(1))].script = []scripted{whole(first)}
		first.run()
		if got, ok := router.Stuck(session); !ok || got != ep(1) {
			t.Fatalf("after a fallback to ep(1) the session is stuck to %v, %v", got, ok)
		}

		second := sessionCall(t, ctx, router, model, session)
		second.up.eps[uuidOf(ep(1))].script = []scripted{whole(second)}
		second.up.eps[uuidOf(ep(2))].script = []scripted{whole(second)}
		second.run()
		if got := served(second); got != 1 || second.up.endpoint(2).sends != 0 {
			t.Errorf("the second call was served by endpoint %d (ep(2) sends: %d); the session's is 1", got, second.up.endpoint(2).sends)
		}

		stranger := sessionCall(t, ctx, router, model, other)
		stranger.up.eps[uuidOf(ep(2))].script = []scripted{whole(stranger)}
		stranger.run()
		if got := served(stranger); got != 2 {
			t.Errorf("a session that never stuck was served by endpoint %d, not the cheapest", got)
		}
	})
}

// TestASessionMovesWhenItsEndpointFails: the stuck endpoint fails and the call is served by the other, which the session
// moves to, so the call after goes there first.
func TestASessionMovesWhenItsEndpointFails(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		router, model := stickyRouter(t, 0)
		session := routing.SessionKey{1}
		router.Stick(session, ep(1), 0)

		moved := sessionCall(t, ctx, router, model, session)
		moved.up.eps[uuidOf(ep(1))].script = []scripted{failure(routing.ScopeEndpoint, false)}
		moved.up.eps[uuidOf(ep(2))].script = []scripted{whole(moved)}
		moved.run()
		if got := served(moved); got != 2 {
			t.Fatalf("the call was served by endpoint %d, not the fallback 2", got)
		}
		if got, _ := router.Stuck(session); got != ep(2) {
			t.Errorf("the session is stuck to %v after being served by ep(2)", got)
		}

		next := sessionCall(t, ctx, router, model, session)
		next.up.eps[uuidOf(ep(2))].script = []scripted{whole(next)}
		next.run()
		if got := served(next); got != 2 || next.up.endpoint(1).sends != 0 {
			t.Errorf("the next call was served by %d with %d sends on the failed endpoint", got, next.up.endpoint(1).sends)
		}
	})
}

// TestACallThatServesNothingWritesNothing: no endpoint answers, so no entry is made; and one that was there stays, as
// the failure says nothing of its cache.
func TestACallThatServesNothingWritesNothing(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		router, model := stickyRouter(t, 0)
		fresh, held := routing.SessionKey{1}, routing.SessionKey{2}
		router.Stick(held, ep(1), 0)
		for _, key := range []routing.SessionKey{fresh, held} {
			h := sessionCall(t, ctx, router, model, key) // every endpoint refuses by default
			h.run()
			if h.w.status == http.StatusOK {
				t.Fatalf("the call was served: %d", h.w.status)
			}
		}
		if _, ok := router.Stuck(fresh); ok {
			t.Error("a call that served nothing stuck its session")
		}
		if got, _ := router.Stuck(held); got != ep(1) {
			t.Errorf("a failed call moved the session to %v", got)
		}
	})
}

// TestTheStickyTimeIsTheRoutes: the time a preset gives rides the plan to the write.
func TestTheStickyTimeIsTheRoutes(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		router, model := stickyRouter(t, 2*time.Minute)
		session := routing.SessionKey{1}
		h := sessionCall(t, ctx, router, model, session)
		h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
		h.run()
		if _, ok := router.Stuck(session); !ok {
			t.Fatal("not stuck")
		}
		time.Sleep(time.Minute)
		if _, ok := router.Stuck(session); !ok {
			t.Error("the entry ended before the route's time")
		}
		time.Sleep(2 * time.Minute)
		if _, ok := router.Stuck(session); ok {
			t.Error("the entry outlived the route's time")
		}
	})
}

// withDearAlpha2 prices alpha-2 above its twin, so a session that is not stuck to alpha-2 starts on alpha-1: what a
// binding is worth reading shows up as which endpoint serves first.
func withDearAlpha2(t *testing.T) func(*rigOptions) {
	return func(o *rigOptions) {
		o.endpoints = func(name string, e *routing.Endpoint) {
			price := cacheCheap
			if name == "alpha-2" {
				price = cacheDear
			}
			schedule, err := catalog.ParseSchedule([]byte(price))
			if err != nil {
				t.Fatal(err)
			}
			e.Price.Schedule = schedule
		}
	}
}

// TestACompactedContextForgetsTheSession: a request that says its context was compacted is not sent to the endpoint
// the session stuck to, and the session is forgotten.
func TestACompactedContextForgetsTheSession(t *testing.T) {
	t.Parallel()
	r := newRig(t, withDearAlpha2(t))
	a := r.newAccount(100_000_000)
	firstOf := func(headers ...string) string {
		t.Helper()
		resp, body := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(messageBody("acme/alpha")), headers...)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		var out struct {
			Entries []string `json:"entries"`
			ID      string   `json:"id"`
		}
		if err := jsonx.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return out.Entries[0]
	}
	cheapest := uuid.UUID(r.endpoints["alpha-1"].ID).String()
	dear := uuid.UUID(r.endpoints["alpha-2"].ID).String()
	if got := firstOf(); got != cheapest {
		t.Fatalf("with nothing stuck the first is %s, not the cheapest", got)
	}
	session := r.calls()[0].Session
	r.router.Stick(session, r.endpoints["alpha-2"].ID, 0)
	if got := firstOf(); got != dear {
		t.Fatalf("a stuck session starts on %s, not alpha-2", got)
	}
	if got := firstOf("X-Claude-Code-Context-Compacted", "true"); got != cheapest {
		t.Errorf("a compacted context starts on %s, not the cheapest", got)
	}
	if _, ok := r.router.Stuck(session); ok {
		t.Error("the session was not forgotten")
	}
}
