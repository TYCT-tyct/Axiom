package payments

import (
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

func ezPayWrite(w http.ResponseWriter, body string) {
	_, _ = io.WriteString(w, body) //nolint:errcheck // httptest.ResponseRecorder writes cannot fail
}

func ezPaySettingsJSON(t *testing.T, gateway, pid string) []byte {
	t.Helper()
	return easyPayJSON(t, easyPaySettings{Preset: "ezpay", Gateway: gateway, PID: pid})
}

func ezPayConfig(t *testing.T, gateway string, certificates ...*x509.Certificate) MerchantConfig {
	t.Helper()
	cfg := easyPayConfig(t, gateway, certificates...)
	cfg.Currency = currency(t, "USD")
	cfg.Settings = ezPaySettingsJSON(t, gateway, "1001")
	return cfg
}

func ezPayRequest(t *testing.T) CreateRequest {
	return CreateRequest{
		Order: OrderID(uuid.NewV7()), Method: "usdt.tron", Upstream: "usdt_trc20",
		NotifyURL: "https://callbacks.example.com/generated", Total: Money{currency(t, "USD"), 1234},
		Subject: "Top-up & credits", ReturnURL: "https://console.example.com/topup?order=test",
		ClientIP: netip.MustParseAddr("203.0.113.7"),
	}
}

func TestEzPayCreateSignatureVectors(t *testing.T) {
	// Digests are fixed from the literal strings, independently of the implementation. The first vector exercises
	// the decode-then-RFC3986 path and the raw-value fallback for a literal percent; the second is the ordinary path.
	for _, tc := range []struct {
		v         url.Values
		key, want string
	}{
		{url.Values{
			"pid": {"1001"}, "type": {"usdt_trc20"}, "out_trade_no": {"ord_demo"},
			"notify_url": {"https://callbacks.example.com/notify"},
			"return_url": {"https://console.example.com/topup"},
			"name":       {"商品 A&B=x 100%"}, "money": {"100.00"}, "currency": {"USD"},
			"sign": {"ignored"}, "sign_type": {"MD5"},
		}, "test-key", "a044d7461cfe6481920ca58ce533adf7"},
		{url.Values{
			"pid": {"1001"}, "type": {"usdt_trc20"}, "out_trade_no": {"ord_demo"},
			"name": {"Top-up & credits"}, "money": {"10.00"},
		}, "test-key", "b7338b55a6e7bc634a11dddf98134824"},
	} {
		if got := ezPayCreateSign(tc.v, tc.key); got != tc.want {
			t.Errorf("signature = %q, want %q", got, tc.want)
		}
	}
}

//nolint:gocognit // one rejected configuration per row; the table is the test
func TestEzPayConfiguration(t *testing.T) {
	cfg := ezPayConfig(t, "https://pay.example.com/prefix/SUBMIT.php/?key=discard#fragment")
	c := easyPayOpen(t, cfg)
	if c.Caps() != CapQuery {
		t.Fatal("EzPay declares query only", c.Caps())
	}
	if methods := c.Methods(); len(methods) != 1 || methods[0] != "usdt.tron" {
		t.Fatal("EzPay offers usdt.tron only", methods)
	}

	base := easyPaySettings{Preset: "ezpay", Gateway: "https://pay.example.com", PID: "1001"}
	for _, tc := range []struct {
		name    string
		edit    func(*easyPaySettings)
		code    string
		secrets map[string]secret.Secret
	}{
		{name: "CNY", code: "CNY"},
		{name: "API mode", edit: func(s *easyPaySettings) { s.Mode = "api" }},
		{name: "close", edit: func(s *easyPaySettings) { s.Close = true }},
		{name: "refund", edit: func(s *easyPaySettings) { s.Refund = true }},
		{name: "channel IDs", edit: func(s *easyPaySettings) { s.CIDs = map[Method]string{"usdt.tron": "2"} }},
		{name: "non-MD5", edit: func(s *easyPaySettings) { s.SignType = "SHA256" }},
		{name: "empty pid", edit: func(s *easyPaySettings) { s.PID = "" }},
		{name: "pid with space", edit: func(s *easyPaySettings) { s.PID = "1001 " }},
		{name: "pid too long", edit: func(s *easyPaySettings) { s.PID = strings.Repeat("a", 33) }},
		{name: "extra secret", secrets: map[string]secret.Secret{"key": secret.New(theKey), "extra": secret.New("x")}},
		{name: "empty key", secrets: map[string]secret.Secret{"key": secret.New("")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			if tc.edit != nil {
				tc.edit(&s)
			}
			raw, err := jsonx.MarshalCanonical(s)
			if err != nil {
				t.Fatal(err)
			}
			secrets := tc.secrets
			if secrets == nil {
				secrets = map[string]secret.Secret{"key": secret.New(theKey)}
			}
			code := tc.code
			if code == "" {
				code = "USD"
			}
			client := egress.NewForTesting(egresstest.Loopback(t))
			t.Cleanup(client.CloseIdleConnections)
			if _, err := (EasyPay{}).Open(MerchantConfig{Currency: currency(t, code), Settings: raw, Secrets: secrets, Client: client}); err == nil {
				t.Fatal("invalid EzPay configuration was accepted")
			}
		})
	}
}

func TestEzPayCreateBuildsASignedRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("redirect checkout made a network call")
	}))
	t.Cleanup(server.Close)
	c := easyPayOpen(t, ezPayConfig(t, server.URL, server.Certificate()))
	r := ezPayRequest(t)
	checkout, err := c.Create(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(checkout.PayURL)
	if err != nil || u.Path != "/submit.php" {
		t.Fatal("wrong checkout", checkout.PayURL, err)
	}
	q := u.Query()
	for name, want := range map[string]string{
		"pid": "1001", "type": "usdt_trc20", "out_trade_no": r.Order.TypeID(),
		"notify_url": r.NotifyURL, "return_url": r.ReturnURL, "name": r.Subject,
		"money": "12.34", "currency": "USD", "sign_type": "MD5",
	} {
		if got := q.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	sign := q.Get("sign")
	if len(sign) != 32 {
		t.Fatal("missing signature", sign)
	}
	q.Del("sign")
	q.Del("sign_type")
	if want := ezPayCreateSign(q, theKey); sign != want {
		t.Fatal("signature does not cover the sent parameters")
	}

	for name, edit := range map[string]func(*CreateRequest){
		"wrong upstream": func(r *CreateRequest) { r.Upstream = "usdt.tron" },
		"wrong currency": func(r *CreateRequest) { r.Total.Currency = currency(t, "CNY") },
		"zero amount":    func(r *CreateRequest) { r.Total.Minor = 0 },
		"HTTP return":    func(r *CreateRequest) { r.ReturnURL = "http://console.example.com/topup" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := r
			edit(&bad)
			_, err := c.Create(t.Context(), bad)
			var rejected *Rejected
			if !errors.As(err, &rejected) || rejected.Code != "invalid_order" {
				t.Fatal("invalid order was not refused", err)
			}
		})
	}
}

//nolint:cyclop,gocognit // one query shape per row and each assertion is the test
func TestEzPayQueryShapes(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api.php" {
			http.Error(w, "wrong request", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		if q.Get("act") != "order" || q.Get("pid") != "1001" || q.Get("key") != theKey {
			http.Error(w, "wrong credentials", http.StatusBadRequest)
			return
		}
		switch q.Get("out_trade_no") {
		case "ord-pending":
			ezPayWrite(w, `{"code":1,"trade_status":"WAIT_BUYER_PAY","money":"10.00","trade_no":"T-pending","out_trade_no":"ord-pending","currency":"USD"}`)
		case "ord-paid":
			ezPayWrite(w, `{"code":"1","trade_status":"TRADE_SUCCESS","money":10,"trade_no":"T-paid","out_trade_no":"ord-paid","currency":"USD"}`)
		case "ord-closed":
			ezPayWrite(w, `{"code":1,"trade_status":"TRADE_CLOSED","money":"10.00","trade_no":"T-closed","out_trade_no":"ord-closed","currency":"USD"}`)
		case "ord-cny":
			ezPayWrite(w, `{"code":1,"trade_status":"TRADE_SUCCESS","money":"70.00","trade_no":"T-cny","out_trade_no":"ord-cny","currency":"CNY"}`)
		case "ord-type":
			ezPayWrite(w, `{"code":1,"trade_status":"TRADE_SUCCESS","money":"10.00","trade_no":"T-type","out_trade_no":"ord-type","type":"usdt_erc20","currency":"USD"}`)
		case "ord-no-echo":
			ezPayWrite(w, `{"code":1,"trade_status":"TRADE_SUCCESS","money":"10.00","trade_no":"T-no-echo","type":"usdt_trc20","currency":"USD"}`)
		case "ord-null-echo":
			ezPayWrite(w, `{"code":1,"trade_status":"TRADE_SUCCESS","money":"10.00","trade_no":"T-null-echo","out_trade_no":null,"type":"usdt_trc20","currency":"USD"}`)
		case "ord-empty-echo":
			ezPayWrite(w, `{"code":1,"trade_status":"TRADE_SUCCESS","money":"10.00","trade_no":"T-empty-echo","out_trade_no":"","type":"usdt_trc20","currency":"USD"}`)
		case "ord-blank-echo":
			ezPayWrite(w, `{"code":1,"trade_status":"TRADE_SUCCESS","money":"10.00","trade_no":"T-blank-echo","out_trade_no":"   ","type":"usdt_trc20","currency":"USD"}`)
		case "ord-missing":
			ezPayWrite(w, `{"code":-1,"msg":"订单不存在"}`)
		case "ord-wrong-key":
			ezPayWrite(w, `{"code":-1,"msg":"密钥错误"}`)
		case "ord-echo":
			ezPayWrite(w, `{"code":1,"trade_status":"TRADE_SUCCESS","money":"10.00","trade_no":"T-echo","out_trade_no":"other","currency":"USD"}`)
		case "ord-status":
			ezPayWrite(w, `{"code":1,"trade_status":"PAID","money":"10.00","trade_no":"T-status","out_trade_no":"ord-status","currency":"USD"}`)
		case "ord-500":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			ezPayWrite(w, `not json`)
		}
	}))
	t.Cleanup(server.Close)
	c := easyPayOpen(t, ezPayConfig(t, server.URL, server.Certificate()))

	for _, tc := range []struct {
		order string
		state PayState
		trade string
		money Money
	}{
		{"ord-pending", PayPending, "T-pending", Money{currency(t, "USD"), 1000}},
		{"ord-paid", PayPaid, "T-paid", Money{currency(t, "USD"), 1000}},
		{"ord-closed", PayClosed, "T-closed", Money{currency(t, "USD"), 1000}},
		{"ord-cny", PayPaid, "T-cny", Money{currency(t, "CNY"), 7000}},
	} {
		got, err := c.Query(t.Context(), tc.order, "")
		if err != nil || got.State != tc.state || got.TradeNo != tc.trade || got.Paid != tc.money {
			t.Errorf("%s: %+v, %v", tc.order, got, err)
		}
	}
	if got, err := c.Query(t.Context(), "ord-type", ""); err != nil || got.State != PayPaid || got.Paid.Minor != -1 {
		t.Fatalf("other chain: %+v, %v", got, err)
	}
	for _, order := range []string{"ord-no-echo", "ord-null-echo", "ord-empty-echo", "ord-blank-echo"} {
		if got, err := c.Query(t.Context(), order, ""); err == nil {
			t.Fatalf("%s: missing echo was accepted: %+v", order, got)
		}
	}
	if got, err := c.Query(t.Context(), "ord-missing", ""); err != nil || got.State != PayPending {
		t.Fatalf("missing order: %+v, %v", got, err)
	}
	for _, order := range []string{"ord-wrong-key", "ord-echo", "ord-status", "ord-500", "ord-json"} {
		if _, err := c.Query(t.Context(), order, ""); err == nil {
			t.Errorf("%s: query accepted", order)
		}
	}
	var rejected *Rejected
	if _, err := c.Query(t.Context(), "ord-wrong-key", ""); !errors.As(err, &rejected) {
		t.Fatal("wrong key was not a definite rejection", err)
	}
}

func ezPayCallback(v url.Values, key string) Callback {
	v.Set("sign", easyPaySign(v, key))
	v.Set("sign_type", "MD5")
	return Callback{Method: http.MethodGet, RawQuery: v.Encode()}
}

//nolint:cyclop // each callback variant and its amount is the test
func TestEzPayVerify(t *testing.T) {
	c := easyPayOpen(t, ezPayConfig(t, "https://pay.example.com"))
	base := func() url.Values {
		return url.Values{
			"pid": {"1001"}, "trade_no": {"T1"}, "out_trade_no": {"ord_demo"}, "type": {"usdt_trc20"},
			"name": {"Top-up & credits"}, "money": {"10.00"}, "currency": {"USD"},
			"pay_currency": {"USDT"}, "pay_amount": {"10.000001"}, "trade_status": {"TRADE_SUCCESS"},
		}
	}
	notice, err := c.Verify(t.Context(), ezPayCallback(base(), theKey))
	if err != nil || !notice.Relevant || !notice.Success || notice.Order != "ord_demo" || notice.TradeNo != "T1" ||
		notice.Paid != (Money{currency(t, "USD"), 1000}) {
		t.Fatalf("valid callback: %+v, %v", notice, err)
	}
	if _, err := c.Verify(t.Context(), ezPayCallback(base(), "wrong")); err == nil {
		t.Fatal("wrong signature accepted")
	}
	unknown := base()
	unknown.Set("unexpected", "x")
	if _, err := c.Verify(t.Context(), ezPayCallback(unknown, theKey)); err == nil {
		t.Fatal("unknown callback field accepted")
	}
	pid := base()
	pid.Set("pid", "1002")
	if _, err := c.Verify(t.Context(), ezPayCallback(pid, theKey)); err == nil {
		t.Fatal("another merchant's callback accepted")
	}
	cny := base()
	cny.Set("currency", "CNY")
	cny.Set("money", "70.00")
	notice, err = c.Verify(t.Context(), ezPayCallback(cny, theKey))
	if err != nil || notice.Paid != (Money{currency(t, "CNY"), 7000}) {
		t.Fatalf("CNY callback: %+v, %v", notice, err)
	}
	missing := base()
	missing.Del("currency")
	notice, err = c.Verify(t.Context(), ezPayCallback(missing, theKey))
	if err != nil || notice.Paid.Minor != -1 {
		t.Fatalf("missing currency: %+v, %v", notice, err)
	}
	otherType := base()
	otherType.Set("type", "usdt_erc20")
	notice, err = c.Verify(t.Context(), ezPayCallback(otherType, theKey))
	if err != nil || notice.Paid.Minor != -1 {
		t.Fatalf("other chain: %+v, %v", notice, err)
	}

	rotating := ezPayConfig(t, "https://pay.example.com")
	rotating.Secrets["old_key"] = secret.New("old-key")
	old := easyPayOpen(t, rotating)
	notice, err = old.Verify(t.Context(), ezPayCallback(base(), "old-key"))
	if err != nil || !notice.Success {
		t.Fatalf("old key: %+v, %v", notice, err)
	}
}
