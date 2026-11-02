package main

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/gateway"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

func (w *callWorld) healthCallPath(clock *testClock, mode quality.Mode) (callNode, *routing.Router, *quality.Monitor, *quality.Loop, *gateway.RouteDeps) {
	w.t.Helper()
	d, upstreams := w.controlledCallDeps(clock)
	site, _ := d.accounts.MainSite()
	if err := w.db.Tx(w.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error { return refreshSalePrices(w.t.Context(), tx, w.site, site.Currency) }); err != nil {
		w.t.Fatal(err)
	}
	ledger, err := billing.Start(w.t.Context(), w.db, pg.PlatformScope(), billing.Config{HolderTTL: time.Hour, ReapGrace: time.Hour, Lifetime: time.Hour}, suppliers.CallWrites{})
	if err != nil {
		w.t.Fatal(err)
	}
	router, err := routing.New(routing.Config{Now: clock.Now, MaxStale: time.Hour, RetryFloor: 20, Rand: func() float64 { return .5 }})
	if err != nil {
		w.t.Fatal(err)
	}
	health, loop, err := quality.Start(w.t.Context(), quality.Config{DB: w.db, Scope: pg.PlatformScope(), Mode: mode, Source: upstreams, Pauses: router, Now: clock.Now, Rand: func() float64 { return .5 }})
	if err != nil {
		w.t.Fatal(err)
	}
	models := gateway.NewModelList()
	n, err := newCallNode(d, router, upstreams, ledger, health, models)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { n.Drain(w.t.Context()) })
	if err := upstreams.Reload(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	routes := routeDeps(d, router, upstreams, health, &atomic.Bool{}, salePrices(w.db, d.accounts), models)
	snap, list, count, err := routes.Build(w.t.Context())
	if err != nil {
		w.t.Fatal(err)
	}
	routes.Publish(snap, list, count)
	return n, router, health, loop, routes
}

func TestGatewayHealthRemoveProbeRestore(t *testing.T) {
	for _, mode := range []quality.Mode{quality.Enforce, quality.Observe} {
		t.Run(string(mode), func(t *testing.T) { runGatewayHealth(t, mode) })
	}
}

func runGatewayHealth(t *testing.T, mode quality.Mode) {
	t.Helper()
	w := newCallWorld(t)
	w.seedFailover()
	w.price()
	clock := &testClock{now: time.Now()}
	n, router, health, loop, _ := w.healthCallPath(clock, mode)
	in := routing.Input{Models: []catalog.ModelID{catalog.ModelID(w.models["anthropic/claude-opus-5-5"])}, Req: &protocol.Request{Format: protocol.Messages, TextBytes: 1, Choices: 1, MaxOutput: 256, ServiceTiers: 1 << catalog.ServiceTierStandard}}
	first := mustPlan(t, router, in).Entries[0]
	w.injectHealthFailures(n, first)
	after := mustPlan(t, router, in)
	if mode == quality.Observe {
		if len(after.Entries) != 2 {
			t.Fatal("observe changed route")
		}
		clock.Advance(31 * time.Second)
		if err := loop.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if w.query(`SELECT kind FROM quality.health_events ORDER BY epoch LIMIT 1`) != "would_remove" {
			t.Fatal("observe event missing")
		}
		return
	}
	if len(after.Entries) != 1 || after.Entries[0].Endpoint == first.Endpoint {
		t.Fatal("next Plan did not remove endpoint")
	}
	if router.Blocked(first) {
		t.Fatal("reservation recheck refused a degraded last resort")
	}
	// Gateway observations use wall time; model slow fixture setup without sleeping.
	clock.Advance(-2 * time.Minute)
	w.checkHealthRecovery(clock, loop, health, first)
	if len(mustPlan(t, router, in).Entries) != 2 {
		t.Fatal("restored endpoint missing")
	}
}

func (w *callWorld) injectHealthFailures(n callNode, first routing.Entry) {
	w.t.Helper()
	keys := [2]string{w.richKey, w.fundedKey("broke@example.com", 10_000_000_000)}
	for i := range 5 {
		w.richKey = keys[i%2]
		var held *suppliers.Attempt
		if i%2 == 1 {
			var err error
			held, err = n.upstreams.Acquire(uuid.UUID(first.Endpoint))
			if err != nil {
				w.t.Fatal(err)
			}
		}
		w.supplier.mu.Lock()
		w.supplier.failures = []int{500, 0}
		w.supplier.mu.Unlock()
		// Distinct conversations keep successful fallback stickiness from moving the next injected failure.
		status := w.callControlled(n, strings.Replace(messagesCall, "Hello", "Hello "+strconv.Itoa(i), 1))
		if held != nil {
			held.Done()
		}
		if status != http.StatusOK {
			w.t.Fatalf("failure injection %d: fallback response %d, sent %d requests", i, status, len(w.supplier.sent()))
		}
	}
}

func (w *callWorld) checkHealthRecovery(clock *testClock, loop *quality.Loop, health *quality.Monitor, first routing.Entry) {
	w.t.Helper()
	opened, _ := health.Live(uuid.UUID(first.Endpoint), 2).Routing(clock.Now())
	clock.Advance(opened.Sub(clock.Now()) + 31*time.Second)
	w.supplier.mu.Lock()
	w.supplier.failures = []int{500}
	w.supplier.mu.Unlock()
	if err := loop.Tick(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	if err := loop.Drain(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	if at, _ := health.Live(uuid.UUID(first.Endpoint), 2).Routing(clock.Now()); at.IsZero() {
		w.t.Fatal("failed probe restored endpoint")
	}
	clock.Advance(time.Minute)
	if err := loop.Tick(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	if err := loop.Drain(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	if at, limit := health.Live(uuid.UUID(first.Endpoint), 2).Routing(clock.Now()); !at.IsZero() || limit != 1 {
		w.t.Fatalf("probe recovery %v ceiling %d", at, limit)
	}
	if got := w.query(`SELECT count(*)::text FROM suppliers.attempt_costs WHERE test`); got != "2" {
		w.t.Fatalf("probe costs: %s", got)
	}
	if got := w.query(`SELECT enabled::text||':'||cost_state FROM suppliers.endpoints WHERE id=$1`, uuid.UUID(first.Endpoint)); got != "true:known" {
		w.t.Fatalf("health changed supplier gates: %s", got)
	}
}

func TestGatewayDisabledEndpointIsNeverProbed(t *testing.T) {
	w := newCallWorld(t)
	w.price()
	clock := &testClock{now: time.Now()}
	n, router, health, loop, routes := w.healthCallPath(clock, quality.Enforce)
	endpoints, err := n.upstreams.Routable()
	if err != nil {
		t.Fatal(err)
	}
	id := endpoints[0].ID
	for i := range 5 {
		health.Observe(t.Context(), quality.Observation{Endpoint: id, Source: uuid.UUID{byte(i%2 + 1)}, Credential: uuid.UUID{byte(i%2 + 1)}, Sent: clock.Now(), Ended: clock.Now(), Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}})
	}
	if err := loop.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE suppliers.endpoints SET enabled=false WHERE id=$1`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := n.upstreams.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	snap, list, count, err := routes.Build(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	routes.Publish(snap, list, count)
	w.checkDisabledHealth(router, health, loop, clock, id, endpoints[0].Model)
}

func (w *callWorld) checkDisabledHealth(router *routing.Router, health *quality.Monitor, loop *quality.Loop, clock *testClock, id, model uuid.UUID) {
	w.t.Helper()
	before := len(w.supplier.sent())
	clock.Advance(20 * time.Minute)
	if err := loop.Tick(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	if err := loop.Drain(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	if len(w.supplier.sent()) != before {
		w.t.Fatal("disabled endpoint received probe")
	}
	if got := w.query(`SELECT enabled::text||':'||cost_state FROM suppliers.endpoints WHERE id=$1`, id); got != "false:known" {
		w.t.Fatal(got)
	}
	if at, _ := health.Live(id, 1).Routing(clock.Now()); at.IsZero() {
		w.t.Fatal("manual disable silently reset automatic state")
	}
	plan, _, err := router.Plan(routing.Input{Models: []catalog.ModelID{catalog.ModelID(model)}, Req: &protocol.Request{Format: protocol.Messages, Choices: 1, ServiceTiers: 1 << catalog.ServiceTierStandard}})
	if err == nil || len(plan.Entries) != 0 {
		w.t.Fatal("disabled endpoint remained in route")
	}
}

func TestGatewayHealthDefersProbesDuringFundsRefusal(t *testing.T) {
	w := newCallWorld(t)
	w.seedFailover()
	w.price()
	clock := &testClock{now: time.Now()}
	n, router, health, loop, _ := w.healthCallPath(clock, quality.Enforce)
	in := routing.Input{Models: []catalog.ModelID{catalog.ModelID(w.models["anthropic/claude-opus-5-5"])}, Req: &protocol.Request{Format: protocol.Messages, TextBytes: 1, Choices: 1, MaxOutput: 256, ServiceTiers: 1 << catalog.ServiceTierStandard}}
	first := mustPlan(t, router, in).Entries[0]
	w.injectHealthFailures(n, first)
	w.supplier.mu.Lock()
	w.supplier.failures = []int{403}
	w.supplier.failureBody = `{"error":{"type":"new_api_error","code":"insufficient_user_quota"}}`
	w.supplier.mu.Unlock()
	if status := w.callControlled(n, messagesCall); status != http.StatusBadGateway {
		t.Fatalf("balance refusal: %d", status)
	}
	sent := len(w.supplier.sent())
	clock.Advance(time.Minute)
	if err := loop.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := loop.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.supplier.sent()) != sent {
		t.Fatal("automatic probe bypassed supplier balance pause")
	}
	if got := w.query(`SELECT count(*)::text FROM suppliers.attempt_costs WHERE test`); got != "0" {
		t.Fatalf("paused probes wrote costs: %s", got)
	}
	if got := w.query(`SELECT count(*)::text FROM quality.endpoint_health WHERE probe_lease_until>0`); got != "0" {
		t.Fatalf("paused probes took leases: %s", got)
	}
	w.recoverSupplierFunds(n, clock, "sync")
	if err := loop.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := loop.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.supplier.sent()) != sent+1 {
		t.Fatal("balance recovery did not resume probe")
	}
	if at, _ := health.Live(uuid.UUID(first.Endpoint), 2).Routing(clock.Now()); !at.IsZero() {
		t.Fatal("resumed probe did not restore endpoint")
	}
}
