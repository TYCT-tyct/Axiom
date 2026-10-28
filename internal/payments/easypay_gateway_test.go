package payments

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
)

func easyPayServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewTLSServer(handler)
	t.Cleanup(s.Close)
	return s
}

func easyPayWrite(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil { //nolint:gosec // deliberately serves protocol fixtures, including HTML failure bodies, only on a test server
		t.Error(err)
	}
}

func TestEasyPayCreateModesAndLinks(t *testing.T) {
	for _, tc := range []struct {
		name, body, pay, qr string
		bad                 bool
	}{
		{"relative", `{"code":"1","trade_no":"T1","payurl":"/checkout?token=x","qrcode":"weixin://pay/token"}`, "https://pay.example.com/checkout?token=x", "weixin://pay/token", false},
		{"mobile", `{"code":1,"payurl":"https://pay.example.com/desktop","payurl2":"alipays://pay/mobile"}`, "alipays://pay/mobile", "", false},
		{"qr only", `{"code":1,"qrcode":"wxp://pay/token"}`, "", "wxp://pay/token", false},
		{"double slash", `{"code":1,"payurl":"//evil.example/x"}`, "", "", true},
		{"qr double slash", `{"code":1,"payurl":"https://pay.example.com/x","qrcode":"//evil.example/x"}`, "", "", true},
		{"http", `{"code":1,"payurl":"http://pay.example.com/x"}`, "", "", true},
		{"javascript", `{"code":1,"payurl":"javascript:alert(1)"}`, "", "", true},
		{"data", `{"code":1,"qrcode":"data:text/plain,x"}`, "", "", true},
		{"opaque token", `{"code":1,"qrcode":"OrderToken"}`, "", "", true},
		{"failure code", `{"code":"0","payurl":"https://pay.example.com/x"}`, "", "", true},
		{"fractional code", `{"code":1.1,"payurl":"https://pay.example.com/x"}`, "", "", true},
		{"empty", `{"code":1}`, "", "", true},
		{"HTML", `<html>failed</html>`, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := easyPayOpen(t, easyPayConfig(t, "https://pay.example.com/prefix"))
			result, err := c.(*easyPayChannel).createResponse([]byte(tc.body), true)
			if (err != nil) != tc.bad {
				t.Fatalf("checkout error %v, want bad=%v", err, tc.bad)
			}
			if tc.bad {
				return
			}
			if diff := cmp.Diff([]string{tc.pay, tc.qr}, []string{result.PayURL, result.QRCode}); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestEasyPayRedirect(t *testing.T) {
	cfg := easyPayConfig(t, "https://pay.example.com/prefix/API.php?discard=x#x")
	var s easyPaySettings
	if err := jsonx.UnmarshalKnown(cfg.Settings, &s); err != nil {
		t.Fatal(err)
	}
	s.Mode = "redirect"
	var err error
	cfg.Settings, err = jsonx.MarshalCanonical(s)
	if err != nil {
		t.Fatal(err)
	}
	request := easyPayRequest(t)
	request.NotifyURL = "https://callbacks.example.com/notify"
	c := easyPayOpen(t, cfg)
	result, err := c.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(result.PayURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/prefix/submit.php" || u.Query().Get("notify_url") != request.NotifyURL || u.Query().Get("sign") != epaySignature(u.Query(), theKey) || u.Query().Has("clientip") || result.TradeNo != "" {
		t.Fatal("wrong redirect parameters")
	}
}

func TestEasyPayQueryResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		state      PayState
		amount     int64
		bad        bool
	}{
		{"PHP", `{"code":"1","status":"1","money":"100.0000","trade_no":"T1","out_trade_no":"order"}`, PayPaid, 10000, false},
		{"numeric", `{"code":1,"status":1,"money":100.00,"trade_no":123}`, PayPaid, 10000, false},
		{"nested", `{"code":"1","data":{"trade_status":"TRADE_SUCCESS","money":"100.00","trade_no":"T1","out_trade_no":"order"}}`, PayPaid, 10000, false},
		{"absent code", `{"status":"1","money":"100.00","trade_no":"T1"}`, PayPaid, 10000, false},
		{"failure wins", `{"code":"-1","trade_status":"TRADE_SUCCESS","status":"1","money":"100.00"}`, PayPending, 0, true},
		{"wrong order", `{"code":1,"status":1,"out_trade_no":"different"}`, PayPending, 0, true},
		{"wrong nested order", `{"code":1,"out_trade_no":"order","data":{"out_trade_no":"different"}}`, PayPending, 0, true},
		{"status precedence", `{"code":1,"trade_status":"WAIT_BUYER_PAY","status":1,"money":"100.00"}`, PayPending, 10000, false},
		{"top precedence", `{"status":"0","money":"0","data":{"status":"1","money":"100.00"}}`, PayPending, -1, false},
		{"precision", `{"status":1,"money":"100.001","trade_no":"T1"}`, PayPaid, -1, false},
		{"zero amount", `{"status":1,"money":"0.00","trade_no":"T1"}`, PayPaid, -1, false},
		{"formatted amount", `{"status":1,"money":"1,000.00","trade_no":"T1"}`, PayPaid, -1, false},
		{"empty trade status", `{"trade_status":"","status":"1","money":"100.00","trade_no":"T1"}`, PayPaid, 10000, false},
		{"missing amount", `{"status":1,"trade_no":"T1"}`, PayPaid, 0, false},
		{"bad amount", `{"status":1,"money":"1e2"}`, PayPaid, -1, false},
		{"fractional code", `{"code":1.1,"status":1}`, PayPending, 0, true},
		{"duplicate code", `{"code":0,"code":1,"status":1}`, PayPending, 0, true},
		{"bool code", `{"code":true,"status":1}`, PayPending, 0, true},
		{"html", `<html>error</html>`, PayPending, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := url.Values{"act": {"order"}, "pid": {"1001"}, "key": {theKey}, "out_trade_no": {"order"}}
			response := easyPayAPIResponse{t: t, body: tc.body, form: want, query: want}
			gateway := easyPayServer(t, response.ServeHTTP)
			c := easyPayOpen(t, easyPayConfig(t, gateway.URL, gateway.Certificate()))
			got, err := c.Query(t.Context(), "order", "ignored-trade")
			if (err != nil) != tc.bad {
				t.Fatalf("query error %v, want bad=%v", err, tc.bad)
			}
			if tc.bad {
				return
			}
			if want := (Payment{State: tc.state, TradeNo: got.TradeNo, Paid: Money{currency(t, "CNY"), tc.amount}}); want != got {
				t.Fatalf("payment %+v, want %+v", got, want)
			}
			if tc.name == "numeric" && got.TradeNo != "123" {
				t.Fatal(got.TradeNo)
			}
		})
	}
}

func TestEasyPayRefundResponsesAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, body                  string
		httpStatus                  int
		fallback, success, rejected bool
	}{
		{"string success", `{"code":"1"}`, 200, false, true, false},
		{"integer success", `{"code":1}`, 200, false, true, false},
		{"Chinese missing", `{"code":0,"msg":"订单不存在"}`, 200, true, true, false},
		{"English missing", `{"code":"-1","msg":"Order not found"}`, 200, true, true, false},
		{"other refusal", `{"code":0,"msg":"insufficient balance"}`, 200, false, false, true},
		{"nonexact missing", `{"code":0,"msg":"refund state unknown, not exist?"}`, 200, false, false, true},
		{"HTML", `<html>订单不存在</html>`, 200, false, false, false},
		{"empty", "", 200, false, false, false},
		{"missing code", `{"msg":"订单不存在"}`, 200, false, false, false},
		{"invalid code", `{"code":1.1,"msg":"订单不存在"}`, 200, false, false, false},
		{"HTTP failure", `{"code":0,"msg":"订单不存在"}`, 500, false, false, false},
		{"redirect", `{"code":1}`, 302, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			r := RefundCall{Order: easyPayRequest(t).Order, TradeNo: "T1", Amount: Money{currency(t, "CNY"), 1234}}
			response := easyPayRefundGateway{t: t, calls: &calls, order: r.Order, body: tc.body, status: tc.httpStatus}
			gateway := easyPayServer(t, response.ServeHTTP)
			got, err := easyPayOpen(t, easyPayConfig(t, gateway.URL, gateway.Certificate())).Refund(t.Context(), r)
			var rejected *Rejected
			if diff := cmp.Diff([]bool{tc.success, tc.rejected}, []bool{err == nil, errors.As(err, &rejected)}); diff != "" {
				t.Fatalf("%s: %v", diff, err)
			}
			if tc.success && got.State != RefundSucceeded {
				t.Fatal(got)
			}
			want := int32(1)
			if tc.fallback {
				want = 2
			}
			if calls.Load() != want {
				t.Fatalf("refund calls %d, want %d", calls.Load(), want)
			}
		})
	}
}

func TestEasyPayEgressBoundsAndRedaction(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider(trace.WithSyncer(exporter))
	defer func() {
		if err := tp.Shutdown(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	var calls atomic.Int32
	gateway := easyPayServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			easyPayWrite(t, w, `{"code":1,"status":"1","money":"1.00","trade_no":"T"}`)
		case 2:
			easyPayWrite(t, w, `{"code":0,"msg":"`+theKey+`?key=`+theKey+`"}`)
		case 3:
			easyPayWrite(t, w, strings.Repeat("x", (1<<20)+1))
		case 4:
			w.Header().Set("Location", "/key="+theKey)
			w.WriteHeader(http.StatusFound)
		default:
			easyPayWrite(t, w, `{"code":"`+theKey+`"}`)
		}
	})
	cfg := easyPayConfig(t, gateway.URL, gateway.Certificate())
	client := egress.NewForTesting(egresstest.Loopback(t, gateway.Certificate()), egress.WithTracerProvider(tp))
	t.Cleanup(client.CloseIdleConnections)
	cfg.Client = client
	c := easyPayOpen(t, cfg)
	for i := range 5 {
		_, err := c.Query(t.Context(), "private-order", "")
		if i > 0 && err == nil {
			t.Fatal("accepted invalid gateway response")
		}
		if err != nil {
			easyPayNoSecrets(t, err.Error(), theKey, "?")
		}
	}
	gateway.Close()
	_, err := c.Query(t.Context(), "private-order", "")
	if err == nil {
		t.Fatal("expected network error")
	}
	easyPayNoSecrets(t, err.Error(), theKey, "?")
	easyPayCheckSpans(t, exporter)
}

func easyPayNoSecrets(t *testing.T, text string, markers ...string) {
	t.Helper()
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			t.Fatalf("output leaked marker %q", marker)
		}
	}
}

func TestEasyPayCreateRequest(t *testing.T) {
	request := easyPayRequest(t)
	cfg := easyPayConfig(t, "https://pay.example.com")
	gateway := easyPayServer(t, func(w http.ResponseWriter, r *http.Request) {
		if diff := cmp.Diff([]string{http.MethodPost, "/prefix/mapi.php", ""}, []string{r.Method, r.URL.Path, r.URL.RawQuery}); diff != "" {
			t.Error(diff)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		want := url.Values{"pid": {"1001"}, "out_trade_no": {request.Order.TypeID()}, "money": {"123.45"}, "type": {"ali"}, "cid": {"2"}, "device": {"mobile"}, "clientip": {"203.0.113.7"}, "name": {request.Subject}, "return_url": {request.ReturnURL}, "notify_url": {request.NotifyURL}, "sign_type": {"MD5"}}
		want.Set("sign", epaySignature(want, theKey))
		if diff := cmp.Diff(want, r.PostForm); diff != "" {
			t.Error(diff)
		}
		easyPayWrite(t, w, `{"code":1,"trade_no":"T1","payurl":"https://pay.example.com/pay"}`)
	})
	cfg.Client = egress.NewForTesting(egresstest.Loopback(t, gateway.Certificate()))
	t.Cleanup(cfg.Client.CloseIdleConnections)
	cfg.Settings = []byte(strings.ReplaceAll(string(cfg.Settings), "https://pay.example.com", gateway.URL+"/prefix/submit.php?discard=x"))
	result, err := easyPayOpen(t, cfg).Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Checkout{TradeNo: "T1", PayURL: "https://pay.example.com/pay"}, result); diff != "" {
		t.Fatal(diff)
	}
}

func easyPayCheckSpans(t *testing.T, exporter *tracetest.InMemoryExporter) {
	t.Helper()
	spans := exporter.GetSpans()
	if len(spans) != 6 {
		t.Fatalf("egress spans %d, want six", len(spans))
	}
	for _, span := range spans {
		easyPayNoSecrets(t, fmt.Sprintf("%+v", span), theKey, "private-order", "?", "/api.php")
	}
}

type easyPayAPIResponse struct {
	t           *testing.T
	body        string
	form, query url.Values
}

func (g easyPayAPIResponse) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		g.t.Error(err)
		return
	}
	if diff := cmp.Diff(g.form, r.PostForm); diff != "" {
		g.t.Error(diff)
	}
	if diff := cmp.Diff(g.query, r.URL.Query()); diff != "" {
		g.t.Error(diff)
	}
	easyPayWrite(g.t, w, g.body)
}

type easyPayRefundGateway struct {
	t      *testing.T
	calls  *atomic.Int32
	order  OrderID
	body   string
	status int
}

func (g easyPayRefundGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := g.calls.Add(1)
	if err := r.ParseForm(); err != nil {
		g.t.Error(err)
		return
	}
	want := url.Values{"act": {"refund"}, "pid": {"1001"}, "key": {theKey}, "money": {"12.34"}, "out_trade_no": {g.order.TypeID()}}
	if n > 1 {
		want.Del("out_trade_no")
		want.Set("trade_no", "T1")
	}
	if diff := cmp.Diff(want, r.PostForm); diff != "" {
		g.t.Error(diff)
	}
	if diff := cmp.Diff(url.Values{"act": {"refund"}}, r.URL.Query()); diff != "" {
		g.t.Error(diff)
	}
	if n == 1 {
		w.WriteHeader(g.status)
		easyPayWrite(g.t, w, g.body)
		return
	}
	easyPayWrite(g.t, w, `{"code":"1"}`)
}

func TestEasyPayCloseResponses(t *testing.T) {
	order := easyPayRequest(t).Order
	for _, body := range []string{`{"code":"1"}`, `{"code":0}`, `<html>error</html>`} {
		want := url.Values{"act": {"close"}, "pid": {"1001"}, "key": {theKey}, "out_trade_no": {order.TypeID()}}
		response := easyPayAPIResponse{t: t, body: body, form: want, query: want}
		gateway := easyPayServer(t, response.ServeHTTP)
		err := easyPayOpen(t, easyPayConfig(t, gateway.URL, gateway.Certificate())).Close(t.Context(), order, "ignored-trade")
		if (err == nil) != (body == `{"code":"1"}`) {
			t.Fatal("close result", err)
		}
	}
}
