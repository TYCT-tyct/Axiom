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

//nolint:cyclop,gocognit // one branch per protocol edge; the test reads as a table of cases
func TestWechatPayQueryStates(t *testing.T) {
	var mu sync.Mutex
	var reply struct {
		status int
		body   []byte
	}
	gateway := &wpTestGateway{t: t, respond: func(r *http.Request, _ []byte) (int, []byte) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/v3/pay/transactions/out-trade-no/") {
			t.Errorf("unexpected query %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("mchid") != wpTestMchID {
			t.Errorf("query did not carry the merchant: %s", r.URL.RawQuery)
		}
		mu.Lock()
		defer mu.Unlock()
		return reply.status, reply.body
	}}
	server := wpTestServer(t, gateway)
	channel := wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), false))
	order := "ord_01ARZ3NDEKTSV4RRFFQ69G5FAV"

	states := map[string]PayState{
		"SUCCESS": PayPaid, "REFUND": PayPaid,
		"NOTPAY": PayPending, "USERPAYING": PayPending,
		"CLOSED": PayClosed, "REVOKED": PayClosed, "PAYERROR": PayClosed,
	}
	for state, want := range states {
		t.Run(state, func(t *testing.T) {
			mu.Lock()
			reply.status, reply.body = http.StatusOK, wpTestJSONBody(map[string]any{
				"appid": wpTestAppID, "mchid": wpTestMchID, "out_trade_no": order,
				"transaction_id": "T-" + order, "trade_state": state,
				"amount": map[string]any{"total": 12345, "currency": "CNY"},
			})
			mu.Unlock()
			payment, err := channel.Query(t.Context(), order, "")
			if err != nil {
				t.Fatal(err)
			}
			if payment.State != want || payment.TradeNo != "T-"+order || payment.Paid.Minor != 12345 {
				t.Fatalf("payment %+v, want state %v", payment, want)
			}
		})
	}

	mu.Lock()
	reply.status, reply.body = http.StatusNotFound, []byte(`{"code":"ORDER_NOT_EXIST","message":"no"}`)
	mu.Unlock()
	payment, err := channel.Query(t.Context(), order, "")
	if err != nil || payment.State != PayPending || payment.Paid.Minor != 0 {
		t.Fatalf("missing order %+v %v", payment, err)
	}

	mu.Lock()
	reply.status, reply.body = http.StatusNotFound, []byte(`{"code":"MCH_NOT_EXISTS","message":"no"}`)
	mu.Unlock()
	if _, err := channel.Query(t.Context(), order, ""); err == nil {
		t.Fatal("a signed 404 that is not ORDER_NOT_EXIST became pending")
	}

	mu.Lock()
	reply.status, reply.body = http.StatusOK, wpTestJSONBody(map[string]any{
		"out_trade_no": "ord_OTHER", "trade_state": "SUCCESS",
	})
	mu.Unlock()
	if _, err := channel.Query(t.Context(), order, ""); err == nil {
		t.Fatal("query echoed another order")
	}

	mu.Lock()
	reply.status, reply.body = http.StatusOK, wpTestJSONBody(map[string]any{
		"out_trade_no": order, "trade_state": "SUCCESS",
		"amount": map[string]any{"total": "12345", "currency": "CNY"},
	})
	mu.Unlock()
	payment, err = channel.Query(t.Context(), order, "")
	if err != nil || payment.Paid.Minor != -1 {
		t.Fatalf("string amount %+v %v", payment, err)
	}

	mu.Lock()
	reply.status, reply.body = http.StatusOK, wpTestJSONBody(map[string]any{
		"out_trade_no": order, "trade_state": "SUCCESS",
		"amount": map[string]any{"total": 12345, "currency": "USD"},
	})
	mu.Unlock()
	payment, err = channel.Query(t.Context(), order, "")
	if err != nil || payment.Paid.Minor != -1 {
		t.Fatalf("USD amount %+v %v", payment, err)
	}

	mu.Lock()
	reply.status, reply.body = http.StatusOK, wpTestJSONBody(map[string]any{
		"out_trade_no": order, "trade_state": "SUCCESS",
	})
	mu.Unlock()
	payment, err = channel.Query(t.Context(), order, "")
	if err != nil || payment.Paid.Minor != 0 {
		t.Fatalf("missing amount %+v %v", payment, err)
	}

	mu.Lock()
	reply.status, reply.body = http.StatusOK, wpTestJSONBody(map[string]any{
		"out_trade_no": order, "trade_state": "WAIT",
	})
	mu.Unlock()
	if _, err := channel.Query(t.Context(), order, ""); err == nil {
		t.Fatal("unknown trade state accepted")
	}

	gateway.omit = true
	if _, err := channel.Query(t.Context(), order, ""); err == nil {
		t.Fatal("unsigned query response accepted")
	}
	gateway.omit = false
	gateway.corrupt = true
	if _, err := channel.Query(t.Context(), order, ""); err == nil {
		t.Fatal("corrupt query signature accepted")
	}
	gateway.corrupt = false
	gateway.serial = "DEADBEEF"
	if _, err := channel.Query(t.Context(), order, ""); err == nil {
		t.Fatal("unknown serial accepted")
	}
	gateway.serial = ""
}

func TestWechatPayQueryWithPublicKey(t *testing.T) {
	gateway := &wpTestGateway{t: t, serial: wpTestKeyID, respond: func(r *http.Request, _ []byte) (int, []byte) {
		return http.StatusOK, wpTestJSONBody(map[string]any{
			"out_trade_no": "ord_01ARZ3NDEKTSV4RRFFQ69G5FAV", "trade_state": "SUCCESS",
			"transaction_id": "T", "amount": map[string]any{"total": 1, "currency": "CNY"},
		})
	}}
	server := wpTestServer(t, gateway)
	channel := wpOpen(t, wpPublicConfig(t, server.URL, server.Certificate(), false))
	payment, err := channel.Query(t.Context(), "ord_01ARZ3NDEKTSV4RRFFQ69G5FAV", "")
	if err != nil || payment.State != PayPaid || payment.Paid.Minor != 1 {
		t.Fatalf("public-key query %+v %v", payment, err)
	}
}

func TestWechatPayTestConfig(t *testing.T) {
	var reply struct {
		status int
		body   []byte
	}
	gateway := &wpTestGateway{t: t, respond: func(r *http.Request, _ []byte) (int, []byte) {
		id := strings.TrimPrefix(r.URL.Path, "/v3/pay/transactions/out-trade-no/")
		if !strings.HasPrefix(id, "probe_") || len(id) != 32 {
			t.Errorf("probe order %q is not 32 bytes", id)
		}
		return reply.status, reply.body
	}}
	server := wpTestServer(t, gateway)
	channel := wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), false)).(*wechatChannel)
	reply.status, reply.body = http.StatusNotFound, []byte(`{"code":"ORDER_NOT_EXIST"}`)
	if err := channel.testConfig(t.Context()); err != nil {
		t.Fatal(err)
	}
	reply.status, reply.body = http.StatusOK, []byte(`{"out_trade_no":"probe_x","trade_state":"SUCCESS"}`)
	if err := channel.testConfig(t.Context()); err == nil {
		t.Fatal("test accepted a found order")
	}
	reply.status, reply.body = http.StatusNotFound, []byte(`{"code":"MCH_NOT_EXISTS"}`)
	if err := channel.testConfig(t.Context()); err == nil {
		t.Fatal("test accepted another 404")
	}
	gateway.omit = true
	if err := channel.testConfig(t.Context()); err == nil {
		t.Fatal("test accepted an unsigned 404")
	}
}

func TestWechatPayClose(t *testing.T) {
	var status int
	var body []byte
	gateway := &wpTestGateway{t: t, respond: func(r *http.Request, request []byte) (int, []byte) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/close") {
			t.Errorf("unexpected close %s %s", r.Method, r.URL.Path)
		}
		var parsed struct {
			MchID string `json:"mchid"`
		}
		if err := jsonx.Unmarshal(request, &parsed); err != nil || parsed.MchID != wpTestMchID {
			t.Errorf("close body %s (%v)", request, err)
		}
		return status, body
	}}
	server := wpTestServer(t, gateway)
	channel := wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), false))
	order := OrderID(uuid.NewV7())

	status, body = http.StatusNoContent, nil
	if err := channel.Close(t.Context(), order, ""); err != nil {
		t.Fatal(err)
	}
	status, body = http.StatusNotFound, []byte(`{"code":"ORDER_NOT_EXIST"}`)
	if err := channel.Close(t.Context(), order, ""); err != nil {
		t.Fatalf("already absent order: %v", err)
	}
	status, body = http.StatusBadRequest, []byte(`{"code":"PARAM_ERROR","message":"secret"}`)
	if err := channel.Close(t.Context(), order, ""); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("close error %v", err)
	}
	status, body = http.StatusInternalServerError, []byte(`{"code":"SYSTEM_ERROR"}`)
	if err := channel.Close(t.Context(), order, ""); err == nil {
		t.Fatal("close accepted a server error")
	}
	status, body = http.StatusNotFound, []byte(`{"code":"ORDER_NOT_EXIST"}`)
	gateway.omit = true
	if err := channel.Close(t.Context(), order, ""); err == nil {
		t.Fatal("close accepted an unsigned 404")
	}
}

//nolint:cyclop,gocognit // one branch per protocol edge; the test reads as a table of cases
func TestWechatPayRefund(t *testing.T) {
	var mu sync.Mutex
	var refundStatus string
	var refundEcho struct {
		outRefundNo, outTradeNo string
		total, refund           int64
	}
	var lastRefund []byte
	var queryBody []byte
	gateway := &wpTestGateway{t: t, respond: func(r *http.Request, body []byte) (int, []byte) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v3/pay/transactions/out-trade-no/"):
			return http.StatusOK, queryBody
		case r.Method == http.MethodPost && r.URL.Path == "/v3/refund/domestic/refunds":
			mu.Lock()
			lastRefund = bytes.Clone(body)
			status, echo := refundStatus, refundEcho
			mu.Unlock()
			if status == "" {
				status = "SUCCESS"
			}
			return http.StatusOK, wpTestJSONBody(map[string]any{
				"refund_id": "WX-" + echo.outRefundNo, "out_refund_no": echo.outRefundNo, "out_trade_no": echo.outTradeNo,
				"status": status, "amount": map[string]any{"total": echo.total, "refund": echo.refund, "currency": "CNY"},
			})
		default:
			t.Errorf("unexpected refund request %s %s", r.Method, r.URL.Path)
			return http.StatusNotFound, nil
		}
	}}
	server := wpTestServer(t, gateway)
	channel := wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), false))
	order := OrderID(uuid.NewV7())
	refund := RefundID(uuid.NewV7())
	queryBody = wpTestJSONBody(map[string]any{
		"out_trade_no": order.TypeID(), "trade_state": "SUCCESS",
		"transaction_id": "T", "amount": map[string]any{"total": 10000, "currency": "CNY"},
	})
	mu.Lock()
	refundEcho = struct {
		outRefundNo, outTradeNo string
		total, refund           int64
	}{refund.TypeID(), order.TypeID(), 10000, 3000}
	mu.Unlock()
	call := RefundCall{Refund: refund, Order: order, Amount: Money{Currency: currency(t, "CNY"), Minor: 3000}, Reason: strings.Repeat("退", 100)}
	state, err := channel.Refund(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != RefundSucceeded || state.ProviderRef != "WX-"+refund.TypeID() {
		t.Fatalf("refund state %+v", state)
	}
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
	if err := jsonx.Unmarshal(lastRefund, &body); err != nil {
		t.Fatal(err)
	}
	if body.OutTradeNo != order.TypeID() || body.OutRefundNo != refund.TypeID() || body.Amount.Refund != 3000 ||
		body.Amount.Total != 10000 || body.Amount.Currency != "CNY" {
		t.Fatalf("refund body %s", lastRefund)
	}
	if len(body.Reason) > 80 || !utf8.ValidString(body.Reason) {
		t.Fatalf("reason %q is %d bytes", body.Reason, len(body.Reason))
	}

	for _, tc := range []struct {
		status string
		want   RefundOutcome
	}{{"PROCESSING", RefundPending}, {"ABNORMAL", RefundPending}, {"CLOSED", RefundFailed}} {
		t.Run(tc.status, func(t *testing.T) {
			mu.Lock()
			refundStatus = tc.status
			mu.Unlock()
			state, err := channel.Refund(t.Context(), call)
			if err != nil || state.State != tc.want {
				t.Fatalf("%+v %v", state, err)
			}
		})
	}
	mu.Lock()
	refundStatus = ""
	mu.Unlock()

	// A 429 may mean the refund was accepted: it must stay unknown, not fail.
	mu.Lock()
	refundStatus = ""
	mu.Unlock()
	gateway.respond = func(r *http.Request, body []byte) (int, []byte) {
		if r.Method == http.MethodGet {
			return http.StatusOK, queryBody
		}
		return http.StatusTooManyRequests, []byte(`{"code":"FREQUENCY_LIMITED"}`)
	}
	if _, err := channel.Refund(t.Context(), call); err == nil || wpTestRejected(err) {
		t.Fatalf("429 became %v", err)
	}
	gateway.respond = func(r *http.Request, body []byte) (int, []byte) {
		if r.Method == http.MethodGet {
			return http.StatusOK, queryBody
		}
		return http.StatusBadRequest, []byte(`{"code":"PARAM_ERROR"}`)
	}
	if _, err := channel.Refund(t.Context(), call); !wpTestRejected(err) {
		t.Fatalf("400 became %v", err)
	}
	gateway.respond = func(r *http.Request, body []byte) (int, []byte) {
		if r.Method == http.MethodGet {
			return http.StatusOK, queryBody
		}
		return http.StatusInternalServerError, []byte(`{"code":"SYSTEM_ERROR"}`)
	}
	if _, err := channel.Refund(t.Context(), call); err == nil || wpTestRejected(err) {
		t.Fatalf("500 became %v", err)
	}

	// The original total and the echoed refund come from the order and the response, not from the caller.
	gateway.respond = func(r *http.Request, body []byte) (int, []byte) {
		if r.Method == http.MethodGet {
			return http.StatusOK, queryBody
		}
		return http.StatusOK, wpTestJSONBody(map[string]any{
			"refund_id": "WX-1", "out_refund_no": refund.TypeID(), "out_trade_no": order.TypeID(),
			"status": "SUCCESS", "amount": map[string]any{"total": 9999, "refund": 3000, "currency": "CNY"},
		})
	}
	if _, err := channel.Refund(t.Context(), call); err == nil {
		t.Fatal("wrong echoed total accepted")
	}
	gateway.respond = func(r *http.Request, body []byte) (int, []byte) {
		if r.Method == http.MethodGet {
			return http.StatusOK, queryBody
		}
		return http.StatusOK, wpTestJSONBody(map[string]any{
			"refund_id": "WX-1", "out_refund_no": refund.TypeID(), "out_trade_no": order.TypeID(),
			"status": "SUCCESS", "amount": map[string]any{"total": 10000, "refund": 3001, "currency": "CNY"},
		})
	}
	if _, err := channel.Refund(t.Context(), call); err == nil {
		t.Fatal("wrong echoed refund accepted")
	}

	queryBody = wpTestJSONBody(map[string]any{"out_trade_no": order.TypeID(), "trade_state": "CLOSED"})
	if _, err := channel.Refund(t.Context(), call); !wpTestRejected(err) {
		t.Fatalf("closed order refund %v", err)
	}
	queryBody = wpTestJSONBody(map[string]any{"out_trade_no": order.TypeID(), "trade_state": "NOTPAY"})
	if _, err := channel.Refund(t.Context(), call); !wpTestRejected(err) {
		t.Fatalf("unpaid order refund %v", err)
	}
	queryBody = wpTestJSONBody(map[string]any{"out_trade_no": order.TypeID(), "trade_state": "SUCCESS"})
	if _, err := channel.Refund(t.Context(), call); !wpTestRejected(err) {
		t.Fatalf("refund without an original total %v", err)
	}
	queryBody = wpTestJSONBody(map[string]any{"out_trade_no": order.TypeID(), "trade_state": "SUCCESS", "amount": map[string]any{"total": 10000, "currency": "CNY"}})
	tooMuch := call
	tooMuch.Amount.Minor = 10001
	if _, err := channel.Refund(t.Context(), tooMuch); !wpTestRejected(err) {
		t.Fatalf("over-refund %v", err)
	}
	// A missing order is a definite refusal, not a pending query.
	gateway.respond = func(r *http.Request, body []byte) (int, []byte) {
		if r.Method == http.MethodGet {
			return http.StatusNotFound, []byte(`{"code":"ORDER_NOT_EXIST"}`)
		}
		return http.StatusOK, nil
	}
	if _, err := channel.Refund(t.Context(), call); !wpTestRejected(err) {
		t.Fatalf("missing order refund %v", err)
	}
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
			return http.StatusOK, wpTestJSONBody(map[string]any{
				"out_trade_no": call.Order.TypeID(), "trade_state": "SUCCESS",
				"amount": map[string]any{"total": 100, "currency": "CNY"},
			})
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

func wpTestJSONBody(v any) []byte {
	body, err := jsonx.MarshalCanonical(v)
	if err != nil {
		panic(err)
	}
	return body
}
