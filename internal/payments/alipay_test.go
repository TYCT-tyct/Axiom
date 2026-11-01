package payments

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

func alipayNotification(t *testing.T, v url.Values, key *rsa.PrivateKey) url.Values {
	t.Helper()
	v.Set("sign", alipayTestSign([]byte(alipaySignText(v, false)), key))
	return v
}

func alipayCallback(v url.Values) Callback {
	return Callback{Method: http.MethodPost, Body: []byte(v.Encode()), Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=UTF-8"}}}
}

func TestAlipaySignatureText(t *testing.T) {
	v := url.Values{
		"b": {"two words"}, "a": {"1"}, "empty": {""}, "sign": {"ignored"},
		"sign_type": {"RSA2"}, "unicode": {"商品 A&B"},
	}
	if got, want := alipaySignText(v, false), "a=1&b=two words&unicode=商品 A&B"; got != want {
		t.Fatalf("notification text %q, want %q", got, want)
	}
	if got, want := alipaySignText(v, true), "a=1&b=two words&sign_type=RSA2&unicode=商品 A&B"; got != want {
		t.Fatalf("request text %q, want %q", got, want)
	}

	keys := alipayKeys(t)
	signature, err := alipaySign(v, keys.app)
	if err != nil {
		t.Fatal(err)
	}
	if !alipayVerify([]byte(alipaySignText(v, true)), signature, &keys.app.PublicKey) {
		t.Fatal("request signature does not verify")
	}
	if alipayVerify([]byte(alipaySignText(v, false)), signature, &keys.app.PublicKey) {
		t.Fatal("request signature verified as notification text")
	}
}

func TestAlipayNotification(t *testing.T) {
	keys := alipayKeys(t)
	v := url.Values{
		"app_id": {alipayTestAppID}, "seller_id": {alipayTestSellerID},
		"out_trade_no": {"ord_demo"}, "trade_no": {"2026110122000000000001"},
		"trade_status": {"TRADE_SUCCESS"}, "total_amount": {"100.00"},
		"notify_type": {"trade_status_sync"}, "notify_id": {"n1"}, "sign_type": {"RSA2"},
	}
	ch := alipayOpen(t, alipayTestConfig(t, ""))
	notice, err := ch.Verify(t.Context(), alipayCallback(alipayNotification(t, v, keys.alipay)))
	if err != nil {
		t.Fatal(err)
	}
	if !notice.Relevant || !notice.Success || notice.Order != "ord_demo" || notice.TradeNo != "2026110122000000000001" ||
		notice.Paid.Minor != 10_000 || notice.Paid.Currency.String() != "CNY" || notice.Raw != "alipay notification" {
		t.Fatalf("%+v", notice)
	}
}

//nolint:cyclop,gocognit // one rejected shape per row; the table is the test
func TestAlipayNotificationRejectsTamperingAndForeignMerchants(t *testing.T) {
	keys := alipayKeys(t)
	base := func() url.Values {
		return url.Values{
			"app_id": {alipayTestAppID}, "seller_id": {alipayTestSellerID},
			"out_trade_no": {"ord_demo"}, "trade_no": {"T1"}, "trade_status": {"TRADE_SUCCESS"},
			"total_amount": {"100.00"}, "notify_type": {"trade_status_sync"}, "sign_type": {"RSA2"},
		}
	}
	ch := alipayOpen(t, alipayTestConfig(t, ""))
	t.Run("tampered amount", func(t *testing.T) {
		v := alipayNotification(t, base(), keys.alipay)
		v.Set("total_amount", "1.00")
		if _, err := ch.Verify(t.Context(), alipayCallback(v)); err == nil {
			t.Fatal("tampered notification accepted")
		}
	})
	t.Run("foreign app", func(t *testing.T) {
		v := base()
		v.Set("app_id", "2021000000000099")
		if _, err := ch.Verify(t.Context(), alipayCallback(alipayNotification(t, v, keys.alipay))); err == nil {
			t.Fatal("foreign app accepted")
		}
	})
	t.Run("foreign seller", func(t *testing.T) {
		v := base()
		v.Set("seller_id", "2088000000000099")
		if _, err := ch.Verify(t.Context(), alipayCallback(alipayNotification(t, v, keys.alipay))); err == nil {
			t.Fatal("foreign seller accepted")
		}
	})
	t.Run("unsupported sign type", func(t *testing.T) {
		v := alipayNotification(t, base(), keys.alipay)
		v.Set("sign_type", "RSA")
		if _, err := ch.Verify(t.Context(), alipayCallback(v)); err == nil {
			t.Fatal("RSA accepted")
		}
	})
	t.Run("missing signature", func(t *testing.T) {
		v := base()
		v.Set("sign_type", "RSA2")
		if _, err := ch.Verify(t.Context(), alipayCallback(v)); err == nil {
			t.Fatal("unsigned notification accepted")
		}
	})
	t.Run("wrong signer", func(t *testing.T) {
		if _, err := ch.Verify(t.Context(), alipayCallback(alipayNotification(t, base(), keys.other))); err == nil {
			t.Fatal("foreign signer accepted")
		}
	})
	t.Run("duplicate parameter", func(t *testing.T) {
		v := alipayNotification(t, base(), keys.alipay)
		cb := alipayCallback(v)
		cb.Body = append(cb.Body, []byte("&out_trade_no=other")...)
		if _, err := ch.Verify(t.Context(), cb); err == nil {
			t.Fatal("duplicate parameter accepted")
		}
	})
	t.Run("GET", func(t *testing.T) {
		v := alipayNotification(t, base(), keys.alipay)
		cb := alipayCallback(v)
		cb.Method, cb.RawQuery, cb.Body = http.MethodGet, v.Encode(), nil
		if _, err := ch.Verify(t.Context(), cb); err == nil {
			t.Fatal("GET accepted")
		}
	})
	t.Run("JSON body", func(t *testing.T) {
		v := alipayNotification(t, base(), keys.alipay)
		cb := alipayCallback(v)
		cb.Header.Set("Content-Type", "application/json")
		if _, err := ch.Verify(t.Context(), cb); err == nil {
			t.Fatal("JSON accepted")
		}
	})
	t.Run("other notify type", func(t *testing.T) {
		v := base()
		v.Set("notify_type", "refund")
		notice, err := ch.Verify(t.Context(), alipayCallback(alipayNotification(t, v, keys.alipay)))
		if err != nil || notice.Relevant {
			t.Fatalf("%+v %v", notice, err)
		}
	})
	t.Run("closed trade", func(t *testing.T) {
		v := base()
		v.Set("trade_status", "TRADE_CLOSED")
		notice, err := ch.Verify(t.Context(), alipayCallback(alipayNotification(t, v, keys.alipay)))
		if err != nil || !notice.Relevant || notice.Success {
			t.Fatalf("%+v %v", notice, err)
		}
	})
}

func TestAlipayNotificationAmountStates(t *testing.T) {
	keys := alipayKeys(t)
	ch := alipayOpen(t, alipayTestConfig(t, ""))
	for _, tc := range []struct {
		amount string
		want   int64
	}{
		{"", 0},
		{"100", 10_000},
		{"100.0", 10_000},
		{"100.00", 10_000},
		{"0.01", 1},
		{"1.001", -1},
		{"-1.00", -1},
		{"1,000.00", -1},
		{"1e3", -1},
		{"1.", -1},
	} {
		t.Run(tc.amount, func(t *testing.T) {
			v := url.Values{
				"app_id": {alipayTestAppID}, "seller_id": {alipayTestSellerID},
				"out_trade_no": {"ord_demo"}, "trade_no": {"T1"},
				"trade_status": {"TRADE_SUCCESS"}, "notify_type": {"trade_status_sync"}, "sign_type": {"RSA2"},
			}
			if tc.amount != "" {
				v.Set("total_amount", tc.amount)
			}
			notice, err := ch.Verify(t.Context(), alipayCallback(alipayNotification(t, v, keys.alipay)))
			if err != nil {
				t.Fatal(err)
			}
			if notice.Paid.Minor != tc.want {
				t.Fatalf("amount %q -> %d, want %d", tc.amount, notice.Paid.Minor, tc.want)
			}
		})
	}
}

func TestAlipayNotificationOldPublicKey(t *testing.T) {
	keys := alipayKeys(t)
	cfg := alipayTestConfig(t, "")
	cfg.Secrets["old_public_key"] = secret.New(alipayPublicPEM(&keys.old.PublicKey))
	ch := alipayOpen(t, cfg)
	v := url.Values{
		"app_id": {alipayTestAppID}, "seller_id": {alipayTestSellerID},
		"out_trade_no": {"ord_demo"}, "trade_no": {"T1"},
		"trade_status": {"TRADE_SUCCESS"}, "total_amount": {"1.00"}, "sign_type": {"RSA2"},
	}
	if _, err := ch.Verify(t.Context(), alipayCallback(alipayNotification(t, v, keys.old))); err != nil {
		t.Fatalf("old key rejected: %v", err)
	}
	if _, err := ch.Verify(t.Context(), alipayCallback(alipayNotification(t, v, keys.other))); err == nil {
		t.Fatal("unknown key accepted")
	}
}

//nolint:cyclop // one configuration concern per row; the table is the test
func TestAlipayConfiguration(t *testing.T) {
	cfg := alipayTestConfig(t, "")
	ch := alipayOpen(t, cfg)
	if ch.Caps() != CapQuery|CapClose|CapRefund|CapQueryRefund|CapRefundKey {
		t.Fatal(ch.Caps())
	}
	if ch.Methods() != nil {
		t.Fatal("Alipay methods come from merchant configuration")
	}
	for _, method := range []string{alipayPagePay, alipayWapPay, alipayPrecreate} {
		in := MerchantInput{Methods: []MethodInput{{Method: "alipay", Upstream: method}}}
		if err := ch.(*alipayChannel).validateMerchant(in); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	if err := ch.(*alipayChannel).validateMerchant(MerchantInput{Methods: []MethodInput{{Method: "alipay", Upstream: "other"}}}); !isField(err, "/methods/0/upstream") {
		t.Fatalf("unknown product: %v", err)
	}

	normalized, err := (Alipay{}).normalizeSettings([]byte(`{"app_id":" 2021000000000001 ","seller_id":" 2088000000000001 ","gateway":"https://openapi.alipay.com/gateway.do/"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(normalized) != `{"app_id":"2021000000000001","gateway":"https://openapi.alipay.com/gateway.do","seller_id":"2088000000000001"}` {
		t.Fatalf("%s", normalized)
	}
	identity, err := (Alipay{}).identitySettings([]byte(`{"app_id":"2021000000000001","gateway":""}`))
	if err != nil {
		t.Fatal(err)
	}
	other, err := (Alipay{}).identitySettings([]byte(`{"app_id":"2021000000000001","gateway":"https://openapi.alipay.com/gateway.do"}`))
	if err != nil || string(identity) != string(other) {
		t.Fatalf("default gateway changed identity: %s %s %v", identity, other, err)
	}
	third, err := (Alipay{}).identitySettings([]byte(`{"app_id":"2021000000000002","gateway":""}`))
	if err != nil || string(identity) == string(third) {
		t.Fatal("app_id is not part of identity")
	}
}

func TestAlipayConfigurationRejectsInvalidValues(t *testing.T) {
	keys := alipayKeys(t)
	small, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // the short key is the input this test rejects
	if err != nil {
		t.Fatal(err)
	}
	base := alipayTestConfig(t, "")
	with := func(edit func(*MerchantConfig)) MerchantConfig {
		cfg := base
		cfg.Secrets = map[string]secret.Secret{}
		maps.Copy(cfg.Secrets, base.Secrets)
		edit(&cfg)
		return cfg
	}
	for name, cfg := range map[string]MerchantConfig{
		"no app":    with(func(c *MerchantConfig) { c.Settings = []byte(`{"app_id":""}`) }),
		"text app":  with(func(c *MerchantConfig) { c.Settings = []byte(`{"app_id":"abc123"}`) }),
		"short app": with(func(c *MerchantConfig) { c.Settings = []byte(`{"app_id":"1"}`) }),
		"unknown":   with(func(c *MerchantConfig) { c.Settings = []byte(`{"app_id":"2021000000000001","extra":1}`) }),
		"plaintext": with(func(c *MerchantConfig) {
			c.Settings = []byte(`{"app_id":"2021000000000001","gateway":"http://openapi.alipay.com/gateway.do"}`)
		}),
		"userinfo": with(func(c *MerchantConfig) {
			c.Settings = []byte(`{"app_id":"2021000000000001","gateway":"https://user@openapi.alipay.com/gateway.do"}`)
		}),
		"query": with(func(c *MerchantConfig) {
			c.Settings = []byte(`{"app_id":"2021000000000001","gateway":"https://openapi.alipay.com/gateway.do?x=1"}`)
		}),
		"fragment": with(func(c *MerchantConfig) {
			c.Settings = []byte(`{"app_id":"2021000000000001","gateway":"https://openapi.alipay.com/gateway.do#x"}`)
		}),
		"usd":         with(func(c *MerchantConfig) { c.Currency = currency(t, "USD") }),
		"no client":   with(func(c *MerchantConfig) { c.Client = nil }),
		"no private":  with(func(c *MerchantConfig) { delete(c.Secrets, "private_key") }),
		"no public":   with(func(c *MerchantConfig) { delete(c.Secrets, "public_key") }),
		"unknown key": with(func(c *MerchantConfig) { c.Secrets["session_key"] = secret.New("x") }),
		"small":       with(func(c *MerchantConfig) { c.Secrets["private_key"] = secret.New(alipayPrivatePEM(small)) }),
		"bad private": with(func(c *MerchantConfig) {
			c.Secrets["private_key"] = secret.New("-----BEGIN PRIVATE KEY-----\nnot base64\n-----END PRIVATE KEY-----")
		}),
		"bad public": with(func(c *MerchantConfig) { c.Secrets["public_key"] = secret.New("not a key") }),
		"bad old":    with(func(c *MerchantConfig) { c.Secrets["old_public_key"] = secret.New("not a key") }),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (Alipay{}).Open(cfg); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}

	t.Run("bare public key", func(t *testing.T) {
		cfg := with(func(c *MerchantConfig) {
			c.Secrets["public_key"] = secret.New(alipayBarePublic(&keys.alipay.PublicKey))
		})
		alipayOpen(t, cfg)
	})
	t.Run("pkcs1 private key", func(t *testing.T) {
		cfg := with(func(c *MerchantConfig) {
			c.Secrets["private_key"] = secret.New(string(pemPrivatePKCS1(keys.app)))
		})
		alipayOpen(t, cfg)
	})
}

func pemPrivatePKCS1(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

//nolint:cyclop,gocognit // one product and one rejected shape per row; the table is the test
func TestAlipayCreate(t *testing.T) {
	keys := alipayKeys(t)
	ch := alipayOpen(t, alipayTestConfig(t, ""))
	for _, tc := range []struct {
		upstream, method string
		mobile           bool
	}{
		{alipayPagePay, "alipay.trade.page.pay", true},
		{alipayWapPay, "alipay.trade.wap.pay", false},
	} {
		t.Run(tc.upstream, func(t *testing.T) {
			r := alipayRequest(t)
			r.Upstream, r.Mobile = tc.upstream, tc.mobile
			checkout, err := ch.Create(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(checkout.PayURL)
			if err != nil || u.Scheme != "https" || u.Host != "openapi.alipay.com" || u.Path != "/gateway.do" {
				t.Fatalf("%s %v", checkout.PayURL, err)
			}
			v := u.Query()
			if !alipayVerify([]byte(alipaySignText(v, true)), v.Get("sign"), &keys.app.PublicKey) {
				t.Fatal("checkout signature does not verify")
			}
			if v.Get("method") != tc.method || v.Get("sign_type") != "RSA2" || v.Get("app_id") != alipayTestAppID ||
				v.Get("notify_url") != r.NotifyURL || v.Get("return_url") != r.ReturnURL || v.Get("charset") != "utf-8" {
				t.Fatalf("%v", v)
			}
			if _, err := time.Parse("2006-01-02 15:04:05", v.Get("timestamp")); err != nil {
				t.Fatal(err)
			}
			var biz map[string]string
			if err := jsonx.Unmarshal([]byte(v.Get("biz_content")), &biz); err != nil {
				t.Fatal(err)
			}
			if biz["out_trade_no"] != r.Order.TypeID() || biz["total_amount"] != "123.45" || biz["product_code"] != tc.upstream ||
				biz["subject"] != r.Subject {
				t.Fatalf("%v", biz)
			}
		})
	}
}

func TestAlipayCreatePrecreate(t *testing.T) {
	r := alipayRequest(t)
	r.Upstream = alipayPrecreate
	gateway := &alipayTestGateway{t: t, respond: func(method string, biz map[string]jsonx.Raw, _ url.Values) (string, bool) {
		if method != "alipay.trade.precreate" {
			t.Errorf("method %s", method)
		}
		if alipayBiz(t, biz, "out_trade_no") != r.Order.TypeID() || alipayBiz(t, biz, "product_code") != alipayPrecreate {
			t.Errorf("biz %v", biz)
		}
		return fmt.Sprintf(`{"code":"10000","msg":"Success","qr_code":"https://qr.alipay.com/abc","out_trade_no":%q}`, r.Order.TypeID()), true
	}}
	server := httptest.NewTLSServer(gateway)
	t.Cleanup(server.Close)
	ch := alipayOpen(t, alipayTestConfig(t, server.URL, server.Certificate()))
	checkout, err := ch.Create(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.QRCode != "https://qr.alipay.com/abc" || checkout.PayURL != "" {
		t.Fatalf("%+v", checkout)
	}
}

func TestAlipayCreateRefusesUnsafeRequests(t *testing.T) {
	ch := alipayOpen(t, alipayTestConfig(t, ""))
	for name, edit := range map[string]func(*CreateRequest){
		"zero amount":    func(r *CreateRequest) { r.Total.Minor = 0 },
		"foreign money":  func(r *CreateRequest) { r.Total.Currency = currency(t, "USD") },
		"plain return":   func(r *CreateRequest) { r.ReturnURL = "http://console.example.com/topup" },
		"plain notify":   func(r *CreateRequest) { r.NotifyURL = "http://callbacks.example.com/x" },
		"unknown method": func(r *CreateRequest) { r.Upstream = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			r := alipayRequest(t)
			edit(&r)
			_, err := ch.Create(t.Context(), r)
			if rejected, ok := errors.AsType[*Rejected](err); !ok || rejected.Code == "" {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestAlipayCreateRejectsBadPrecreateAnswers(t *testing.T) {
	r := alipayRequest(t)
	r.Upstream = alipayPrecreate
	for name, answer := range map[string]struct {
		node string
		sign bool
	}{
		"unsigned":  {`{"code":"10000","qr_code":"https://qr.alipay.com/abc","out_trade_no":"` + r.Order.TypeID() + `"}`, false},
		"not https": {`{"code":"10000","qr_code":"http://qr.alipay.com/abc","out_trade_no":"` + r.Order.TypeID() + `"}`, true},
		"other id":  {`{"code":"10000","qr_code":"https://qr.alipay.com/abc","out_trade_no":"ord_other"}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			gateway := &alipayTestGateway{t: t, respond: func(string, map[string]jsonx.Raw, url.Values) (string, bool) {
				return answer.node, answer.sign
			}}
			server := httptest.NewTLSServer(gateway)
			t.Cleanup(server.Close)
			ch := alipayOpen(t, alipayTestConfig(t, server.URL, server.Certificate()))
			if _, err := ch.Create(t.Context(), r); err == nil {
				t.Fatal("bad precreate answer accepted")
			}
		})
	}
}
