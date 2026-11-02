package payments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/alert"
)

// recordingSink keeps what the module raised so a test can assert the kind, key, fields and recovery.
type recordingSink struct {
	mu       sync.Mutex
	raised   []alert.Signal
	resolved []alert.Signal
}

func (s *recordingSink) Raise(kind, key, severity string, fields map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raised = append(s.raised, alert.Signal{Kind: kind, Key: key, Severity: severity, Fields: fields})
	return nil
}

func (s *recordingSink) RaiseOnce(_, kind, key, severity string, fields map[string]string) error {
	return s.Raise(kind, key, severity, fields)
}

func (s *recordingSink) Resolve(kind, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolved = append(s.resolved, alert.Signal{Kind: kind, Key: key, Resolved: true})
	return nil
}

func (s *recordingSink) of(kind string) (raised, resolved []alert.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sig := range s.raised {
		if sig.Kind == kind {
			raised = append(raised, sig)
		}
	}
	for _, sig := range s.resolved {
		if sig.Kind == kind {
			resolved = append(resolved, sig)
		}
	}
	return raised, resolved
}

// testTimer is a scheduled signal callback at the fake clock's time.
type testTimer struct {
	at time.Time
	f  func()
}

// runDue runs the callbacks due at the current fake time, including the ones they schedule.
func runDue(timers *[]testTimer, now func() time.Time) {
	for {
		idx := -1
		for i, timer := range *timers {
			if !timer.at.After(now()) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return
		}
		f := (*timers)[idx].f
		*timers = append((*timers)[:idx], (*timers)[idx+1:]...)
		f()
	}
}

// runTimers runs the callbacks due at the world's fake time.
func (w *world) runTimers() {
	w.t.Helper()
	runDue(&w.timers, w.p.now)
}

// newSignalNode is a Payments with only the signal state, for the tests that interleave two nodes.
func newSignalNode(instance string, alerts *recordingSink, now func() time.Time, timers *[]testTimer) *Payments {
	p := &Payments{
		alerts: alerts, instance: instance, now: now,
		signals: NewSignalState(),
	}
	p.after = func(d time.Duration, f func()) { *timers = append(*timers, testTimer{at: now().Add(d), f: f}) }
	return p
}

// TestVerifySignalReportsTheSmallWindow: a count below the old hard-coded floor is reported as the full count, so a
// rule threshold of 3 can see it.
func TestVerifySignalReportsTheSmallWindow(t *testing.T) {
	w := newTestWorld(t, fakePlugin{verify: func(Callback, string) (Notice, error) { return Notice{}, errors.New("bad signature") }})
	m := w.waiting()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.p.now = func() time.Time { return now }
	for range 3 {
		w.mustFailCallback(MerchantID(m.ID))
	}
	if raised, _ := w.alerts.of(signalVerifyFailed); len(raised) != 0 {
		t.Fatalf("raised before the window ended: %+v", raised)
	}
	now = now.Add(signalWindow)
	w.runTimers()
	raised, _ := w.alerts.of(signalVerifyFailed)
	if len(raised) != 1 || raised[0].Key != "test-node/callbacks" || raised[0].Severity != "warning" || raised[0].Fields["value"] != "3" {
		t.Fatalf("raised %+v", raised)
	}
}

// TestVerifySignalReportsTheWholeWindow: the value is the full count, not a hard-coded floor, so a rule threshold above
// the old floor sees the real size of the burst.
func TestVerifySignalReportsTheWholeWindow(t *testing.T) {
	w := newTestWorld(t, fakePlugin{verify: func(Callback, string) (Notice, error) { return Notice{}, errors.New("bad signature") }})
	m := w.waiting()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.p.now = func() time.Time { return now }
	for range 12 {
		w.mustFailCallback(MerchantID(m.ID))
	}
	now = now.Add(signalWindow)
	w.runTimers()
	raised, _ := w.alerts.of(signalVerifyFailed)
	if len(raised) != 1 || raised[0].Fields["value"] != "12" {
		t.Fatalf("raised %+v", raised)
	}
}

// TestSignalResolvesAfterAQuietWindow: a full minute with no event resolves the node's incident.
func TestSignalResolvesAfterAQuietWindow(t *testing.T) {
	w := newTestWorld(t, fakePlugin{verify: func(Callback, string) (Notice, error) { return Notice{}, errors.New("bad signature") }})
	m := w.waiting()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.p.now = func() time.Time { return now }
	w.mustFailCallback(MerchantID(m.ID))
	now = now.Add(signalWindow)
	w.runTimers()
	if raised, _ := w.alerts.of(signalVerifyFailed); len(raised) != 1 {
		t.Fatalf("raised %+v", raised)
	}
	now = now.Add(signalWindow)
	w.runTimers()
	if _, resolved := w.alerts.of(signalVerifyFailed); len(resolved) != 1 || resolved[0].Key != "test-node/callbacks" {
		t.Fatalf("resolved %+v", resolved)
	}
}

// TestQuerySignalReportsEveryCount: two unknown queries are reported as two, below both the old floor and a rule that
// asks for five.
func TestQuerySignalReportsEveryCount(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) {
		return Payment{}, errors.New("gateway down")
	}})
	w.waiting()
	o := w.mustPlace(yuan, "query-failure")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.p.now = func() time.Time { return now }
	for range 2 {
		if err := w.p.queryOrder(t.Context(), o, true); err == nil {
			t.Fatal("a failed query reported success")
		}
	}
	now = now.Add(signalWindow)
	w.runTimers()
	raised, _ := w.alerts.of(signalQueryFailed)
	if len(raised) != 1 || raised[0].Key != "test-node/queries" || raised[0].Fields["value"] != "2" {
		t.Fatalf("raised %+v", raised)
	}
}

// TestCallbackRateLimitSignalReportsEveryCount: three refusals are reported as three, so a rule threshold of three sees
// them even though the old floor was twenty.
func TestCallbackRateLimitSignalReportsEveryCount(t *testing.T) {
	w := newTestWorld(t, fakePlugin{})
	m := w.waiting()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.p.now = func() time.Time { return now }
	for range callbackMerchantLimit + 3 {
		w.callback(MerchantID(m.ID), http.MethodGet, nil)
	}
	now = now.Add(signalWindow)
	w.runTimers()
	raised, _ := w.alerts.of(signalRateLimited)
	if len(raised) != 1 || raised[0].Key != "test-node/callbacks" || raised[0].Fields["value"] != "3" {
		t.Fatalf("raised %+v", raised)
	}
}

// TestOrderRateLimitSignalReportsEveryCount: twenty-five order refusals are reported as twenty-five, above the old
// floor, because the rule chooses the threshold.
func TestOrderRateLimitSignalReportsEveryCount(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	w.limits("ip_order_limit = 1, pending_limit = 50")
	if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.90", "first"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.p.now = func() time.Time { return now }
	for i := range 25 {
		if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.90", fmt.Sprintf("refused-%d", i)); !errors.Is(err, ErrTooManyIPOrders) {
			t.Fatalf("refusal %d: %v", i, err)
		}
	}
	now = now.Add(signalWindow)
	w.runTimers()
	raised, _ := w.alerts.of(signalRateLimited)
	if len(raised) != 1 || raised[0].Key != "test-node/orders" || raised[0].Fields["value"] != "25" {
		t.Fatalf("raised %+v", raised)
	}
}

// TestSignalCountersAreSharedAcrossTheNodesInstances: the listener and the background poller are different Payments,
// but a rule threshold must see their combined minute, not two smaller reports.
func TestSignalCountersAreSharedAcrossTheNodesInstances(t *testing.T) {
	w := newTestWorld(t, fakePlugin{})
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	w.p.now = clock
	var otherTimers []testTimer
	other := newSignalNode("test-node", w.alerts, clock, &otherTimers)
	other.signals = w.p.signals
	for range 3 {
		w.p.queryFailed()
	}
	for range 2 {
		other.queryFailed()
	}
	now = now.Add(signalWindow)
	w.runTimers()
	runDue(&otherTimers, clock)
	raised, _ := w.alerts.of(signalQueryFailed)
	if len(raised) != 1 || raised[0].Key != "test-node/queries" || raised[0].Fields["value"] != "5" {
		t.Fatalf("raised %+v", raised)
	}
}

// TestTwoNodesDoNotResolveEachOther: the keys carry the node identity, so a quiet node's recovery cannot close an
// incident another node is still producing.
func TestTwoNodesDoNotResolveEachOther(t *testing.T) {
	w := newTestWorld(t, fakePlugin{})
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	w.p.now = clock
	w.p.instance = "node-a"
	var otherTimers []testTimer
	other := newSignalNode("node-b", w.alerts, clock, &otherTimers)
	runOther := func() { runDue(&otherTimers, clock) }
	w.p.verificationFailed()
	other.verificationFailed()
	now = now.Add(signalWindow)
	w.runTimers()
	runOther()
	raised, _ := w.alerts.of(signalVerifyFailed)
	if len(raised) != 2 || raised[0].Key != "node-a/callbacks" || raised[1].Key != "node-b/callbacks" {
		t.Fatalf("raised %+v", raised)
	}
	// Node B keeps failing in the second minute; node A is quiet.
	now = now.Add(signalWindow)
	other.verificationFailed()
	w.runTimers()
	runOther()
	if _, resolved := w.alerts.of(signalVerifyFailed); len(resolved) != 1 || resolved[0].Key != "node-a/callbacks" {
		t.Fatalf("node A's recovery touched another node: %+v", resolved)
	}
	// Node B is quiet in the third minute, so its own check resolves it.
	now = now.Add(signalWindow)
	runOther()
	now = now.Add(signalWindow)
	runOther()
	if _, resolved := w.alerts.of(signalVerifyFailed); len(resolved) != 2 || resolved[1].Key != "node-b/callbacks" {
		t.Fatalf("node B did not resolve: %+v", resolved)
	}
}

// TestMerchantUnreadableSignalRaisesOnceAndResolves: a merchant that cannot be decrypted signals once with this node's
// key, not once per request, and a later save that seals fresh secrets resolves it.
func TestMerchantUnreadableSignalRaisesOnceAndResolves(t *testing.T) {
	w := newTestWorld(t, fakePlugin{})
	m := w.waiting()
	key := "test-node/" + MerchantID(m.ID).TypeID()
	w.exec(fmt.Sprintf("UPDATE payments.merchant_configs SET nonce = set_byte(nonce, 0, (get_byte(nonce, 0) + 1) %% 256) WHERE merchant_id = '%s'", m.ID))
	if _, err := w.channel(MerchantID(m.ID)); err == nil {
		t.Fatal("a merchant with a changed nonce opened")
	}
	if _, err := w.channel(MerchantID(m.ID)); err == nil {
		t.Fatal("a merchant with a changed nonce opened")
	}
	raised, _ := w.alerts.of(signalMerchantUnreadable)
	if len(raised) != 1 || raised[0].Key != key || raised[0].Severity != "critical" {
		t.Fatalf("raised %+v", raised)
	}
	in := w.input(w.siteA)
	in.ID, in.Rev = MerchantID(m.ID), m.Rev
	w.mustSave(siteScope(w.siteA), in)
	if _, err := w.channel(MerchantID(m.ID)); err != nil {
		t.Fatal(err)
	}
	if _, resolved := w.alerts.of(signalMerchantUnreadable); len(resolved) != 1 || resolved[0].Key != key {
		t.Fatalf("resolved %+v", resolved)
	}
}

// mustFailCallback posts a callback that the fake channel refuses to verify.
func (w *world) mustFailCallback(id MerchantID) {
	w.t.Helper()
	if r := w.callback(id, http.MethodGet, nil); r.Code != http.StatusBadRequest {
		w.t.Fatalf("callback: %d %s", r.Code, r.Body)
	}
}
