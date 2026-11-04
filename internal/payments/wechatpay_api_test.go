package payments

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/platform/jsonx"
)

// wpQueryFixture is the query walk's gateway and channel, with one answer the test replaces per case.
type wpQueryFixture struct {
	t       *testing.T
	mu      sync.Mutex
	status  int
	body    []byte
	gateway *wpTestGateway
	channel Channel
	order   string
}

func newWPQueryFixture(t *testing.T) *wpQueryFixture {
	t.Helper()
	f := &wpQueryFixture{t: t, order: "ord_01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	f.gateway = &wpTestGateway{t: t, respond: func(r *http.Request, _ []byte) (int, []byte) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/v3/pay/transactions/out-trade-no/") {
			t.Errorf("unexpected query %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("mchid") != wpTestMchID {
			t.Errorf("query did not carry the merchant: %s", r.URL.RawQuery)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.status, f.body
	}}
	server := wpTestServer(t, f.gateway)
	f.channel = wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), false))
	return f
}

func (f *wpQueryFixture) reply(status int, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

// checkOrderEdges asserts the missing order, foreign and echoed replies, the amount shapes and an unknown state.
func (f *wpQueryFixture) checkOrderEdges() {
	f.t.Helper()
	f.reply(http.StatusNotFound, []byte(`{"code":"ORDER_NOT_EXIST","message":"no"}`))
	payment, err := f.channel.Query(f.t.Context(), f.order, "")
	if err != nil || payment.State != PayPending || payment.Paid.Minor != 0 {
		f.t.Fatalf("missing order %+v %v", payment, err)
	}

	f.reply(http.StatusNotFound, []byte(`{"code":"MCH_NOT_EXISTS","message":"no"}`))
	if _, err := f.channel.Query(f.t.Context(), f.order, ""); err == nil {
		f.t.Fatal("a signed 404 that is not ORDER_NOT_EXIST became pending")
	}

	f.reply(http.StatusOK, wpTestJSONBody(map[string]any{
		"appid": wpTestAppID, "mchid": wpTestMchID, "out_trade_no": "ord_OTHER", "trade_state": "SUCCESS",
	}))
	if _, err := f.channel.Query(f.t.Context(), f.order, ""); err == nil {
		f.t.Fatal("query echoed another order")
	}

	for _, tc := range []struct {
		name   string
		amount any
		want   int64
	}{
		{"string amount", map[string]any{"total": "12345", "currency": "CNY"}, -1},
		{"USD amount", map[string]any{"total": 12345, "currency": "USD"}, -1},
		{"missing amount", nil, 0},
	} {
		f.t.Run(tc.name, func(t *testing.T) {
			reply := map[string]any{
				"appid": wpTestAppID, "mchid": wpTestMchID, "out_trade_no": f.order, "trade_state": "SUCCESS",
			}
			if tc.amount != nil {
				reply["amount"] = tc.amount
			}
			f.reply(http.StatusOK, wpTestJSONBody(reply))
			payment, err := f.channel.Query(t.Context(), f.order, "")
			if err != nil || payment.Paid.Minor != tc.want {
				t.Fatalf("%s %+v %v", tc.name, payment, err)
			}
		})
	}

	f.reply(http.StatusOK, wpTestJSONBody(map[string]any{
		"appid": wpTestAppID, "mchid": wpTestMchID, "out_trade_no": f.order, "trade_state": "WAIT",
	}))
	if _, err := f.channel.Query(f.t.Context(), f.order, ""); err == nil {
		f.t.Fatal("unknown trade state accepted")
	}
}

// checkSignatures asserts an unsigned, corrupt or unknown-serial response is refused.
func (f *wpQueryFixture) checkSignatures() {
	f.t.Helper()
	for _, tc := range []struct {
		name  string
		setup func()
		reset func()
	}{
		{"unsigned", func() { f.gateway.omit = true }, func() { f.gateway.omit = false }},
		{"corrupt signature", func() { f.gateway.corrupt = true }, func() { f.gateway.corrupt = false }},
		{"unknown serial", func() { f.gateway.serial = "DEADBEEF" }, func() { f.gateway.serial = "" }},
	} {
		f.t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			defer tc.reset()
			if _, err := f.channel.Query(t.Context(), f.order, ""); err == nil {
				t.Fatalf("%s accepted", tc.name)
			}
		})
	}
}

func TestWechatPayQueryStates(t *testing.T) {
	f := newWPQueryFixture(t)
	order := f.order

	states := map[string]PayState{
		"SUCCESS": PayPaid, "REFUND": PayPaid,
		"NOTPAY": PayPending, "USERPAYING": PayPending,
		"CLOSED": PayClosed, "REVOKED": PayClosed, "PAYERROR": PayClosed,
	}
	for state, want := range states {
		t.Run(state, func(t *testing.T) {
			f.reply(http.StatusOK, wpTestJSONBody(map[string]any{
				"appid": wpTestAppID, "mchid": wpTestMchID, "out_trade_no": order,
				"transaction_id": "T-" + order, "trade_state": state,
				"amount": map[string]any{"total": 12345, "currency": "CNY"},
			}))
			payment, err := f.channel.Query(t.Context(), order, "")
			if err != nil {
				t.Fatal(err)
			}
			if payment.State != want || payment.TradeNo != "T-"+order || payment.Paid.Minor != 12345 {
				t.Fatalf("payment %+v, want state %v", payment, want)
			}
		})
	}

	for name, change := range map[string]func(map[string]any){
		"another merchant": func(m map[string]any) { m["mchid"] = "1900000002" },
		"another app":      func(m map[string]any) { m["appid"] = "wx0000000000000000" },
		"missing merchant": func(m map[string]any) { delete(m, "mchid") },
		"missing app":      func(m map[string]any) { delete(m, "appid") },
	} {
		t.Run(name, func(t *testing.T) {
			body := wpTransaction(order, "T-"+order, 12345, "SUCCESS")
			change(body)
			f.reply(http.StatusOK, wpTestJSONBody(body))
			if _, err := f.channel.Query(t.Context(), order, ""); err == nil {
				t.Fatal("query from another app or merchant accepted")
			}
		})
	}

	f.checkOrderEdges()
	f.checkSignatures()
}

func TestWechatPayQueryWithPublicKey(t *testing.T) {
	gateway := &wpTestGateway{t: t, serial: wpTestKeyID, respond: func(r *http.Request, _ []byte) (int, []byte) {
		return http.StatusOK, wpQueryJSON("ord_01ARZ3NDEKTSV4RRFFQ69G5FAV", "SUCCESS",
			map[string]any{"total": 1, "currency": "CNY"})
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
	reply.status, reply.body = http.StatusOK, wpQueryJSON("probe_x", "SUCCESS", nil)
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
