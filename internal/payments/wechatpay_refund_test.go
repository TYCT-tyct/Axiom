package payments

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
	"uuid"

	"axiom/internal/platform/jsonx"
)

// wpRefundFixture is the refund walk's gateway and channel: the test changes one answer at a time.
type wpRefundFixture struct {
	t            *testing.T
	mu           sync.Mutex
	refundStatus string
	refundEcho   struct {
		outRefundNo, outTradeNo string
		total, refund           int64
	}
	lastRefund []byte
	queryBody  []byte
	gateway    *wpTestGateway
	channel    Channel
	call       RefundCall
}

func newWPRefundFixture(t *testing.T) *wpRefundFixture {
	t.Helper()
	f := &wpRefundFixture{t: t}
	f.gateway = &wpTestGateway{t: t, respond: f.respond}
	server := wpTestServer(t, f.gateway)
	f.channel = wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), false))
	order := OrderID(uuid.NewV7())
	refund := RefundID(uuid.NewV7())
	f.queryBody = wpQueryJSON(order.TypeID(), "SUCCESS", map[string]any{"total": 10000, "currency": "CNY"})
	f.refundEcho = struct {
		outRefundNo, outTradeNo string
		total, refund           int64
	}{refund.TypeID(), order.TypeID(), 10000, 3000}
	f.call = RefundCall{Refund: refund, Order: order, Amount: Money{Currency: currency(t, "CNY"), Minor: 3000}, Reason: strings.Repeat("退", 100)}
	return f
}

func (f *wpRefundFixture) respond(r *http.Request, body []byte) (int, []byte) {
	switch {
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v3/pay/transactions/out-trade-no/"):
		return http.StatusOK, f.queryBody
	case r.Method == http.MethodPost && r.URL.Path == "/v3/refund/domestic/refunds":
		f.mu.Lock()
		f.lastRefund = bytes.Clone(body)
		status, echo := f.refundStatus, f.refundEcho
		f.mu.Unlock()
		if status == "" {
			status = "SUCCESS"
		}
		return http.StatusOK, wpTestJSONBody(map[string]any{
			"refund_id": "WX-" + echo.outRefundNo, "out_refund_no": echo.outRefundNo, "out_trade_no": echo.outTradeNo,
			"status": status, "amount": map[string]any{"total": echo.total, "refund": echo.refund, "currency": "CNY"},
		})
	default:
		f.t.Errorf("unexpected refund request %s %s", r.Method, r.URL.Path)
		return http.StatusNotFound, nil
	}
}

// checkRequest asserts the refund request carries the order, the refund, the amounts and a bounded reason.
func (f *wpRefundFixture) checkRequest() {
	f.t.Helper()
	var body struct {
		OutTradeNo  string `json:"out_trade_no"`
		OutRefundNo string `json:"out_refund_no"`
		Reason      string `json:"reason"`
		Amount      struct {
			Refund   int64  `json:"refund"`
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if err := jsonx.Unmarshal(f.lastRefund, &body); err != nil {
		f.t.Fatal(err)
	}
	if body.OutTradeNo != f.call.Order.TypeID() || body.OutRefundNo != f.call.Refund.TypeID() || body.Amount.Refund != 3000 ||
		body.Amount.Total != 10000 || body.Amount.Currency != "CNY" {
		f.t.Fatalf("refund body %s", f.lastRefund)
	}
	if len(body.Reason) > 80 || !utf8.ValidString(body.Reason) {
		f.t.Fatalf("reason %q is %d bytes", body.Reason, len(body.Reason))
	}
}

// checkStatuses maps each refund status the gateway may report.
func (f *wpRefundFixture) checkStatuses() {
	f.t.Helper()
	for _, tc := range []struct {
		status string
		want   RefundOutcome
	}{{"PROCESSING", RefundPending}, {"ABNORMAL", RefundPending}, {"CLOSED", RefundFailed}} {
		f.t.Run(tc.status, func(t *testing.T) {
			f.mu.Lock()
			f.refundStatus = tc.status
			f.mu.Unlock()
			state, err := f.channel.Refund(f.t.Context(), f.call)
			if err != nil || state.State != tc.want {
				t.Fatalf("%+v %v", state, err)
			}
		})
	}
	f.mu.Lock()
	f.refundStatus = ""
	f.mu.Unlock()
}

// wpRefundResponseCase is one uncertain refund answer and whether it is definite or unknown.
type wpRefundResponseCase struct {
	name     string
	status   int
	body     string
	rejected bool
	unknown  bool
}

// checkResponse asserts the answer is the kind of error the case expects.
func (f *wpRefundFixture) checkResponse(t *testing.T, tc wpRefundResponseCase) {
	t.Helper()
	f.gateway.respond = func(r *http.Request, _ []byte) (int, []byte) {
		if r.Method == http.MethodGet {
			return http.StatusOK, f.queryBody
		}
		return tc.status, []byte(tc.body)
	}
	_, err := f.channel.Refund(t.Context(), f.call)
	if tc.rejected && !wpTestRejected(err) {
		t.Fatalf("%s became %v", tc.name, err)
	}
	if tc.unknown && (err == nil || wpTestRejected(err)) {
		t.Fatalf("%s became %v", tc.name, err)
	}
}

// checkUncertainResponses asserts a 429 and a 500 stay unknown, a 400 is definite, and a failed pre-send query sends
// no refund.
func (f *wpRefundFixture) checkUncertainResponses() {
	f.t.Helper()
	for _, tc := range []wpRefundResponseCase{
		{"throttled", http.StatusTooManyRequests, `{"code":"FREQUENCY_LIMITED"}`, false, true},
		{"bad request", http.StatusBadRequest, `{"code":"PARAM_ERROR"}`, true, false},
		{"server error", http.StatusInternalServerError, `{"code":"SYSTEM_ERROR"}`, false, true},
	} {
		f.t.Run(tc.name, func(t *testing.T) {
			f.checkResponse(t, tc)
		})
	}
	f.gateway.respond = func(r *http.Request, _ []byte) (int, []byte) {
		if r.Method == http.MethodGet {
			return http.StatusInternalServerError, []byte(`{"code":"SYSTEM_ERROR"}`)
		}
		return http.StatusOK, nil
	}
	if _, err := f.channel.Refund(f.t.Context(), f.call); !wpTestRejected(err) {
		f.t.Fatalf("pre-send query failure became %v", err)
	}
}

// checkEchoes asserts the original total and the echoed refund come from the order and the response, not the caller.
func (f *wpRefundFixture) checkEchoes() {
	f.t.Helper()
	for _, tc := range []struct {
		name        string
		total       int64
		refund      int64
		description string
	}{
		{"wrong echoed total", 9999, 3000, "wrong echoed total"},
		{"wrong echoed refund", 10000, 3001, "wrong echoed refund"},
	} {
		f.t.Run(tc.name, func(t *testing.T) {
			f.gateway.respond = func(r *http.Request, _ []byte) (int, []byte) {
				if r.Method == http.MethodGet {
					return http.StatusOK, f.queryBody
				}
				return http.StatusOK, wpTestJSONBody(map[string]any{
					"refund_id": "WX-1", "out_refund_no": f.call.Refund.TypeID(), "out_trade_no": f.call.Order.TypeID(),
					"status": "SUCCESS", "amount": map[string]any{"total": tc.total, "refund": tc.refund, "currency": "CNY"},
				})
			}
			if _, err := f.channel.Refund(f.t.Context(), f.call); err == nil {
				t.Fatal(tc.description + " accepted")
			}
		})
	}
}

// checkOrderStates asserts a refused order or amount never reaches the refund request.
func (f *wpRefundFixture) checkOrderStates() {
	f.t.Helper()
	for _, tc := range []struct {
		name  string
		query []byte
		money int64
		order int
	}{
		{"closed order", wpQueryJSON(f.call.Order.TypeID(), "CLOSED", nil), 3000, http.StatusOK},
		{"unpaid order", wpQueryJSON(f.call.Order.TypeID(), "NOTPAY", nil), 3000, http.StatusOK},
		{"refund without an original total", wpQueryJSON(f.call.Order.TypeID(), "SUCCESS", nil), 3000, http.StatusOK},
		{"over-refund", wpQueryJSON(f.call.Order.TypeID(), "SUCCESS", map[string]any{"total": 10000, "currency": "CNY"}), 10001, http.StatusOK},
		{"missing order", []byte(`{"code":"ORDER_NOT_EXIST"}`), 3000, http.StatusNotFound},
	} {
		f.t.Run(tc.name, func(t *testing.T) {
			call := f.call
			call.Amount.Minor = tc.money
			f.gateway.respond = func(r *http.Request, _ []byte) (int, []byte) {
				if r.Method == http.MethodGet {
					return tc.order, tc.query
				}
				return http.StatusOK, nil
			}
			if _, err := f.channel.Refund(f.t.Context(), call); !wpTestRejected(err) {
				t.Fatalf("%s became %v", tc.name, err)
			}
		})
	}
}

func TestWechatPayRefund(t *testing.T) {
	f := newWPRefundFixture(t)
	state, err := f.channel.Refund(t.Context(), f.call)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != RefundSucceeded || state.ProviderRef != "WX-"+f.call.Refund.TypeID() {
		t.Fatalf("refund state %+v", state)
	}
	f.checkRequest()
	f.checkStatuses()
	f.checkUncertainResponses()
	f.checkEchoes()
	f.checkOrderStates()
}

//nolint:cyclop // one branch per query-refund status; the test reads as a table of cases
func TestWechatPayQueryRefund(t *testing.T) {
	var status int
	var body []byte
	var path string
	gateway := &wpTestGateway{t: t, respond: func(r *http.Request, _ []byte) (int, []byte) {
		path = r.URL.Path
		return status, body
	}}
	server := wpTestServer(t, gateway)
	channel := wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), false))
	order, refund := OrderID(uuid.NewV7()), RefundID(uuid.NewV7())
	call := RefundCall{Refund: refund, Order: order, ProviderRef: "WX-OLD", Amount: Money{Currency: currency(t, "CNY"), Minor: 3000}}
	makeBody := func(state string) []byte {
		return wpTestJSONBody(map[string]any{
			"refund_id": "WX-NEW", "out_refund_no": refund.TypeID(), "out_trade_no": order.TypeID(),
			"status": state, "amount": map[string]any{"total": 10000, "refund": 3000, "currency": "CNY"},
		})
	}
	status, body = http.StatusOK, makeBody("SUCCESS")
	state, err := channel.QueryRefund(t.Context(), call)
	if err != nil || state.State != RefundSucceeded || state.ProviderRef != "WX-NEW" {
		t.Fatalf("%+v %v", state, err)
	}
	if path != "/v3/refund/domestic/refunds/"+refund.TypeID() {
		t.Fatalf("queried %s, not the merchant refund number", path)
	}
	for _, tc := range []struct {
		status string
		want   RefundOutcome
	}{{"PROCESSING", RefundPending}, {"ABNORMAL", RefundPending}, {"CLOSED", RefundFailed}} {
		status, body = http.StatusOK, makeBody(tc.status)
		state, err := channel.QueryRefund(t.Context(), call)
		if err != nil || state.State != tc.want {
			t.Fatalf("%s: %+v %v", tc.status, state, err)
		}
	}
	status, body = http.StatusNotFound, []byte(`{"code":"RESOURCE_NOT_EXISTS"}`)
	state, err = channel.QueryRefund(t.Context(), call)
	if err != nil || state.State != RefundPending {
		t.Fatalf("missing refund %+v %v", state, err)
	}
	status, body = http.StatusNotFound, []byte(`{"code":"MCH_NOT_EXISTS"}`)
	if _, err := channel.QueryRefund(t.Context(), call); err == nil {
		t.Fatal("other 404 became pending")
	}
	status, body = http.StatusOK, wpTestJSONBody(map[string]any{
		"refund_id": "WX-NEW", "out_refund_no": "rfd_OTHER", "out_trade_no": order.TypeID(),
		"status": "SUCCESS", "amount": map[string]any{"total": 10000, "refund": 3000, "currency": "CNY"},
	})
	if _, err := channel.QueryRefund(t.Context(), call); err == nil {
		t.Fatal("query echoed another refund")
	}
	status, body = http.StatusOK, wpTestJSONBody(map[string]any{
		"refund_id": "WX-NEW", "out_refund_no": refund.TypeID(), "out_trade_no": order.TypeID(),
		"status": "SUCCESS", "amount": map[string]any{"total": 10000, "refund": 3001, "currency": "CNY"},
	})
	if _, err := channel.QueryRefund(t.Context(), call); err == nil {
		t.Fatal("query echoed another amount")
	}
	status, body = http.StatusOK, makeBody("UNKNOWN")
	if _, err := channel.QueryRefund(t.Context(), call); err == nil {
		t.Fatal("unknown refund status accepted")
	}
	gateway.omit = true
	if _, err := channel.QueryRefund(t.Context(), call); err == nil {
		t.Fatal("unsigned refund response accepted")
	}
}

func TestWechatPayRefundErrorCodes(t *testing.T) {
	var call RefundCall
	gateway := &wpTestGateway{t: t, respond: func(r *http.Request, _ []byte) (int, []byte) {
		if r.Method == http.MethodGet {
			return http.StatusOK, wpQueryJSON(call.Order.TypeID(), "SUCCESS",
				map[string]any{"total": 100, "currency": "CNY"})
		}
		return http.StatusBadRequest, []byte(`{"code":"USER_ACCOUNT_ABNORMAL","message":"do not log this"}`)
	}}
	server := wpTestServer(t, gateway)
	channel := wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), false))
	call = RefundCall{
		Refund: RefundID(uuid.NewV7()), Order: OrderID(uuid.NewV7()),
		Amount: Money{Currency: currency(t, "CNY"), Minor: 100},
	}
	_, err := channel.Refund(t.Context(), call)
	var rejected *Rejected
	if !errors.As(err, &rejected) || strings.Contains(err.Error(), "do not log this") {
		t.Fatalf("refund error %v", err)
	}
}
