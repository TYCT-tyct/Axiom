package payments

import (
	"bytes"
	"errors"
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
	queryFail  bool
	queryMchid string
	createFail bool
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

// respond dispatches one gateway request. The mutex is held for the whole request so each handler sees a settled state.
func (s *wpE2EState) respond(t *testing.T, r *http.Request, body []byte) (int, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodPost:
		switch {
		case r.URL.Path == "/v3/pay/transactions/native":
			return s.createNative(t, body)
		case r.URL.Path == "/v3/pay/transactions/h5":
			return s.createH5(t, body)
		case strings.HasSuffix(r.URL.Path, "/close"):
			return s.closeOrder(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v3/pay/transactions/out-trade-no/"), "/close"))
		case r.URL.Path == "/v3/refund/domestic/refunds":
			return s.createRefund(t, body)
		}
	case http.MethodGet:
		switch {
		case strings.HasPrefix(r.URL.Path, "/v3/pay/transactions/out-trade-no/"):
			return s.queryOrder(strings.TrimPrefix(r.URL.Path, "/v3/pay/transactions/out-trade-no/"))
		case strings.HasPrefix(r.URL.Path, "/v3/refund/domestic/refunds/"):
			return s.queryRefund(strings.TrimPrefix(r.URL.Path, "/v3/refund/domestic/refunds/"))
		}
	}
	t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	return http.StatusNotFound, nil
}

func (s *wpE2EState) createNative(t *testing.T, body []byte) (int, []byte) {
	t.Helper()
	if s.createFail {
		return http.StatusBadRequest, []byte(`{"code":"PARAM_ERROR","message":"refused"}`)
	}
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
}

func (s *wpE2EState) createH5(t *testing.T, body []byte) (int, []byte) {
	t.Helper()
	if s.createFail {
		return http.StatusBadRequest, []byte(`{"code":"PARAM_ERROR","message":"refused"}`)
	}
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
}

func (s *wpE2EState) queryOrder(id string) (int, []byte) {
	if s.queryFail {
		return http.StatusInternalServerError, []byte(`{"code":"SYSTEM_ERROR","message":"query failed"}`)
	}
	o := s.orders[id]
	if o == nil {
		return http.StatusNotFound, []byte(`{"code":"ORDER_NOT_EXIST","message":"no such order"}`)
	}
	mchid := wpTestMchID
	if s.queryMchid != "" {
		mchid = s.queryMchid
	}
	return http.StatusOK, wpTestJSONBody(map[string]any{
		"appid": wpTestAppID, "mchid": mchid, "out_trade_no": id,
		"transaction_id": o.trade, "trade_state": o.state,
		"amount": map[string]any{"total": o.total, "currency": "CNY"},
	})
}

func (s *wpE2EState) closeOrder(id string) (int, []byte) {
	if o := s.orders[id]; o != nil {
		o.closed, o.state = true, "CLOSED"
	}
	return http.StatusNoContent, nil
}

func (s *wpE2EState) createRefund(t *testing.T, body []byte) (int, []byte) {
	t.Helper()
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
}

func (s *wpE2EState) queryRefund(id string) (int, []byte) {
	refund := s.refunds[id]
	if refund == nil {
		return http.StatusNotFound, []byte(`{"code":"RESOURCE_NOT_EXISTS","message":"no such refund"}`)
	}
	status := "PROCESSING"
	if s.refundMode == "success" {
		status = "SUCCESS"
	}
	return http.StatusOK, wpE2ERefundJSON(id, refund, status)
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

// wpE2E is one local WeChat Pay world across the walk's phases: the paid order is what the refund phases refund.
type wpE2E struct {
	t        *testing.T
	w        *world
	state    *wpE2EState
	gateway  *wpTestGateway
	merchant Merchant
	paid     Order
}

func newWPE2E(t *testing.T) *wpE2E {
	t.Helper()
	f := &wpE2E{
		t: t, w: newTestWorld(t, WechatPay{}),
		state: &wpE2EState{orders: map[string]*wpE2EOrder{}, refunds: map[string]*wpE2ERefund{}},
	}
	f.gateway = &wpTestGateway{t: t, respond: func(r *http.Request, body []byte) (int, []byte) {
		return f.state.respond(t, r, body)
	}}
	server := wpTestServer(t, f.gateway)
	cfg := wpCertConfig(t, server.URL, server.Certificate(), false)
	f.w.p.client = cfg.Client
	f.w.open(f.w.siteA, RoundRobin)
	in := f.w.input(f.w.siteA,
		MethodInput{Method: "wxpay", Upstream: "native", Fee: Fee{PPM: 30_000}, Enabled: true},
		MethodInput{Method: "wxpayh5", Upstream: "h5", Fee: Fee{PPM: 30_000}, Enabled: true},
	)
	in.Kind, in.Settings, in.Secrets = "wechatpay", cfg.Settings, cfg.Secrets
	in.RefundEnabled, in.UserRefund = true, true
	f.merchant = f.w.mustSave(siteScope(f.w.siteA), in)
	return f
}

func (f *wpE2E) place(method Method, credits int64, key string) (Order, Checkout) {
	f.t.Helper()
	o, checkout, err := f.w.placeAs(f.w.accountA(), CreateOrder{
		Site: f.w.site(f.w.siteA, "CNY"), Account: f.w.account, User: f.w.user, Method: method, Credits: credits, Key: key,
		ReturnURL: "https://console.example.com/topup", ClientIP: netip.MustParseAddr("203.0.113.7"),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return o, checkout
}

// checkCreateRefusal asserts a signed 4xx is definite: the order fails and gives its daily reservation back.
func (f *wpE2E) checkCreateRefusal() {
	f.t.Helper()
	f.state.mu.Lock()
	f.state.createFail = true
	f.state.mu.Unlock()
	refused, _, err := f.w.placeAs(f.w.accountA(), CreateOrder{
		Site: f.w.site(f.w.siteA, "CNY"), Account: f.w.account, User: f.w.user, Method: "wxpay", Credits: yuan,
		Key: "wechat-refused", ReturnURL: "https://console.example.com/topup", ClientIP: netip.MustParseAddr("203.0.113.7"),
	})
	f.state.mu.Lock()
	f.state.createFail = false
	f.state.mu.Unlock()
	if !errors.Is(err, ErrRefused) {
		f.t.Fatalf("create refusal error %v", err)
	}
	if got, _, _ := f.w.stored(refused.ID); got != "failed" {
		f.t.Fatalf("create refusal state %s", got)
	}
	if used := f.w.count(f.w.p.platform, "SELECT coalesce(sum(used), 0) FROM payments.merchant_usage WHERE merchant_id=$1", f.merchant.ID); used != 0 {
		f.t.Fatalf("create refusal left %d in daily usage", used)
	}
}

// checkNativeCallback asserts a native checkout's encrypted callback credits once, and its replay neither queries nor
// credits again.
func (f *wpE2E) checkNativeCallback() {
	f.t.Helper()
	paid, checkout := f.place("wxpay", 10*yuan, "wechat-native")
	f.paid = paid
	if !strings.HasPrefix(checkout.QRCode, "weixin://wxpay/bizpayurl?pr=") {
		f.t.Fatalf("native checkout %+v", checkout)
	}
	f.state.settle(paid.ID.TypeID(), paid.Total.Minor)
	callback := wpTestCallback(f.t, wpTransaction(paid.ID.TypeID(), "T-"+paid.ID.TypeID(), paid.Total.Minor, "SUCCESS"), wpKeys(f.t).platform, wpTestPlatformSerial(f.t))
	response := f.w.wpCallback(MerchantID(f.merchant.ID), callback)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		f.t.Fatalf("callback %d %s", response.Code, response.Body)
	}
	f.w.assertCredit(paid, paid.Credits)
	if got, trade, _ := f.w.stored(paid.ID); got != "paid" || trade != "T-"+paid.ID.TypeID() {
		f.t.Fatalf("%s %s", got, trade)
	}
	if queries := f.gateway.queryCount(); queries != 1 {
		f.t.Fatalf("confirmation queried %d times", queries)
	}

	// A replay of the committed receipt neither queries nor credits again.
	response = f.w.wpCallback(MerchantID(f.merchant.ID), callback)
	if response.Code != http.StatusOK {
		f.t.Fatalf("replay %d %s", response.Code, response.Body)
	}
	f.w.assertCredit(paid, paid.Credits)
	if queries := f.gateway.queryCount(); queries != 1 {
		f.t.Fatalf("replay queried %d times", queries)
	}
}

// checkH5Checkout asserts H5 is a separate method on the same merchant whose checkout carries the return address.
func (f *wpE2E) checkH5Checkout() {
	f.t.Helper()
	_, h5 := f.place("wxpayh5", yuan, "wechat-h5")
	if !strings.Contains(h5.PayURL, "https://wx.tenpay.com/cgi-bin/mmpayweb-bin/checkmweb?prepay_id=") ||
		!strings.HasSuffix(h5.PayURL, "&redirect_url=https%3A%2F%2Fconsole.example.com%2Ftopup") {
		f.t.Fatalf("h5 checkout %s", h5.PayURL)
	}
}

// checkInvalidAmount asserts a verified amount that does not match the order goes to review without a credit.
func (f *wpE2E) checkInvalidAmount() {
	f.t.Helper()
	bad, _ := f.place("wxpay", yuan, "wechat-invalid")
	f.state.settle(bad.ID.TypeID(), bad.Total.Minor+1)
	badCallback := wpTestCallback(f.t, wpTransaction(bad.ID.TypeID(), "T-"+bad.ID.TypeID(), bad.Total.Minor+1, "SUCCESS"), wpKeys(f.t).platform, wpTestPlatformSerial(f.t))
	if response := f.w.wpCallback(MerchantID(f.merchant.ID), badCallback); response.Code != http.StatusOK {
		f.t.Fatalf("invalid callback %d %s", response.Code, response.Body)
	}
	f.w.assertCredit(bad, 0)
	if got, _, _ := f.w.stored(bad.ID); got != "review" {
		f.t.Fatalf("invalid amount state %s", got)
	}
	if f.w.count(f.w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 1 {
		f.t.Fatal("invalid payment has no discrepancy")
	}
}

// checkLostCallback asserts the scheduled query recovers a payment whose callback never arrived.
func (f *wpE2E) checkLostCallback() Order {
	f.t.Helper()
	lost, _ := f.place("wxpay", yuan, "wechat-lost")
	f.state.settle(lost.ID.TypeID(), lost.Total.Minor)
	f.w.exec(fmt.Sprintf("UPDATE payments.orders SET next_query_at = clock_timestamp() - interval '1 second' WHERE id = '%s'", uuid.UUID(lost.ID)))
	f.w.p.pollOrder(f.t.Context(), lost.ID)
	f.w.assertCredit(lost, lost.Credits)
	if got, _, _ := f.w.stored(lost.ID); got != "paid" {
		f.t.Fatalf("lost callback state %s", got)
	}
	return lost
}

// checkWrongMerchant asserts a signed query for another merchant does not credit the local order.
func (f *wpE2E) checkWrongMerchant() {
	f.t.Helper()
	wrongMerchant, _ := f.place("wxpay", yuan, "wechat-wrong-merchant")
	f.state.settle(wrongMerchant.ID.TypeID(), wrongMerchant.Total.Minor)
	f.state.mu.Lock()
	f.state.queryMchid = "1900000002"
	f.state.mu.Unlock()
	f.w.p.pollOrder(f.t.Context(), wrongMerchant.ID)
	f.w.assertCredit(wrongMerchant, 0)
	if got, _, _ := f.w.stored(wrongMerchant.ID); got != "pending" {
		f.t.Fatalf("wrong-merchant query state %s", got)
	}
	f.state.mu.Lock()
	f.state.queryMchid = ""
	f.state.mu.Unlock()
}

// checkPreSendFailure asserts a failed pre-refund order query sent no refund and released the freeze.
func (f *wpE2E) checkPreSendFailure(lost Order) {
	f.t.Helper()
	f.state.mu.Lock()
	f.state.queryFail = true
	f.state.mu.Unlock()
	noSend := f.w.mustRefund(lost, lost.Credits, "wechat-refund-not-sent")
	if got := f.w.approve(noSend); got.State != "failed" {
		f.t.Fatalf("pre-send failure state %s", got.State)
	}
	if frozen := f.w.count(f.w.p.platform, "SELECT frozen FROM billing.balances WHERE account_id=$1", f.w.account); frozen != 0 {
		f.t.Fatalf("pre-send failure left %d frozen", frozen)
	}
	f.state.mu.Lock()
	f.state.queryFail = false
	f.state.mu.Unlock()
}

// checkCancelAndRefund closes a waiting order, then refunds the paid one through the core.
func (f *wpE2E) checkCancelAndRefund() {
	f.t.Helper()
	cancelled, _ := f.place("wxpay", yuan, "wechat-cancel")
	if _, err := f.w.p.Cancel(f.t.Context(), f.w.accountA(), cancelled.ID); err != nil {
		f.t.Fatal(err)
	}
	if !f.state.isClosed(cancelled.ID.TypeID()) {
		f.t.Fatal("close did not reach the gateway")
	}
	refund := f.w.mustRefund(f.paid, f.paid.Credits, "wechat-refund")
	if refund.State != "frozen" {
		f.t.Fatalf("refund state %s", refund.State)
	}
	f.w.approve(refund)
	f.w.dueRefunds()
	stored, err := f.w.p.GetRefund(f.t.Context(), f.w.accountA(), refund.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if stored.State != "succeeded" || stored.ProviderRef != "WX-"+refund.ID.TypeID() {
		f.t.Fatalf("refund %+v", stored)
	}
}

// checkUnknownRefund asserts an unknown refund stays frozen and converges on the same merchant refund number.
func (f *wpE2E) checkUnknownRefund() {
	f.t.Helper()
	unknown, _ := f.place("wxpay", yuan, "wechat-unknown")
	f.state.settle(unknown.ID.TypeID(), unknown.Total.Minor)
	unknownCallback := wpTestCallback(f.t, wpTransaction(unknown.ID.TypeID(), "T-"+unknown.ID.TypeID(), unknown.Total.Minor, "SUCCESS"), wpKeys(f.t).platform, wpTestPlatformSerial(f.t))
	if response := f.w.wpCallback(MerchantID(f.merchant.ID), unknownCallback); response.Code != http.StatusOK {
		f.t.Fatalf("unknown order callback %d %s", response.Code, response.Body)
	}
	f.state.mu.Lock()
	f.state.refundMode = "unknown"
	f.state.mu.Unlock()
	pending := f.w.mustRefund(unknown, unknown.Credits, "wechat-unknown-refund")
	f.w.approve(pending)
	f.w.dueRefunds()
	stored, err := f.w.p.GetRefund(f.t.Context(), f.w.accountA(), pending.ID)
	if err != nil || stored.State != "pending" {
		f.t.Fatalf("%+v %v", stored, err)
	}
	f.state.mu.Lock()
	f.state.refundMode = "success"
	f.state.mu.Unlock()
	f.w.dueRefunds()
	stored, err = f.w.p.GetRefund(f.t.Context(), f.w.accountA(), pending.ID)
	if err != nil || stored.State != "succeeded" || stored.ProviderRef != "WX-"+pending.ID.TypeID() {
		f.t.Fatalf("%+v %v", stored, err)
	}
}

// checkNoSecrets asserts neither the log nor the order events carry a key or a certificate.
func (f *wpE2E) checkNoSecrets() {
	f.t.Helper()
	var raw string
	if err := f.w.tx(f.w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(f.t.Context(), "SELECT string_agg(raw::text, '') FROM payments.order_events").Scan(&raw)
	}); err != nil {
		f.t.Fatal(err)
	}
	wpNoSecrets(f.t, f.w.logs.String(), "PRIVATE KEY", "BEGIN CERTIFICATE", wpTestAPIKey)
	wpNoSecrets(f.t, raw, "PRIVATE KEY", "BEGIN CERTIFICATE", wpTestAPIKey)
}

// TestWechatPayLocalGatewayEndToEnd walks one WeChat Pay gateway: create refusal, native callback and replay, H5,
// an invalid amount, a lost callback, a wrong-merchant query, a pre-send failure, cancellation, a refund and an
// unknown refund that converges.
func TestWechatPayLocalGatewayEndToEnd(t *testing.T) {
	f := newWPE2E(t)
	f.checkCreateRefusal()
	f.checkNativeCallback()
	f.checkH5Checkout()
	f.checkInvalidAmount()
	lost := f.checkLostCallback()
	f.checkWrongMerchant()
	f.checkPreSendFailure(lost)
	f.checkCancelAndRefund()
	f.checkUnknownRefund()
	f.checkNoSecrets()
}

func wpNoSecrets(t *testing.T, text string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(text, value) {
			t.Fatalf("leaked %q in %q", value, text)
		}
	}
}
