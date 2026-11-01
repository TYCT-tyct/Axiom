package payments

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"maps"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

func TestWechatPaySettingsAndKeys(t *testing.T) {
	server := wpTestServer(t, http.NotFoundHandler())
	valid := wpCertConfig(t, server.URL, server.Certificate(), false)
	channel := wpOpen(t, valid)
	if channel.Caps() != CapQuery|CapClose|CapRefund|CapQueryRefund|CapRefundKey {
		t.Fatalf("caps %v", channel.Caps())
	}
	if channel.Methods() != nil {
		t.Fatal("WeChat Pay lets the site name its methods")
	}
	public := wpPublicConfig(t, server.URL, server.Certificate(), true)
	wpOpen(t, public)

	// PKCS#1 private keys and bare base64 public keys are what WeChat's console also hands out.
	keys := wpKeys(t)
	pkcs1 := wpTestPEM(t, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(keys.merchant))
	bare := base64.StdEncoding.EncodeToString(wpTestPublicDER(t, &keys.platform.PublicKey))
	form := valid
	form.Secrets = maps.Clone(valid.Secrets)
	form.Secrets["private_key"] = secret.New(pkcs1)
	form.Secrets["platform_certificate"] = secret.New(wpCertificatePEM(keys.platformCert))
	form.Secrets["platform_public_key"] = secret.New(bare)
	form.Settings = wpTestSettingsWith(t, valid.Settings, func(s *wechatSettings) { s.PlatformKeyID = wpTestKeyID })
	wpOpen(t, form)

	clone := func(c MerchantConfig) MerchantConfig {
		c.Secrets = maps.Clone(c.Secrets)
		c.Settings = slices.Clone(c.Settings)
		return c
	}
	cases := []struct {
		name   string
		change func(*MerchantConfig)
	}{
		{"missing private key", func(c *MerchantConfig) { c.Secrets["private_key"] = secret.New("") }},
		{"bad private key", func(c *MerchantConfig) { c.Secrets["private_key"] = secret.New("not a key") }},
		{"short api v3 key", func(c *MerchantConfig) { c.Secrets["api_v3_key"] = secret.New(strings.Repeat("x", 31)) }},
		{"unknown secret", func(c *MerchantConfig) { c.Secrets["key"] = secret.New("x") }},
		{"bad platform certificate", func(c *MerchantConfig) { c.Secrets["platform_certificate"] = secret.New("not a certificate") }},
		{"no verification key", func(c *MerchantConfig) { delete(c.Secrets, "platform_certificate") }},
		{"public key without id", func(c *MerchantConfig) {
			c.Secrets["platform_public_key"] = secret.New(wpPublicPEM(&keys.platform.PublicKey))
			c.Settings = wpTestSettingsWith(t, c.Settings, func(s *wechatSettings) { s.PlatformKeyID = "" })
		}},
		{"public key id without key", func(c *MerchantConfig) {
			c.Settings = wpTestSettingsWith(t, c.Settings, func(s *wechatSettings) { s.PlatformKeyID = wpTestKeyID })
		}},
		{"old key id without key", func(c *MerchantConfig) {
			c.Settings = wpTestSettingsWith(t, c.Settings, func(s *wechatSettings) { s.OldPlatformKeyID = wpTestOldKey })
		}},
		{"old public key without id", func(c *MerchantConfig) {
			c.Secrets["old_platform_public_key"] = secret.New(wpPublicPEM(&keys.old.PublicKey))
		}},
		{"duplicate certificate serial", func(c *MerchantConfig) {
			c.Secrets["old_platform_certificate"] = secret.New(wpCertificatePEM(keys.platformCert))
		}},
		{"not CNY", func(c *MerchantConfig) { c.Currency = currency(t, "USD") }},
		{"bad app id", func(c *MerchantConfig) {
			c.Settings = wpTestSettingsWith(t, c.Settings, func(s *wechatSettings) { s.AppID = "bad-id" })
		}},
		{"bad merchant id", func(c *MerchantConfig) {
			c.Settings = wpTestSettingsWith(t, c.Settings, func(s *wechatSettings) { s.MchID = "merchant" })
		}},
		{"bad serial", func(c *MerchantConfig) {
			c.Settings = wpTestSettingsWith(t, c.Settings, func(s *wechatSettings) { s.SerialNo = "1234" })
		}},
		{"http gateway", func(c *MerchantConfig) {
			c.Settings = wpTestSettingsWith(t, c.Settings, func(s *wechatSettings) { s.Gateway = "http://api.mch.weixin.qq.com" })
		}},
		{"gateway with path", func(c *MerchantConfig) {
			c.Settings = wpTestSettingsWith(t, c.Settings, func(s *wechatSettings) { s.Gateway = "https://api.mch.weixin.qq.com/v3" })
		}},
		{"gateway with query", func(c *MerchantConfig) {
			c.Settings = wpTestSettingsWith(t, c.Settings, func(s *wechatSettings) { s.Gateway = "https://api.mch.weixin.qq.com/?v=1" })
		}},
		{"gateway with empty query", func(c *MerchantConfig) {
			c.Settings = wpTestSettingsWith(t, c.Settings, func(s *wechatSettings) { s.Gateway = "https://api.mch.weixin.qq.com/?" })
		}},
		{"unknown settings member", func(c *MerchantConfig) {
			var raw map[string]any
			if err := jsonx.Unmarshal(c.Settings, &raw); err != nil {
				t.Fatal(err)
			}
			raw["extra"] = true
			c.Settings = wpTestJSON(t, raw)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := clone(valid)
			tc.change(&cfg)
			if _, err := (WechatPay{}).Open(cfg); err == nil {
				t.Fatal("accepted an invalid configuration")
			}
		})
	}
}

func TestWechatPayNormalizesSettings(t *testing.T) {
	raw := []byte(`{"app_id":" wx1234567890abcdef ","mch_id":" 1900000001 ","serial_no":" 408b07e79b8269fec3d5d3e6ab8ed163a6a380db ","platform_public_key_id":" pub_key_id_0123456789abcdef0123456789abcdef ","gateway":"https://API.MCH.WEIXIN.QQ.COM/"}`)
	normalized, err := (WechatPay{}).normalizeSettings(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"app_id":"wx1234567890abcdef","gateway":"https://api.mch.weixin.qq.com","mch_id":"1900000001","platform_public_key_id":"PUB_KEY_ID_0123456789ABCDEF0123456789ABCDEF","serial_no":"408B07E79B8269FEC3D5D3E6AB8ED163A6A380DB"}`
	if string(normalized) != want {
		t.Fatalf("normalized %s, want %s", normalized, want)
	}
	identity, err := (WechatPay{}).identitySettings(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(identity, []byte(`"app_id":"wx1234567890abcdef"`)) || !bytes.Contains(identity, []byte(`"gateway":"https://api.mch.weixin.qq.com"`)) {
		t.Fatalf("identity %s", identity)
	}
	changed := wpTestSettingsWith(t, normalized, func(s *wechatSettings) { s.Gateway = "https://api2.mch.weixin.qq.com" })
	before, err := (WechatPay{}).identitySettings(normalized)
	if err != nil {
		t.Fatal(err)
	}
	after, err := (WechatPay{}).identitySettings(changed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) {
		t.Fatal("gateway change escaped identity protection")
	}
}

//nolint:cyclop,gocognit // one branch per protocol edge; the test reads as a table of cases
func TestWechatPayMerchantValidationAndURLs(t *testing.T) {
	server := wpTestServer(t, http.NotFoundHandler())
	channel := wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), false)).(*wechatChannel)
	if err := channel.validateMerchant(MerchantInput{Methods: []MethodInput{{Upstream: "native"}, {Upstream: "h5"}}}); err != nil {
		t.Fatal(err)
	}
	if err := channel.validateMerchant(MerchantInput{Methods: []MethodInput{{Upstream: "jsapi"}}}); err == nil {
		t.Fatal("jsapi method accepted")
	}
	for _, raw := range []string{"https://pay.example.com/notify"} {
		if !wechatNotifyURL(raw) {
			t.Fatalf("notify URL %q rejected", raw)
		}
	}
	for _, raw := range []string{
		"http://pay.example.com/notify", "https://pay.example.com/notify?", "https://pay.example.com/notify?x=1", "https://user@pay.example.com/notify",
		"https://pay.example.com/notify#fragment", "https:///notify",
	} {
		if wechatNotifyURL(raw) {
			t.Fatalf("notify URL %q accepted", raw)
		}
	}
	for _, raw := range []string{"https://console.example.com/topup", "https://console.example.com/topup?x=1"} {
		if !wechatReturnURL(raw) {
			t.Fatalf("return URL %q rejected", raw)
		}
	}
	for _, raw := range []string{"http://console.example.com/topup", "https://console.example.com/topup#fragment", "https://user@console.example.com/"} {
		if wechatReturnURL(raw) {
			t.Fatalf("return URL %q accepted", raw)
		}
	}
	if !wechatCodeURL("weixin://wxpay/bizpayurl?pr=abc") || wechatCodeURL("https://wxpay.example.com/pay") {
		t.Fatal("code_url check is wrong")
	}
}

//nolint:cyclop,gocognit // one branch per protocol edge; the test reads as a table of cases
func TestWechatPayCreate(t *testing.T) {
	var mu sync.Mutex
	var creates int
	var reply struct {
		status int
		body   string
	}
	gateway := &wpTestGateway{t: t, respond: func(r *http.Request, _ []byte) (int, []byte) {
		mu.Lock()
		creates++
		status, body := reply.status, reply.body
		mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		return status, []byte(body)
	}}
	server := wpTestServer(t, gateway)
	channel := wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), false))

	r := wpTestRequest(t)
	r.Total.Minor = 12345
	r.Subject = strings.Repeat("好", 200)
	reply.body = `{"code_url":"weixin://wxpay/bizpayurl?pr=abc"}`
	checkout, err := channel.Create(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.QRCode != "weixin://wxpay/bizpayurl?pr=abc" || checkout.PayURL != "" {
		t.Fatalf("native checkout %+v", checkout)
	}
	if got := wpLastBody(t, gateway); !bytes.Contains(got, []byte(`"total":12345`)) {
		t.Fatalf("native body %s", got)
	}
	if !bytes.Contains(wpLastBody(t, gateway), []byte(`"description":"`+strings.Repeat("好", 127)+`"`)) {
		t.Fatal("description was not cut to 127 runes")
	}

	r.Upstream, r.Total.Minor = "h5", 100
	reply.body = `{"h5_url":"https://wx.tenpay.com/cgi-bin/mmpayweb-bin/checkmweb?prepay_id=abc&package=123"}`
	checkout, err = channel.Create(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://wx.tenpay.com/cgi-bin/mmpayweb-bin/checkmweb?prepay_id=abc&package=123&redirect_url=https%3A%2F%2Fconsole.example.com%2Ftopup"
	if checkout.PayURL != want {
		t.Fatalf("h5 checkout %s, want %s", checkout.PayURL, want)
	}
	if !bytes.Contains(wpLastBody(t, gateway), []byte(`"h5_info":{"type":"Wap"}`)) || !bytes.Contains(wpLastBody(t, gateway), []byte(`"payer_client_ip":"203.0.113.7"`)) {
		t.Fatalf("h5 body %s", wpLastBody(t, gateway))
	}

	mu.Lock()
	before := creates
	mu.Unlock()
	r.Upstream = "jsapi"
	if _, err := channel.Create(t.Context(), r); !wpTestRejected(err) {
		t.Fatalf("jsapi accepted: %v", err)
	}
	r.Upstream = "h5"
	r.ClientIP = netip.Addr{}
	if _, err := channel.Create(t.Context(), r); !wpTestRejected(err) {
		t.Fatalf("h5 without an IP accepted: %v", err)
	}
	r.ClientIP = netip.MustParseAddr("203.0.113.7")
	r.NotifyURL = "http://callbacks.example.com/generated"
	if _, err := channel.Create(t.Context(), r); !wpTestRejected(err) {
		t.Fatalf("http notify URL accepted: %v", err)
	}
	r.NotifyURL = "https://callbacks.example.com/generated"
	r.ReturnURL = "http://console.example.com/topup"
	if _, err := channel.Create(t.Context(), r); !wpTestRejected(err) {
		t.Fatalf("http return URL accepted: %v", err)
	}
	r.ReturnURL = "https://console.example.com/topup"
	r.Total.Minor = 0
	if _, err := channel.Create(t.Context(), r); !wpTestRejected(err) {
		t.Fatalf("zero amount accepted: %v", err)
	}
	r.Total.Minor = 100
	r.Total.Currency = currency(t, "USD")
	if _, err := channel.Create(t.Context(), r); !wpTestRejected(err) {
		t.Fatalf("USD amount accepted: %v", err)
	}
	r.Total.Currency = currency(t, "CNY")
	r.Subject = " "
	if _, err := channel.Create(t.Context(), r); !wpTestRejected(err) {
		t.Fatalf("empty subject accepted: %v", err)
	}
	r.Subject = "Top up"
	mu.Lock()
	if creates != before {
		t.Fatalf("invalid requests reached the gateway: %d -> %d", before, creates)
	}
	mu.Unlock()

	reply.status, reply.body = http.StatusBadRequest, `{"code":"PARAM_ERROR","message":"the secret echo"}`
	if _, err := channel.Create(t.Context(), r); err == nil || strings.Contains(err.Error(), "secret echo") || !strings.Contains(err.Error(), "PARAM_ERROR") {
		t.Fatalf("gateway error %v", err)
	}
	reply.status, reply.body = http.StatusOK, `{"code_url":"https://evil.example/pay"}`
	if _, err := channel.Create(t.Context(), r); err == nil {
		t.Fatal("accepted a non-weixin code_url")
	}
	r.Upstream = "native"
	reply.body = `{"code_url":"weixin://wxpay/bizpayurl?pr=abc"}`
	if _, err := channel.Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
}

func wpTestSettingsWith(t *testing.T, raw []byte, change func(*wechatSettings)) []byte {
	t.Helper()
	var s wechatSettings
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil {
		t.Fatal(err)
	}
	change(&s)
	out, err := jsonx.MarshalCanonical(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func wpTestJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := jsonx.MarshalCanonical(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func wpTestPEM(t *testing.T, kind string, der []byte) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))
}

func wpTestPublicDER(t *testing.T, key *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func wpTestRejected(err error) bool {
	var rejected *Rejected
	return errors.As(err, &rejected)
}

func wpLastBody(t *testing.T, g *wpTestGateway) []byte {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastBody
}
