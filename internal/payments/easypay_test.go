package payments

import (
	"crypto/x509"
	"errors"
	"net/http"
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

func easyPayConfig(t *testing.T, gateway string, certificates ...*x509.Certificate) MerchantConfig {
	t.Helper()
	s := easyPaySettings{Gateway: gateway, PID: "1001", Mode: "api", CIDs: map[Method]string{"alipay": "2"}, Query: true, Close: true, Refund: true}
	raw, err := jsonx.MarshalCanonical(s)
	if err != nil {
		t.Fatal(err)
	}
	client := egress.NewForTesting(egresstest.Loopback(t, certificates...))
	t.Cleanup(client.CloseIdleConnections)
	return MerchantConfig{ID: MerchantID(uuid.NewV7()), Currency: currency(t, "CNY"), Settings: raw, Secrets: map[string]secret.Secret{"key": secret.New(theKey)}, Client: client}
}

func easyPayOpen(t *testing.T, cfg MerchantConfig) Channel {
	t.Helper()
	c, err := (EasyPay{}).Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func easyPayRequest(t *testing.T) CreateRequest {
	return CreateRequest{Order: OrderID(uuid.NewV7()), Method: "alipay", Upstream: "ali", NotifyURL: "https://callbacks.example.com/generated", Total: Money{currency(t, "CNY"), 12345}, Subject: "Top-up & credits", ReturnURL: "https://console.example.com/topup?order=test", ClientIP: netip.MustParseAddr("203.0.113.7"), Mobile: true}
}

func TestEasyPaySignatureVectors(t *testing.T) {
	const (
		unicodeDigest = "d4a86fb2883912436806439d6d6607e8"
		emptyDigest   = "f57f90c282c011b2f6999bd5d5955327"
		zeroDigest    = "4b29011054127ad4924f759cee1150fa"
	)
	// Expected digests are fixed from these literal protocol strings, independently of the implementation:
	// a=1&b=two wordskey; money=100.00&name=商品 A&B=x&out_trade_no=ord_demo&pid=1001&trade_status=TRADE_SUCCESStest-key;
	// empty-key; a=0&z=中文test-key. Values are decoded once and never URL-escaped in the signature base.
	for _, tc := range []struct {
		v         url.Values
		key, want string
	}{
		{url.Values{"b": {"two words"}, "a": {"1"}, "empty": {""}, "sign": {"ignored"}, "sign_type": {"MD5"}}, "key", "e858aafe1d5233800a8d44afa9185a58"},
		{url.Values{"money": {"100.00"}, "name": {"商品 A&B=x"}, "out_trade_no": {"ord_demo"}, "pid": {"1001"}, "trade_status": {"TRADE_SUCCESS"}}, "test-key", unicodeDigest},
		{url.Values{}, "empty-key", emptyDigest},
		{url.Values{"z": {"中文"}, "a": {"0"}}, "test-key", zeroDigest},
	} {
		if got := easyPaySign(tc.v, tc.key); got != tc.want {
			t.Errorf("signature = %q, want %q", got, tc.want)
		}
	}
}

func TestEasyPayConfiguration(t *testing.T) {
	cfg := easyPayConfig(t, "https://pay.example.com/prefix/MAPI.php/?key=discard#fragment")
	c := easyPayOpen(t, cfg)
	if (EasyPay{}).Kind() != "easypay" || c.(*easyPayChannel).settings.Gateway != "https://pay.example.com/prefix" || c.Caps() != CapQuery|CapClose|CapRefund || c.Methods() != nil {
		t.Fatalf("binding: %+v", c)
	}
	for name, edit := range map[string]func(*MerchantConfig, *easyPaySettings){
		"unknown field":       func(c *MerchantConfig, _ *easyPaySettings) { c.Settings = []byte(`{"surprise":true}`) },
		"duplicate field":     func(c *MerchantConfig, _ *easyPaySettings) { c.Settings = []byte(`{"pid":"1","pid":"2"}`) },
		"gateway credentials": func(_ *MerchantConfig, s *easyPaySettings) { s.Gateway = "https://user:pass@pay.example.com" },
		"gateway scheme":      func(_ *MerchantConfig, s *easyPaySettings) { s.Gateway = "javascript:alert(1)" },
		"pid":                 func(_ *MerchantConfig, s *easyPaySettings) { s.PID = "1&x=2" },
		"mode":                func(_ *MerchantConfig, s *easyPaySettings) { s.Mode = "popup" },
		"signature":           func(_ *MerchantConfig, s *easyPaySettings) { s.SignType = "SHA256" },
		"http gateway":        func(_ *MerchantConfig, s *easyPaySettings) { s.Gateway = "http://pay.example.com" },
		"cid":                 func(_ *MerchantConfig, s *easyPaySettings) { s.CIDs["alipay"] = "1=x" },
		"unknown secret":      func(c *MerchantConfig, _ *easyPaySettings) { c.Secrets["oldkey"] = secret.New("previous-test-key") },
		"hyphenated secret":   func(c *MerchantConfig, _ *easyPaySettings) { c.Secrets["old-key"] = secret.New("previous-test-key") },
		"key":                 func(c *MerchantConfig, _ *easyPaySettings) { c.Secrets = nil },
		"currency":            func(c *MerchantConfig, _ *easyPaySettings) { c.Currency = Money{}.Currency },
		"client":              func(c *MerchantConfig, _ *easyPaySettings) { c.Client = nil },
	} {
		t.Run(name, func(t *testing.T) {
			in := cfg
			in.Secrets = map[string]secret.Secret{"key": secret.New(theKey)}
			var s easyPaySettings
			if err := jsonx.UnmarshalKnown(cfg.Settings, &s); err != nil {
				t.Fatal(err)
			}
			edit(&in, &s)
			if string(in.Settings) == string(cfg.Settings) {
				in.Settings = easyPayJSON(t, s)
			}
			if _, err := (EasyPay{}).Open(in); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}

func TestEasyPayExactAmounts(t *testing.T) {
	for _, tc := range []struct {
		text, code string
		want       int64
	}{
		{"100.0000", "CNY", 10000},
		{"0.01", "CNY", 1},
		{"0001.2000", "CNY", 120},
		{"1.001", "CNY", -1},
		{"92233720368547758.07", "CNY", 9223372036854775807},
		{"92233720368547758.08", "CNY", -1},
		{"999999999999999999999999", "CNY", -1},
		{"0", "JPY", -1},
		{"100.000", "JPY", 100},
		{"1.1", "JPY", -1},
		{"1.0010", "KWD", 1001},
		{"1.0001", "KWD", -1},
		{"", "CNY", 0},
		{"0.00", "CNY", -1},
		{"0.0000", "CNY", -1},
		{"1,000.00", "CNY", -1},
		{"1e3", "CNY", -1},
		{"NaN", "CNY", -1},
		{"-1", "CNY", -1},
		{"+1", "CNY", -1},
		{"1/2", "CNY", -1},
		{".5", "CNY", -1},
		{"1.", "CNY", -1},
		{"1.2.3", "CNY", -1},
		{"1 0", "CNY", -1},
	} {
		t.Run(tc.code+"/"+tc.text, func(t *testing.T) {
			cur := currency(t, tc.code)
			got := easyPayMinor(tc.text, digits(cur))
			if got != tc.want {
				t.Fatalf("amount %q = %d, want %d", tc.text, got, tc.want)
			}
			if got > 0 {
				if round := easyPayMinor(easyPayFormat(Money{cur, got}), digits(cur)); round != got {
					t.Fatal("round-trip", round)
				}
			}
		})
	}
}

func easyPayNoticeValues() url.Values {
	return url.Values{"pid": {"1001"}, "trade_no": {"T1"}, "out_trade_no": {"ord_demo"}, "type": {"alipay"}, "name": {"商品 & text=value"}, "money": {"100.0000"}, "trade_status": {"TRADE_SUCCESS"}, "param": {"token"}, "sign_type": {"MD5"}}
}

func easyPayCallback(method string, v url.Values) Callback {
	cb := Callback{Method: method, Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}}}
	if method == http.MethodPost {
		cb.Body = []byte(v.Encode())
	} else {
		cb.RawQuery = v.Encode()
	}
	return cb
}

func TestEasyPayCallbackSecurity(t *testing.T) {
	c := easyPayOpen(t, easyPayConfig(t, "https://pay.example.com"))
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		v := easyPayNoticeValues()
		v.Set("sign", epaySignature(v, theKey))
		n, err := c.Verify(t.Context(), easyPayCallback(method, v))
		want := Notice{Relevant: true, Success: true, Paid: Money{currency(t, "CNY"), 10000}, Order: "ord_demo", TradeNo: "T1", Raw: "easypay notification"}
		if err != nil || want != n {
			t.Fatalf("notice %+v %v", n, err)
		}
	}
	for name, mutate := range map[string]func(url.Values){
		"signature":       func(v url.Values) { v.Set("sign", strings.Repeat("0", 32)) },
		"unsigned change": func(v url.Values) { v.Set("money", "101.00") },
		"missing sign":    func(v url.Values) { v.Del("sign") },
		"merchant":        func(v url.Values) { v.Set("pid", "1002"); v.Set("sign", epaySignature(v, theKey)) },
		"extra":           func(v url.Values) { v.Set("extra", "x"); v.Set("sign", epaySignature(v, theKey)) },
		"creation smuggling": func(v url.Values) {
			v.Set("return_url", "https://console.example.com/?trade_status=TRADE_SUCCESS")
			v.Set("sign", epaySignature(v, theKey))
		},
		"token smuggling": func(v url.Values) {
			v.Set("param", "x&trade_status=TRADE_SUCCESS")
			v.Set("sign", epaySignature(v, theKey))
		},
		"duplicate":       func(v url.Values) { v.Add("money", "100.00") },
		"wrong sign type": func(v url.Values) { v.Set("sign_type", "SHA256") },
	} {
		t.Run(name, func(t *testing.T) {
			v := easyPayNoticeValues()
			v.Set("sign", epaySignature(v, theKey))
			mutate(v)
			if _, err := c.Verify(t.Context(), easyPayCallback(http.MethodGet, v)); err == nil {
				t.Fatal("accepted unsafe callback")
			}
		})
	}
	// A signed order's unescaped value can produce exactly the forged callback's signature base.
	creation := url.Values{"money": {"100.00"}, "name": {"topup"}, "out_trade_no": {"ord_demo"}, "pid": {"1001"}, "return_url": {"https://console.example.com/&trade_status=TRADE_SUCCESS"}, "type": {"alipay"}}
	forged := url.Values{"money": {"100.00"}, "name": {"topup"}, "out_trade_no": {"ord_demo"}, "pid": {"1001"}, "return_url": {"https://console.example.com/"}, "trade_status": {"TRADE_SUCCESS"}, "type": {"alipay"}}
	forged.Set("sign", epaySignature(creation, theKey))
	if epaySignature(forged, theKey) != forged.Get("sign") {
		t.Fatal("smuggling fixture does not collide")
	}
	if _, err := c.Verify(t.Context(), easyPayCallback(http.MethodGet, forged)); err == nil {
		t.Fatal("accepted replayed creation signature")
	}
}

func TestEasyPayCallbackFramingAndRotation(t *testing.T) {
	cfg := easyPayConfig(t, "https://pay.example.com")
	cfg.Secrets["old_key"] = secret.New("previous-test-key")
	c := easyPayOpen(t, cfg)
	v := easyPayNoticeValues()
	v.Set("sign", epaySignature(v, "previous-test-key"))
	if _, err := c.Verify(t.Context(), easyPayCallback(http.MethodGet, v)); err != nil {
		t.Fatal(err)
	}
	for _, cb := range []Callback{
		{Method: http.MethodPut},
		{Method: http.MethodGet, RawQuery: v.Encode(), Body: []byte("x")},
		{Method: http.MethodPost, RawQuery: v.Encode(), Body: []byte(v.Encode()), Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}},
		{Method: http.MethodPost, Body: []byte(v.Encode()), Header: http.Header{"Content-Type": {"application/json"}}},
		{Method: http.MethodGet, RawQuery: "money=%zz"},
		{Method: http.MethodGet, RawQuery: strings.Repeat("x", callbackMax+1)},
	} {
		if _, err := c.Verify(t.Context(), cb); err == nil {
			t.Fatal("accepted invalid framing")
		}
	}
	v.Set("trade_status", "WAIT_BUYER_PAY")
	v.Set("sign", epaySignature(v, theKey))
	if n, err := c.Verify(t.Context(), easyPayCallback(http.MethodGet, v)); err != nil || n.Success {
		t.Fatal(n, err)
	}
	for _, tc := range []struct {
		ok     bool
		status int
		body   string
	}{{true, http.StatusOK, "success"}, {false, http.StatusInternalServerError, "fail"}} {
		status, body := c.Ack(tc.ok)
		if status != tc.status || string(body) != tc.body {
			t.Fatal(status, string(body))
		}
	}
}

func TestEasyPayUnsupportedCapabilities(t *testing.T) {
	cfg := easyPayConfig(t, "https://pay.example.com")
	var s easyPaySettings
	if err := jsonx.UnmarshalKnown(cfg.Settings, &s); err != nil {
		t.Fatal(err)
	}
	s.Query, s.Close, s.Refund = false, false, false
	var err error
	cfg.Settings, err = jsonx.MarshalCanonical(s)
	if err != nil {
		t.Fatal(err)
	}
	c := easyPayOpen(t, cfg)
	if c.Caps() != 0 {
		t.Fatal(c.Caps())
	}
	for _, call := range []func(){func() { _, err := c.Query(t.Context(), "order", ""); t.Fatal("query returned", err) }, func() { err := c.Close(t.Context(), OrderID{}, ""); t.Fatal("close returned", err) }, func() { _, err := c.Refund(t.Context(), RefundCall{}); t.Fatal("refund returned", err) }, func() { _, err := c.QueryRefund(t.Context(), RefundCall{}); t.Fatal("query refund returned", err) }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("undeclared capability did not panic")
				}
			}()
			call()
		}()
	}
	r := easyPayRequest(t)
	r.Total.Minor = -1
	_, err = c.Create(t.Context(), r)
	if rejected, ok := errors.AsType[*Rejected](err); !ok || rejected.Code != "invalid_order" {
		t.Fatal("invalid order was not refused", err)
	}
}

func easyPayJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := jsonx.MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
