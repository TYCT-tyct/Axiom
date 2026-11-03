package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/gateway"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// controlledCallPath assembles the production gateway and real supplier adapter, with the routing and supplier
// clocks controlled together. It leaves periodic loops stopped so each snapshot refresh is explicit.
func (w *callWorld) controlledCallPath(clock *testClock) (callNode, *routing.Router, *gateway.RouteDeps) {
	w.t.Helper()
	d, upstreams := w.controlledCallDeps(clock)
	site, ok := d.accounts.MainSite()
	if !ok {
		w.t.Fatal("main site missing")
	}
	// Periodic workers are stopped, so publish the same database-owned prices explicitly.
	if err := w.db.Tx(w.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return refreshSalePrices(w.t.Context(), tx, w.site, site.Currency)
	}); err != nil {
		w.t.Fatal(err)
	}
	ledger, err := billing.Start(w.t.Context(), w.db, pg.PlatformScope(), billing.Config{
		HolderTTL: time.Hour, ReapGrace: time.Hour, Lifetime: time.Hour,
		Segments: billing.NewSegmentPolicy(billing.DefaultSegments()),
	}, suppliers.CallWrites{})
	if err != nil {
		w.t.Fatal(err)
	}
	router, err := routing.New(routing.Config{Now: clock.Now, MaxStale: time.Hour, Rand: func() float64 { return 0.5 }})
	if err != nil {
		w.t.Fatal(err)
	}
	health, models := quality.New(nil), gateway.NewModelList()
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
	return n, router, routes
}

func (w *callWorld) callControlled(n callNode, body string) int {
	w.t.Helper()
	req := httptest.NewRequestWithContext(w.t.Context(), http.MethodPost, "http://api.main.example/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.richKey)
	rw := httptest.NewRecorder()
	httpserver.Public(n.handler, slog.New(slog.DiscardHandler), time.Minute).ServeHTTP(rw, req)
	if rw.Code != http.StatusOK {
		w.t.Logf("gateway refusal: %d %s", rw.Code, rw.Body.String())
	}
	if rw.Code != http.StatusOK && rw.Code != http.StatusNotFound && rw.Code != http.StatusServiceUnavailable && rw.Code != http.StatusBadGateway {
		w.t.Fatalf("call: %d %s", rw.Code, rw.Body.String())
	}
	return rw.Code
}

func TestGatewayRetryAfterRestsAndDownranksEndpoint(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	w.seedFailover()
	w.price()
	clock := &testClock{now: time.Now()}
	n, router, _ := w.controlledCallPath(clock)
	in := routing.Input{
		Models: []catalog.ModelID{catalog.ModelID(w.models["anthropic/claude-opus-5-5"])},
		Req:    &protocol.Request{Format: protocol.Messages, TextBytes: 1, Choices: 1, MaxOutput: 256, ServiceTiers: 1 << catalog.ServiceTierStandard},
	}
	before := mustPlan(t, router, in)
	first := before.Entries[0]
	w.supplier.mu.Lock()
	w.supplier.failures, w.supplier.retryAfter = []int{429}, "10"
	w.supplier.mu.Unlock()
	if status := w.callControlled(n, messagesCall); status != http.StatusOK {
		t.Fatalf("fallback status: %d", status)
	}
	if got := len(w.supplier.sent()); got != 2 {
		t.Fatalf("upstream attempts = %d, want 2", got)
	}
	if !router.Blocked(first) {
		t.Fatal("429 endpoint was not rested")
	}
	clock.Advance(10*time.Second - time.Nanosecond)
	if status := w.callControlled(n, messagesCall); status != http.StatusOK {
		t.Fatalf("call during pause: %d", status)
	}
	if !router.Blocked(first) {
		t.Fatal("Retry-After expired early")
	}
	during := mustPlan(t, router, in)
	for _, entry := range during.Entries {
		if entry.Endpoint == first.Endpoint {
			t.Fatal("paused endpoint was planned")
		}
	}
	clock.Advance(time.Nanosecond)
	if router.Blocked(first) {
		t.Fatal("endpoint did not reopen at Retry-After")
	}
	after := mustPlan(t, router, in)
	if len(after.Entries) != 2 || after.Entries[0].Endpoint == first.Endpoint {
		t.Fatal("reopened endpoint did not remain downranked")
	}
	clock.Advance(time.Minute)
	recovered := mustPlan(t, router, in)
	if recovered.Entries[0].Endpoint != first.Endpoint {
		t.Fatal("endpoint's weight did not recover")
	}
}

func TestGatewaySupplierBalanceRefusalRestsEveryEndpoint(t *testing.T) {
	t.Parallel()
	for _, recovery := range []string{"sync", "probe"} {
		t.Run(recovery, func(t *testing.T) {
			checkSupplierBalanceRecovery(t, recovery)
		})
	}
}

func mustPlan(t *testing.T, router *routing.Router, in routing.Input) routing.Plan {
	t.Helper()
	p, wait, err := router.Plan(in)
	if err != nil || wait != nil {
		t.Fatalf("plan: %v, %v", wait, err)
	}
	return p
}

func checkSupplierBalanceRecovery(t *testing.T, recovery string) {
	t.Helper()
	w := newCallWorld(t)
	w.seedFailover()
	w.price()
	clock := &testClock{now: time.Now()}
	n, _, routes := w.controlledCallPath(clock)
	w.supplier.mu.Lock()
	w.supplier.failures = []int{403}
	w.supplier.failureBody = `{"error":{"type":"new_api_error","code":"insufficient_user_quota"}}`
	w.supplier.mu.Unlock()
	if status := w.callControlled(n, messagesCall); status != http.StatusBadGateway {
		t.Fatalf("exhausted supplier: %d", status)
	}
	if got := len(w.supplier.sent()); got != 1 {
		t.Fatalf("same-supplier fallback made %d calls, want 1", got)
	}
	clock.Advance(6 * time.Second)
	if err := n.upstreams.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if status := w.callControlled(n, messagesCall); status != http.StatusServiceUnavailable {
		t.Fatalf("old balance recovered supplier: %d", status)
	}
	if got := len(w.supplier.sent()); got != 1 {
		t.Fatalf("rested supplier called %d times", got)
	}
	w.recoverSupplierFunds(n, clock, recovery)
	// Publishing another snapshot must preserve the live recovery state too.
	snap, list, count, err := routes.Build(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	routes.Publish(snap, list, count)
	if status := w.callControlled(n, messagesCall); status != http.StatusOK {
		t.Fatalf("recovered supplier: %d", status)
	}
}

func (w *callWorld) recoverSupplierFunds(n callNode, clock *testClock, recovery string) {
	t := w.t
	t.Helper()
	if recovery == "sync" {
		w.query("UPDATE suppliers.suppliers SET balance_observed_at = $1 RETURNING id::text", clock.Now())
		if err := n.upstreams.Reload(t.Context()); err != nil {
			t.Fatal(err)
		}
	} else {
		endpoints, err := n.upstreams.Routable()
		if err != nil {
			t.Fatal(err)
		}
		for _, ep := range endpoints {
			if ep.Upstream != "up-claude" {
				continue
			}
			result, err := n.upstreams.TestEndpoint(t.Context(), suppliers.TestRequest{Endpoint: ep.ID, Format: protocol.Messages})
			if err != nil || result.Outcome() != suppliers.TestPassed {
				t.Fatalf("probe: %v, %v", result.Outcome(), err)
			}
			break
		}
	}
}

func (w *callWorld) controlledCallDeps(clock *testClock) (callDeps, *suppliers.Service) {
	w.t.Helper()
	logger := slog.New(slog.DiscardHandler)
	exit := egress.NewForTesting(egresstest.Loopback(w.t))
	cfg := testGatewayConfig()
	meter := noop.NewMeterProvider().Meter("test")
	limits, err := gateway.New(cfg, meter)
	if err != nil {
		w.t.Fatal(err)
	}
	acct, err := accounts.NewService(w.db, accounts.Config{
		Platform: pg.PlatformScope(), Hashes: macKeys(w.t),
		Keyring: masterKeys(w.t), LookupSlot: limits.KeyLookupSlot,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if err := acct.Load(w.t.Context(), logger); err != nil {
		w.t.Fatal(err)
	}
	cat, err := catalog.Open(w.db, pg.PlatformScope(), catalog.Config{MaxStale: time.Hour})
	if err != nil {
		w.t.Fatal(err)
	}
	if err := cat.Refresh(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	upstreams, err := suppliers.New(suppliers.Config{
		DB: w.db, Scope: pg.PlatformScope(), Keys: masterKeys(w.t),
		Egress: exit, Clock: clock.Now, MaxStale: time.Hour,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return callDeps{
		db: w.db, keys: masterKeys(w.t), identity: []byte(strings.Repeat("i", 32)),
		accounts: acct, catalog: cat, egress: exit, gate: cfg, limits: limits, logger: logger, meter: meter,
	}, upstreams
}

func TestGatewayRetryAfterRestsCredential(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	if err := pgtest.Run(t.Context(), w.database.Superuser, "UPDATE suppliers.suppliers SET kind = 'sub2api'"); err != nil {
		t.Fatal(err)
	}
	group := uuid.MustParse(w.query("SELECT supplier_group_id::text FROM suppliers.credentials"))
	w.addFailureCredential(group, "second")
	w.price()
	clock := &testClock{now: time.Now()}
	n, _, _ := w.controlledCallPath(clock)
	w.supplier.mu.Lock()
	w.supplier.failures, w.supplier.retryAfter = []int{429}, "10"
	w.supplier.failureBody = `{"code":"rate_limit_exceeded"}`
	w.supplier.mu.Unlock()
	if status := w.callControlled(n, messagesCall); status != http.StatusOK {
		t.Fatalf("credential fallback: %d", status)
	}
	before := w.supplier.sent()
	if len(before) != 2 || before[0].auth == before[1].auth {
		t.Fatal("429 did not switch credential")
	}
	clock.Advance(10*time.Second - time.Nanosecond)
	if status := w.callControlled(n, messagesCall); status != http.StatusOK {
		t.Fatalf("call while credential rests: %d", status)
	}
	during := w.supplier.sent()
	if len(during) != 3 || during[2].auth == before[0].auth {
		t.Fatal("credential was picked before Retry-After")
	}
	clock.Advance(time.Nanosecond)
	if status := w.callControlled(n, messagesCall); status != http.StatusOK {
		t.Fatalf("call at Retry-After: %d", status)
	}
	after := w.supplier.sent()
	if len(after) != 4 || after[3].auth != before[0].auth {
		t.Fatal("credential did not return at Retry-After")
	}
}

func TestGatewaySupplierBalanceFiltersEachRequest(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	w.price()
	w.query("UPDATE suppliers.suppliers SET balance_native = 0.01 RETURNING id::text")
	n, _, _ := w.controlledCallPath(&testClock{now: time.Now()})
	// Changing the database after snapshot construction must not change affordability on the call path.
	w.query("UPDATE suppliers.suppliers SET balance_native = 100 RETURNING id::text")
	if status := w.callControlled(n, messagesCall); status != http.StatusServiceUnavailable {
		t.Fatalf("large request spent beyond the synced balance: %d", status)
	}
	if got := len(w.supplier.sent()); got != 0 {
		t.Fatalf("unaffordable request reached upstream %d times", got)
	}
	cheaper := strings.Replace(messagesCall, `"max_tokens":256`, `"max_tokens":16`, 1)
	if status := w.callControlled(n, cheaper); status != http.StatusOK {
		t.Fatalf("affordable request: %d", status)
	}
	if got := len(w.supplier.sent()); got != 1 {
		t.Fatalf("affordable request made %d attempts", got)
	}
}

func TestGatewayNewAPICostGapsAreWithheldBeforeRouting(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	w.price()
	// Keep the native schedule in the shape new-api actually publishes: no service tiers or tool fees.
	w.query(`WITH fresh AS (
  INSERT INTO suppliers.cost_schedules (supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
  SELECT supplier_id, endpoint_id, native_unit, (schedule - 'service_tiers') #- '{rates,web_search}', basis, 'native-gaps'
  FROM suppliers.cost_schedules RETURNING id, endpoint_id
 ) UPDATE suppliers.endpoints e SET cost_schedule_id = f.id FROM fresh f
 WHERE e.id = f.endpoint_id RETURNING e.id::text`)
	n, _, _ := w.controlledCallPath(&testClock{now: time.Now()})
	for _, body := range []string{
		messagesCall,
		`{"model":"claude-opus-5-5","max_tokens":16,"messages":[{"role":"user","content":"Hello"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":1}]}`,
	} {
		if status := w.callControlled(n, body); status != http.StatusNotFound {
			t.Fatalf("unpublished native gaps reached routing: %d", status)
		}
	}
	if got := len(w.supplier.sent()); got != 0 {
		t.Fatalf("unpublished native gaps made %d upstream calls", got)
	}
}
