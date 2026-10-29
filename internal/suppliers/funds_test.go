package suppliers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

func TestSupplierRefusalSurvivesReloadUntilFreshFunds(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	ep := w.svc.snap.Load().endpoints[w.endpoint]
	original := ep.funds
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	a.rest(Failure{Scope: ScopeSupplier, Cause: SupplierBalanceLow{}})
	a.Done()
	for _, elapsed := range []time.Duration{0, 3 * time.Hour} {
		w.clock.advance(elapsed)
		if err := w.svc.Reload(t.Context()); err != nil {
			t.Fatal(err)
		}
		if original.CanPay(platform.Exact{}) {
			t.Fatal("old balance or elapsed time cleared the refusal")
		}
		if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrSupplierResting) {
			t.Fatalf("rested supplier: %v", err)
		}
	}
	zeroObserved := w.clock.now().Add(-time.Second)
	if w.svc.fundsOf(w.supplier, platform.Exact{}, &zeroObserved).CanPay(platform.Exact{}) {
		t.Fatal("a fresh zero balance cleared the refusal")
	}
	oldObserved := w.clock.now()
	// Equal timestamps are not newer evidence.
	original.rest(w.clock.now())
	if w.svc.fundsOf(w.supplier, original.Balance, &oldObserved).CanPay(platform.Exact{}) {
		t.Fatal("a balance observed at the refusal cleared it")
	}
	w.clock.advance(time.Second)
	// A newer sync, even with an unchanged positive balance, is recovery evidence.
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.suppliers SET balance_observed_at = $1 WHERE id = $2", w.clock.now(), w.supplier)
		return err
	})
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !original.CanPay(platform.Exact{}) {
		t.Fatal("new positive sync did not recover the shared state")
	}
	a, err = w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	a.Done()
}

func TestSupplierFundsAreShared(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	ep := w.svc.snap.Load().endpoints[w.endpoint]
	ep.funds.rest(w.clock.now())
	other := w.svc.fundsOf(w.supplier, ep.funds.Balance, nil)
	if other.CanPay(platform.Exact{}) {
		t.Fatal("another endpoint did not share the supplier's refusal")
	}
}

func TestExplicitProbeRecoversSupplierOnlyOnCompleteSuccess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"complete", "data: {\"id\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true},
		{"broken", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, 4)
			server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
				rw.Header().Set("Content-Type", "text/event-stream")
				_, _ = rw.Write([]byte(tc.body)) //nolint:errcheck // the test consumes the response
			}))
			defer server.Close()
			w.pointAt(server.URL)
			ep := w.svc.snap.Load().endpoints[w.endpoint]
			ep.funds.rest(w.clock.now())
			result, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Outcome() == TestPassed; got != tc.want {
				t.Fatalf("probe outcome: %v", result.Outcome())
			}
			if got := ep.funds.CanPay(platform.Exact{}); got != tc.want {
				t.Fatalf("funds recovered = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBalanceRefusalRequestsAreCoalescedAndRateLimited(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	ep := w.svc.snap.Load().endpoints[w.endpoint]
	for range 10 {
		ep.funds.rest(w.clock.now())
	}
	if err := w.svc.RefreshBalances(t.Context()); err != nil {
		t.Fatal(err)
	}
	rt := ep.funds.state
	if rt.pending.Load() || rt.lastRequest != w.clock.now() {
		t.Fatal("refusal did not request a background balance sync")
	}
	ep.funds.rest(w.clock.now())
	w.clock.advance(time.Minute - time.Nanosecond)
	if err := w.svc.RefreshBalances(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !rt.pending.Load() {
		t.Fatal("the balance request bypassed the per-supplier rate limit")
	}
	w.clock.advance(time.Nanosecond)
	if err := w.svc.RefreshBalances(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rt.pending.Load() {
		t.Fatal("the next balance request was not scheduled")
	}
}

// Keep classification separate from the pause: only a supplier balance refusal rests the account.
func TestOtherSupplierFailuresDoNotRestFunds(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Done()
	for _, f := range []Failure{{Scope: ScopeSupplier, Cause: adapter.Unknown{Status: 503}}, {Scope: ScopeCredential, Cause: SupplierBalanceLow{}}} {
		a.rest(f)
		if !a.ep.funds.CanPay(platform.Exact{}) {
			t.Fatal("a failure without a supplier balance refusal rested funds")
		}
	}
}

func TestLateProbeDoesNotClearNewerSupplierRefusal(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		close(started)
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		rw.Header().Set("Content-Type", "text/event-stream")
		_, _ = rw.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")) //nolint:errcheck // consumed by the test
	}))
	defer server.Close()
	w.pointAt(server.URL)
	funds := w.svc.snap.Load().endpoints[w.endpoint].funds
	funds.rest(w.clock.now())
	done := make(chan error, 1)
	go func() {
		result, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
		if err == nil && result.Outcome() != TestPassed {
			err = errors.New("probe did not finish successfully")
		}
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("probe ended before sending: %v", err)
	case <-t.Context().Done():
		t.Fatal("probe did not start")
	}
	// A successful sync may recover the supplier while the old probe is still reading. A later refusal must keep a
	// different version even at the same clock tick (no ABA after the recovery).
	observed := w.clock.now().Add(time.Second)
	w.svc.fundsOf(w.supplier, funds.Balance, &observed)
	funds.rest(w.clock.now())
	w.svc.fundsOf(w.supplier, funds.Balance, &observed) // reloading the same observation is not another sync
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if funds.CanPay(platform.Exact{}) {
		t.Fatal("an older probe cleared a newer refusal at the same clock tick")
	}
}

func TestFailedBalanceRequestKeepsItsSignal(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	funds := w.svc.snap.Load().endpoints[w.endpoint].funds
	funds.rest(w.clock.now())
	w.db.Close()
	if err := w.svc.RefreshBalances(t.Context()); err == nil {
		t.Fatal("closed database accepted the sync request")
	}
	if !funds.state.pending.Load() || !funds.state.lastRequest.IsZero() {
		t.Fatal("failed write consumed the signal or its rate-limit window")
	}
}
