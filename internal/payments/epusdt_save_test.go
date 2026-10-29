package payments

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
)

func TestEpusdtProbeLeavesPaymentsAndDisableAvailable(t *testing.T) {
	w := newTestWorld(t, EasyPay{})
	gateway := &easyPayGateway{t: t, orders: map[string]easyPayGatewayOrder{}}
	var block atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		if block.CompareAndSwap(true, false) {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		gateway.ServeHTTP(wr, r)
	}))
	t.Cleanup(server.Close)
	// Release the pending HTTP request even if a regression makes a payment assertion fail.
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	w.p.client = easyPayConfig(t, server.URL, server.Certificate()).Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA, MethodInput{Method: "usdt.tron", Upstream: "usdt.tron", Enabled: true})
	in.Kind, in.Settings = "easypay", epusdtConfig(t, server.URL)
	m := w.mustSave(siteScope(w.siteA), in)
	o, _, err := w.placeAs(w.accountA(), CreateOrder{Site: w.site(w.siteA, "CNY"), Account: w.account, User: w.user, Method: "usdt.tron", Credits: 100 * yuan, Key: "probe-lock", ClientIP: netip.MustParseAddr("203.0.113.7"), ReturnURL: "https://console.example.com/balance"})
	if err != nil {
		t.Fatal(err)
	}
	gateway.mu.Lock()
	gateway.orders[o.ID.TypeID()] = easyPayGatewayOrder{money: "100.0000", trade: "T-probe-lock"}
	gateway.mu.Unlock()
	in.ID, in.Rev, in.Secrets = MerchantID(m.ID), m.Rev, nil
	in.Name = "waiting for query"
	block.Store(true)
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); _, err := w.save(siteScope(w.siteA), in); done <- err }()
	defer func() { unblock(); <-finished }()
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	if response := w.callback(o.MerchantID, http.MethodGet, gateway.settle(o.ID, "100.0000")); response.Code != http.StatusOK {
		t.Fatal("save probe blocked payment confirmation", response.Code, response.Body)
	}
	w.assertCredit(o, 100*yuan)
	// A concurrent disable changes the revision while the probe is still waiting.
	disabled := in
	disabled.Enabled = false
	disabled.Name = m.Name
	saved := w.mustSave(siteScope(w.siteA), disabled)
	if saved.Enabled {
		t.Fatal("merchant remained enabled")
	}
	// The pending probe completes only after payment and emergency disable have committed.
	unblock()
	if err := <-done; !errors.Is(err, ErrStale) {
		t.Fatal("probe overwrote concurrent disable", err)
	}
}
