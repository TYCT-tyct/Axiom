package gateway

import (
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/billing"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
	"axiom/internal/routing"
)

func stickyValue(t *testing.T, cache *kv.Client, key string) routing.EndpointID {
	t.Helper()
	values, err := cache.Do(t.Context(), kv.Precheck, kv.Get(key))
	if err != nil {
		t.Fatal(err)
	}
	return routing.StickyEndpoint(values[0])
}

func stickyCalls(t *testing.T, reader *sdkmetric.ManualReader, use kv.Use) int64 {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "axiom.kv.calls" {
				continue
			}
			sum := m.Data.(metricdata.Sum[int64])
			for _, point := range sum.DataPoints {
				if v, _ := point.Attributes.Value("use"); v.AsString() == string(use) {
					total += point.Value
				}
			}
		}
	}
	return total
}

// The second router is deliberately populated with a conflicting local entry: it must only use the shared precheck.
func TestStickyAcrossNodesInOnePrecheck(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	namespace, clock := kvtest.Namespace(), newFrozenClock()
	a, b := node(t, server, namespace, clock), node(t, server, namespace, clock)
	a.Router, _ = stickyRouter(t, time.Hour)
	b.Router, _ = stickyRouter(t, time.Hour)
	reader := sdkmetric.NewManualReader()
	b.KV = server.OpenIn(t, namespace, kv.Deps{
		Now:   clock.Now,
		Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"),
	})
	clock.midWindow(a.KV, a.Limits.cfg.RateWindow)
	first := &Call{Session: routing.SessionKey{1}, Plan: routing.Plan{StickyTTL: time.Hour}}
	a.precheck(t.Context(), rateSite, callDimension, "local", "one", first, false)
	delivered := &session{
		x: &Executor{Router: a.Router}, c: first,
		ends: []billing.AttemptResult{{Result: billing.Aggregated}}, tries: []try{{entry: routing.Entry{Endpoint: ep(1)}}},
	}
	delivered.stick()
	waitFor(t, "the background sticky write", func() bool { return stickyValue(t, b.KV, first.stickyKey) == ep(1) })
	b.Router.Stick(first.Session, ep(2), 0)
	next := &Call{Session: first.Session}
	before := stickyCalls(t, reader, kv.Precheck)
	if rate := b.precheck(t.Context(), rateSite, callDimension, "local", "one", next, false); rate != 2 || next.sticky != ep(1) {
		t.Fatalf("second node: rate %d, sticky %v", rate, next.sticky)
	}
	if got := stickyCalls(t, reader, kv.Precheck) - before; got != 1 {
		t.Fatalf("precheck made %d accesses", got)
	}
	// GET misses are successes for the count, not unavailable batches.
	missing := &Call{Session: routing.SessionKey{2}}
	if rate := b.precheck(t.Context(), rateSite, callDimension, "local", "one", missing, false); rate != 3 || missing.sticky != (routing.EndpointID{}) {
		t.Fatalf("missing binding: rate %d, sticky %v", rate, missing.sticky)
	}
	before = stickyCalls(t, reader, kv.Precheck)
	b.precheck(t.Context(), rateSite, callDimension, "local", "one", next, true)
	if got := stickyCalls(t, reader, kv.Precheck) - before; got != 1 {
		t.Fatalf("compacted precheck made %d accesses", got)
	}
	if next.sticky != (routing.EndpointID{}) || stickyValue(t, a.KV, first.stickyKey) != (routing.EndpointID{}) {
		t.Fatal("compaction retained the shared binding")
	}
}

// TestSharedCompactedHeader: the header is what makes the precheck of the call delete the shared binding instead of
// reading it, so the call starts afresh and the binding that follows it names the endpoint that served.
func TestSharedCompactedHeader(t *testing.T) {
	t.Parallel()
	cache := kvtest.New(t).Open(t, kv.Deps{})
	r := newRig(t, withDearAlpha2(t), func(o *rigOptions) { o.cache = cache })
	a := r.newAccount(100_000_000)
	if resp, body := r.message(a, "acme/alpha"); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	key := r.calls()[0].stickyKey
	// The first call offered its own binding from the background. A later offer on the same queue is a barrier for it,
	// so the binding below is what the compacted request reads rather than one the queue may replace.
	barrier := routing.StickyKey(cache, uuid.UUID{}, routing.SessionKey{9})
	cache.Later(uuid.UUID{9}, kv.Set(barrier, "sent", time.Hour))
	waitFor(t, "the first call's binding", func() bool {
		values, err := cache.Do(t.Context(), kv.Precheck, kv.Get(barrier))
		if err != nil {
			t.Fatal(err)
		}
		text, err := values[0].String()
		return err == nil && text == "sent"
	})
	if _, err := cache.Do(t.Context(), kv.StickyWrite, r.router.StickyWrite(key, r.endpoints["alpha-2"].ID, 0)); err != nil {
		t.Fatal(err)
	}
	resp, body := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(messageBody("acme/alpha")),
		"X-Claude-Code-Context-Compacted", "true")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var out struct {
		Entries []string `json:"entries"`
	}
	if err := jsonx.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if cheapest := uuid.UUID(r.endpoints["alpha-1"].ID).String(); out.Entries[0] != cheapest {
		t.Fatalf("a compacted request started on %s, not the cheapest: the shared binding was not deleted", out.Entries[0])
	}
	waitFor(t, "the binding the compacted request wrote", func() bool {
		return stickyValue(t, cache, key) == r.endpoints["alpha-1"].ID
	})
}

func TestStickyOutageDoesNotUseLocalBindings(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	h := node(t, server, kvtest.Namespace(), newFrozenClock())
	h.Router, _ = stickyRouter(t, time.Hour)
	call := &Call{Session: routing.SessionKey{1}}
	h.Router.Stick(call.Session, ep(1), 0)
	if err := h.KV.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rate := h.precheck(t.Context(), rateSite, callDimension, "local", "one", call, false); rate != 1 || call.sticky != (routing.EndpointID{}) {
		t.Fatalf("outage: rate %d, sticky %v", rate, call.sticky)
	}
	s := &session{
		x: &Executor{Router: h.Router}, c: call,
		ends: []billing.AttemptResult{{Result: billing.Committed}}, tries: []try{{entry: routing.Entry{Endpoint: ep(2)}}},
	}
	s.stick()
	if got, _ := h.Router.Stuck(call.Session); got != ep(1) {
		t.Fatal("a shared write fell back to the local table")
	}
}

// Using the same endpoint never renews the original expiry, even when the preset supplies a different TTL.
func TestStickyDoesNotRenewOnUse(t *testing.T) {
	t.Parallel()
	reader := sdkmetric.NewManualReader()
	// The life is read as the admin, whom only a container of the test's own has (README「权限」).
	server := kvtest.Own(t)
	cache := server.Open(t, kv.Deps{Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")})
	router, _ := stickyRouter(t, time.Hour)
	key := routing.StickyKey(cache, uuid.UUID{1}, routing.SessionKey{1})
	if _, err := cache.Do(t.Context(), kv.StickyWrite, router.StickyWrite(key, ep(1), time.Hour)); err != nil {
		t.Fatal(err)
	}
	call := &Call{sticky: ep(1), stickyCache: cache, stickyKey: key, Plan: routing.Plan{StickyTTL: time.Minute}}
	s := &session{
		x: &Executor{Router: router}, c: call,
		ends: []billing.AttemptResult{{Result: billing.Committed}}, tries: []try{{entry: routing.Entry{Endpoint: ep(1)}}},
	}
	for range 100 {
		s.stick()
	}
	// A later binding on the same FIFO queue is a barrier for all offers above. Its different endpoint also proves
	// that a successful fallback writes, while the original key keeps its hour rather than the new preset's minute.
	barrier := routing.StickyKey(cache, uuid.UUID{1}, routing.SessionKey{2})
	call.stickyKey = barrier
	s.tries[0].entry.Endpoint = ep(2)
	s.stick()
	waitFor(t, "the changed endpoint", func() bool { return stickyValue(t, cache, barrier) == ep(2) })
	life, err := server.Do(t, t.Context(), server.Admin.B().Pttl().Key(key).Build())[0].ToInt64()
	if err != nil || life <= time.Minute.Milliseconds() {
		t.Fatalf("using the binding replaced its original expiry: %d, %v", life, err)
	}
	waitFor(t, "the completed write's metric", func() bool { return stickyCalls(t, reader, kv.StickyWrite) >= 2 })
	if n := stickyCalls(t, reader, kv.StickyWrite); n != 2 {
		t.Fatalf("initial binding and endpoint change made %d batches, want 2", n)
	}
}
