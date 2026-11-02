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

// TestVerifyFailureSignalFiresAtItsFloor: a signature failure is counted per minute, and the signal is raised only when
// the window reaches the floor; a later quiet window resolves it.
func TestVerifyFailureSignalFiresAtItsFloor(t *testing.T) {
	w := newTestWorld(t, fakePlugin{verify: func(Callback, string) (Notice, error) { return Notice{}, errors.New("bad signature") }})
	m := w.waiting()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.p.now = func() time.Time { return now }
	for range verifyFloor - 1 {
		w.mustFailCallback(MerchantID(m.ID))
	}
	if raised, _ := w.alerts.of(signalVerifyFailed); len(raised) != 0 {
		t.Fatalf("raised below the floor: %+v", raised)
	}
	w.mustFailCallback(MerchantID(m.ID))
	raised, resolved := w.alerts.of(signalVerifyFailed)
	if len(raised) != 1 || raised[0].Key != "callbacks" || raised[0].Severity != "warning" || raised[0].Fields["value"] != fmt.Sprint(verifyFloor) {
		t.Fatalf("raised %+v", raised)
	}
	if len(resolved) != 0 {
		t.Fatalf("resolved while the window was still at its floor: %+v", resolved)
	}
	now = now.Add(signalWindow)
	w.mustFailCallback(MerchantID(m.ID))
	now = now.Add(signalWindow)
	w.mustFailCallback(MerchantID(m.ID))
	if _, resolved = w.alerts.of(signalVerifyFailed); len(resolved) != 1 || resolved[0].Key != "callbacks" {
		t.Fatalf("resolved %+v", resolved)
	}
}

// TestQueryFailureSignalFiresAtItsFloor: unknown merchant queries are counted in the same window as signature failures.
func TestQueryFailureSignalFiresAtItsFloor(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) {
		return Payment{}, errors.New("gateway down")
	}})
	w.waiting()
	o := w.mustPlace(yuan, "query-failure")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.p.now = func() time.Time { return now }
	for range queryFloor - 1 {
		if err := w.p.queryOrder(t.Context(), o, true); err == nil {
			t.Fatal("a failed query reported success")
		}
	}
	if raised, _ := w.alerts.of(signalQueryFailed); len(raised) != 0 {
		t.Fatalf("raised below the floor: %+v", raised)
	}
	if err := w.p.queryOrder(t.Context(), o, true); err == nil {
		t.Fatal("a failed query reported success")
	}
	raised, _ := w.alerts.of(signalQueryFailed)
	if len(raised) != 1 || raised[0].Key != "queries" || raised[0].Fields["value"] != fmt.Sprint(queryFloor) {
		t.Fatalf("raised %+v", raised)
	}
}

// TestCallbackRateLimitSignalFiresAtItsFloor: refusals from the callback budget are counted per minute, separately from
// signature failures.
func TestCallbackRateLimitSignalFiresAtItsFloor(t *testing.T) {
	w := newTestWorld(t, fakePlugin{})
	m := w.waiting()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.p.now = func() time.Time { return now }
	for range callbackMerchantLimit + rateFloor - 1 {
		w.callback(MerchantID(m.ID), http.MethodGet, nil)
	}
	if raised, _ := w.alerts.of(signalRateLimited); len(raised) != 0 {
		t.Fatalf("raised below the floor: %+v", raised)
	}
	if r := w.callback(MerchantID(m.ID), http.MethodGet, nil); r.Code != http.StatusTooManyRequests {
		t.Fatalf("over the merchant budget: %d %s", r.Code, r.Body)
	}
	raised, _ := w.alerts.of(signalRateLimited)
	if len(raised) != 1 || raised[0].Key != "callbacks" || raised[0].Fields["value"] != fmt.Sprint(rateFloor) {
		t.Fatalf("raised %+v", raised)
	}
}

// TestOrderRateLimitSignalFiresAtItsFloor: refusals from an IP group's order budget carry their own key.
func TestOrderRateLimitSignalFiresAtItsFloor(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	w.limits("ip_order_limit = 1, pending_limit = 50")
	if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.90", "first"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w.p.now = func() time.Time { return now }
	for i := range rateFloor - 1 {
		if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.90", fmt.Sprintf("refused-%d", i)); !errors.Is(err, ErrTooManyIPOrders) {
			t.Fatalf("refusal %d: %v", i, err)
		}
	}
	if raised, _ := w.alerts.of(signalRateLimited); len(raised) != 0 {
		t.Fatalf("raised below the floor: %+v", raised)
	}
	if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.90", "refused-last"); !errors.Is(err, ErrTooManyIPOrders) {
		t.Fatal(err)
	}
	raised, _ := w.alerts.of(signalRateLimited)
	if len(raised) != 1 || raised[0].Key != "orders" || raised[0].Fields["value"] != fmt.Sprint(rateFloor) {
		t.Fatalf("raised %+v", raised)
	}
}

// TestMerchantUnreadableSignalRaisesOnceAndResolves: a merchant that cannot be decrypted signals once, not once per
// request, and a later save that seals fresh secrets resolves it.
func TestMerchantUnreadableSignalRaisesOnceAndResolves(t *testing.T) {
	w := newTestWorld(t, fakePlugin{})
	m := w.waiting()
	w.exec(fmt.Sprintf("UPDATE payments.merchant_configs SET nonce = set_byte(nonce, 0, (get_byte(nonce, 0) + 1) %% 256) WHERE merchant_id = '%s'", m.ID))
	if _, err := w.channel(MerchantID(m.ID)); err == nil {
		t.Fatal("a merchant with a changed nonce opened")
	}
	if _, err := w.channel(MerchantID(m.ID)); err == nil {
		t.Fatal("a merchant with a changed nonce opened")
	}
	raised, _ := w.alerts.of(signalMerchantUnreadable)
	if len(raised) != 1 || raised[0].Key != MerchantID(m.ID).TypeID() || raised[0].Severity != "critical" {
		t.Fatalf("raised %+v", raised)
	}
	in := w.input(w.siteA)
	in.ID, in.Rev = MerchantID(m.ID), m.Rev
	w.mustSave(siteScope(w.siteA), in)
	if _, err := w.channel(MerchantID(m.ID)); err != nil {
		t.Fatal(err)
	}
	if _, resolved := w.alerts.of(signalMerchantUnreadable); len(resolved) != 1 || resolved[0].Key != MerchantID(m.ID).TypeID() {
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
