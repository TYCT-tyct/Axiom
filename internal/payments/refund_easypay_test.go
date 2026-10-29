package payments

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEasyPayRefundCoreTreatsAmbiguousHTTPAsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, body, state string
		status            int
	}{
		{"success", `{"code":1}`, "succeeded", http.StatusOK},
		{"refused", `{"code":0}`, "failed", http.StatusOK},
		{"HTML", `<html>bad gateway</html>`, "pending", http.StatusOK},
		{"503", `{"code":0}`, "pending", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, EasyPay{})
			gateway := &easyPayGateway{t: t, orders: map[string]easyPayGatewayOrder{}}
			var sends atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Query().Get("act") == "refund" {
					sends.Add(1)
					response.WriteHeader(tc.status)
					easyPayWrite(t, response, tc.body)
					return
				}
				gateway.ServeHTTP(response, request)
			}))
			t.Cleanup(server.Close)
			cfg := easyPayConfig(t, server.URL, server.Certificate())
			w.p.client = cfg.Client
			w.open(w.siteA, RoundRobin)
			in := w.input(w.siteA)
			in.Kind = "easypay"
			in.Settings = cfg.Settings
			in.RefundEnabled, in.UserRefund = true, true
			w.mustSave(siteScope(w.siteA), in)
			o := w.mustPlace(10*yuan, "easypay")
			response := w.callback(o.MerchantID, http.MethodGet, gateway.settle(o.ID, ""))
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body)
			}
			r := w.approve(w.mustRefund(o, o.Credits, "refund"))
			if r.State != tc.state {
				t.Fatal(r)
			}
			w.dueRefunds()
			w.dueRefunds()
			if sends.Load() != 1 {
				t.Fatal("easypay was resent", sends.Load())
			}
		})
	}
}
