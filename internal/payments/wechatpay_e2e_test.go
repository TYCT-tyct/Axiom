package payments

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

type wpE2EOrder struct {
	total  int64
	trade  string
	state  string
	closed bool
}

type wpE2ERefund struct {
	order         string
	total, refund int64
}

type wpE2EState struct {
	mu         sync.Mutex
	orders     map[string]*wpE2EOrder
	refunds    map[string]*wpE2ERefund
	refundMode string // "", "unknown" or "success"
}

func (s *wpE2EState) settle(id string, total int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.orders[id]
	if o == nil {
		o = &wpE2EOrder{}
		s.orders[id] = o
	}
	o.total, o.trade, o.state = total, "T-"+id, "SUCCESS"
}

func (s *wpE2EState) isClosed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.orders[id]
	return o != nil && o.closed
}

func wpE2ERefundJSON(id string, refund *wpE2ERefund, status string) []byte {
	return wpTestJSONBody(map[string]any{
		"refund_id": "WX-" + id, "out_refund_no": id, "out_trade_no": refund.order,
		"transaction_id": "T-" + refund.order, "status": status,
		"amount": map[string]any{"total": refund.total, "refund": refund.refund, "currency": "CNY"},
	})
}

//nolint:cyclop,gocognit // one branch per gateway method; each branch settles the state the next step reads
func (s *wpE2EState) respond(t *testing.T, r *http.Request, body []byte) (int, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v3/pay/transactions/native":
		var request struct {
			OutTradeNo string `json:"out_trade_no"`
			Amount     struct {
				Total int64 `json:"total"`
			} `json:"amount"`
		}
		if err := jsonx.Unmarshal(body, &request); err != nil {
			t.Errorf("native body: %v", err)
			return http.StatusBadRequest, nil
		}
		s.orders[request.OutTradeNo] = &wpE2EOrder{total: request.Amount.Total, state: "NOTPAY"}
		return http.StatusOK, wpTestJSONBody(map[string]any{"code_url": "weixin://wxpay/bizpayurl?pr=" + request.OutTradeNo})
	case r.Method == http.MethodPost && r.URL.Path == "/v3/pay/transactions/h5":
		var request struct {
			OutTradeNo string `json:"out_trade_no"`
			Amount     struct {
				Total int64 `json:"total"`
			} `json:"amount"`
			SceneInfo struct {
				PayerClientIP string `json:"payer_client_ip"`
				H5Info        struct {
					Type string `json:"type"`
				} `json:"h5_info"`
			} `json:"scene_info"`
		}
		if err := jsonx.Unmarshal(body, &request); err != nil || request.SceneInfo.PayerClientIP == "" || request.SceneInfo.H5Info.Type != "Wap" {
			t.Errorf("h5 body %s (%v)", body, err)
			return http.StatusBadRequest, nil
		}
		s.orders[request.OutTradeNo] = &wpE2EOrder{total: request.Amount.Total, state: "NOTPAY"}
		return http.StatusOK, wpTestJSONBody(map[string]any{
			"h5_url": "https://wx.tenpay.com/cgi-bin/mmpayweb-bin/checkmweb?prepay_id=" + request.OutTradeNo + "&package=123",
		})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v3/pay/transactions/out-trade-no/"):
		id := strings.TrimPrefix(r.URL.Path, "/v3/pay/transactions/out-trade-no/")
		o := s.orders[id]
		if o == nil {
			return http.StatusNotFound, []byte(`{"code":"ORDER_NOT_EXIST","message":"no such order"}`)
		}
		return http.StatusOK, wpTestJSONBody(map[string]any{
			"appid": wpTestAppID, "mchid": wpTestMchID, "out_trade_no": id,
			"transaction_id": o.trade, "trade_state": o.state,
			"amount": map[string]any{"total": o.total, "currency": "CNY"},
		})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/close"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v3/pay/transactions/out-trade-no/"), "/close")
		o := s.orders[id]
		if o != nil {
			o.closed, o.state = true, "CLOSED"
		}
		return http.StatusNoContent, nil
	case r.Method == http.MethodPost && r.URL.Path == "/v3/refund/domestic/refunds":
		var request struct {
			OutTradeNo  string `json:"out_trade_no"`
			OutRefundNo string `json:"out_refund_no"`
			Amount      struct {
				Total  int64 `json:"total"`
				Refund int64 `json:"refund"`
			} `json:"amount"`
		}
		if err := jsonx.Unmarshal(body, &request); err != nil {
			t.Errorf("refund body: %v", err)
			return http.StatusBadRequest, nil
		}
		refund := &wpE2ERefund{order: request.OutTradeNo, total: request.Amount.Total, refund: request.Amount.Refund}
		s.refunds[request.OutRefundNo] = refund
		if s.refundMode == "unknown" {
			return http.StatusInternalServerError, []byte(`{"code":"SYSTEM_ERROR","message":"unknown"}`)
		}
		return http.StatusOK, wpE2ERefundJSON(request.OutRefundNo, refund, "SUCCESS")
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v3/refund/domestic/refunds/"):
		id := strings.TrimPrefix(r.URL.Path, "/v3/refund/domestic/refunds/")
		refund := s.refunds[id]
		if refund == nil {
			return http.StatusNotFound, []byte(`{"code":"RESOURCE_NOT_EXISTS","message":"no such refund"}`)
		}
		status := "PROCESSING"
		if s.refundMode == "success" {
			status = "SUCCESS"
		}
		return http.StatusOK, wpE2ERefundJSON(id, refund, status)
	default:
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		return http.StatusNotFound, nil
	}
}

func (w *world) wpCallback(merchant MerchantID, callback Callback) *httptest.ResponseRecorder {
	w.t.Helper()
	mux := http.NewServeMux()
	mux.Handle("POST "+NotifyPath+"{merchant}", w.p.Notify())
	request := httptest.NewRequestWithContext(w.t.Context(), http.MethodPost, NotifyPath+merchant.TypeID(), bytes.NewReader(callback.Body))
	request.Header = callback.Header.Clone()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

//nolint:cyclop,gocognit // one branch per protocol edge; the test reads as a table of cases
func TestWechatPayLocalGatewayEndToEnd(t *testing.T) {
	w := newTestWorld(t, WechatPay{})
	state := &wpE2EState{orders: map[string]*wpE2EOrder{}, refunds: map[string]*wpE2ERefund{}}
	gateway := &wpTestGateway{t: t, respond: func(r *http.Request, body []byte) (int, []byte) {
		return state.respond(t, r, body)
	}}
	server := wpTestServer(t, gateway)
	cfg := wpCertConfig(t, server.URL, server.Certificate(), false)
	w.p.client = cfg.Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA,
		MethodInput{Method: "wxpay", Upstream: "native", Fee: Fee{PPM: 30_000}, Enabled: true},
		MethodInput{Method: "wxpayh5", Upstream: "h5", Fee: Fee{PPM: 30_000}, Enabled: true},
	)
	in.Kind, in.Settings, in.Secrets = "wechatpay", cfg.Settings, cfg.Secrets
	in.RefundEnabled, in.UserRefund = true, true
	merchant := w.mustSave(siteScope(w.siteA), in)

	place := func(method Method, credits int64, key string) (Order, Checkout) {
		w.t.Helper()
		o, checkout, err := w.placeAs(w.accountA(), CreateOrder{
			Site: w.site(w.siteA, "CNY"), Account: w.account, User: w.user, Method: method, Credits: credits, Key: key,
			ReturnURL: "https://console.example.com/topup", ClientIP: netip.MustParseAddr("203.0.113.7"),
		})
		if err != nil {
			w.t.Fatal(err)
		}
		return o, checkout
	}

	// Native order, encrypted callback, query-confirmed credit.
	paid, checkout := place("wxpay", 10*yuan, "wechat-native")
	if !strings.HasPrefix(checkout.QRCode, "weixin://wxpay/bizpayurl?pr=") {
		t.Fatalf("native checkout %+v", checkout)
	}
	state.settle(paid.ID.TypeID(), paid.Total.Minor)
	callback := wpTestCallback(t, wpTransaction(paid.ID.TypeID(), "T-"+paid.ID.TypeID(), paid.Total.Minor, "SUCCESS"), wpKeys(t).platform, wpTestPlatformSerial(t))
	response := w.wpCallback(MerchantID(merchant.ID), callback)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("callback %d %s", response.Code, response.Body)
	}
	w.assertCredit(paid, paid.Credits)
	if got, trade, _ := w.stored(paid.ID); got != "paid" || trade != "T-"+paid.ID.TypeID() {
		t.Fatalf("%s %s", got, trade)
	}
	if queries := gateway.queryCount(); queries != 1 {
		t.Fatalf("confirmation queried %d times", queries)
	}

	// A replay of the committed receipt neither queries nor credits again.
	response = w.wpCallback(MerchantID(merchant.ID), callback)
	if response.Code != http.StatusOK {
		t.Fatalf("replay %d %s", response.Code, response.Body)
	}
	w.assertCredit(paid, paid.Credits)
	if queries := gateway.queryCount(); queries != 1 {
		t.Fatalf("replay queried %d times", queries)
	}

	// H5 is a separate method on the same merchant; its checkout carries the return address.
	_, h5 := place("wxpayh5", yuan, "wechat-h5")
	if !strings.Contains(h5.PayURL, "https://wx.tenpay.com/cgi-bin/mmpayweb-bin/checkmweb?prepay_id=") ||
		!strings.HasSuffix(h5.PayURL, "&redirect_url=https%3A%2F%2Fconsole.example.com%2Ftopup") {
		t.Fatalf("h5 checkout %s", h5.PayURL)
	}

	// A verified amount that does not match the order goes to review without a credit.
	bad, _ := place("wxpay", yuan, "wechat-invalid")
	state.settle(bad.ID.TypeID(), bad.Total.Minor+1)
	badCallback := wpTestCallback(t, wpTransaction(bad.ID.TypeID(), "T-"+bad.ID.TypeID(), bad.Total.Minor+1, "SUCCESS"), wpKeys(t).platform, wpTestPlatformSerial(t))
	if response := w.wpCallback(MerchantID(merchant.ID), badCallback); response.Code != http.StatusOK {
		t.Fatalf("invalid callback %d %s", response.Code, response.Body)
	}
	w.assertCredit(bad, 0)
	if got, _, _ := w.stored(bad.ID); got != "review" {
		t.Fatalf("invalid amount state %s", got)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 1 {
		t.Fatal("invalid payment has no discrepancy")
	}

	// A lost callback is recovered by the scheduled query.
	lost, _ := place("wxpay", yuan, "wechat-lost")
	state.settle(lost.ID.TypeID(), lost.Total.Minor)
	w.exec(fmt.Sprintf("UPDATE payments.orders SET next_query_at = clock_timestamp() - interval '1 second' WHERE id = '%s'", uuid.UUID(lost.ID)))
	w.p.pollOrder(t.Context(), lost.ID)
	w.assertCredit(lost, lost.Credits)
	if got, _, _ := w.stored(lost.ID); got != "paid" {
		t.Fatalf("lost callback state %s", got)
	}

	// Close a waiting order, then refund the paid one through the core.
	cancelled, _ := place("wxpay", yuan, "wechat-cancel")
	if _, err := w.p.Cancel(t.Context(), w.accountA(), cancelled.ID); err != nil {
		t.Fatal(err)
	}
	if !state.isClosed(cancelled.ID.TypeID()) {
		t.Fatal("close did not reach the gateway")
	}

	refund := w.mustRefund(paid, paid.Credits, "wechat-refund")
	if refund.State != "frozen" {
		t.Fatalf("refund state %s", refund.State)
	}
	w.approve(refund)
	w.dueRefunds()
	storedRefund, err := w.p.GetRefund(t.Context(), w.accountA(), refund.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRefund.State != "succeeded" || storedRefund.ProviderRef != "WX-"+refund.ID.TypeID() {
		t.Fatalf("refund %+v", storedRefund)
	}

	// An unknown refund stays frozen and converges on the same merchant refund number.
	unknown, _ := place("wxpay", yuan, "wechat-unknown")
	state.settle(unknown.ID.TypeID(), unknown.Total.Minor)
	unknownCallback := wpTestCallback(t, wpTransaction(unknown.ID.TypeID(), "T-"+unknown.ID.TypeID(), unknown.Total.Minor, "SUCCESS"), wpKeys(t).platform, wpTestPlatformSerial(t))
	if response := w.wpCallback(MerchantID(merchant.ID), unknownCallback); response.Code != http.StatusOK {
		t.Fatalf("unknown order callback %d %s", response.Code, response.Body)
	}
	state.mu.Lock()
	state.refundMode = "unknown"
	state.mu.Unlock()
	pending := w.mustRefund(unknown, unknown.Credits, "wechat-unknown-refund")
	w.approve(pending)
	w.dueRefunds()
	storedRefund, err = w.p.GetRefund(t.Context(), w.accountA(), pending.ID)
	if err != nil || storedRefund.State != "pending" {
		t.Fatalf("%+v %v", storedRefund, err)
	}
	state.mu.Lock()
	state.refundMode = "success"
	state.mu.Unlock()
	w.dueRefunds()
	storedRefund, err = w.p.GetRefund(t.Context(), w.accountA(), pending.ID)
	if err != nil || storedRefund.State != "succeeded" || storedRefund.ProviderRef != "WX-"+pending.ID.TypeID() {
		t.Fatalf("%+v %v", storedRefund, err)
	}

	// No secret in logs or order events.
	var raw string
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT string_agg(raw::text, '') FROM payments.order_events").Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	wpNoSecrets(t, w.logs.String(), "PRIVATE KEY", "BEGIN CERTIFICATE", wpTestAPIKey)
	wpNoSecrets(t, raw, "PRIVATE KEY", "BEGIN CERTIFICATE", wpTestAPIKey)
}

func wpNoSecrets(t *testing.T, text string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(text, value) {
			t.Fatalf("leaked %q in %q", value, text)
		}
	}
}
