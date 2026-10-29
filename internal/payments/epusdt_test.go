package payments

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"axiom/internal/platform/pg"
)

func epusdtConfig(t *testing.T, gateway string) []byte {
	t.Helper()
	return easyPayJSON(t, easyPaySettings{Gateway: gateway, PID: "1001", Preset: "epusdt"})
}

func TestEpusdtPreset(t *testing.T) {
	cfg := easyPayConfig(t, "https://pay.example.com/payments/epay/v1/order/create-transaction")
	cfg.Settings = epusdtConfig(t, "https://pay.example.com/payments/epay/v1/order/create-transaction")
	ch := easyPayOpen(t, cfg)
	if ch.Caps() != CapQuery {
		t.Fatal("Epusdt must confirm payments and refund offline", ch.Caps())
	}
	r := easyPayRequest(t)
	r.Method, r.Upstream = "usdt.tron", "usdt.tron"
	checkout, err := ch.Create(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(checkout.PayURL, "/payments/epay/v1/order/create-transaction/submit.php?") || !strings.Contains(checkout.PayURL, "type=usdt.tron") || !strings.Contains(checkout.PayURL, "currency=cny") {
		t.Fatal("incorrect Epusdt checkout")
	}
}

func TestEpusdtSaveRequiresQuery(t *testing.T) {
	w := newTestWorld(t, EasyPay{})
	var reply atomic.Value
	reply.Store(`{"code":-1,"msg":"订单不存在"}`)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		epusdtProbeReply(t, wr, r, reply.Load().(string))
	}))
	t.Cleanup(upstream.Close)
	w.p.client = easyPayConfig(t, upstream.URL, upstream.Certificate()).Client
	in := w.input(w.siteA, MethodInput{Method: "usdt.tron", Upstream: "usdt.tron", Enabled: true})
	in.Kind, in.Settings = "easypay", epusdtConfig(t, upstream.URL)
	for _, tc := range []struct {
		method          Method
		upstream, field string
	}{
		{"alipay", "usdt.tron", "/methods/0/method"},
		{"usdt.tron", "alipay", "/methods/0/upstream"},
	} {
		candidate := in
		candidate.Methods = []MethodInput{{Method: tc.method, Upstream: tc.upstream, Enabled: true}}
		if _, err := w.save(siteScope(w.siteA), candidate); !isField(err, tc.field) {
			t.Fatal("accepted incorrect Epusdt mapping", err)
		}
	}
	for _, body := range []string{"404", `<html>cashier</html>`, `{}`, `{"code":1}`, `{"code":-1,"msg":"invalid key"}`, `{"msg":"订单不存在"}`, `{"code":1,"msg":"订单不存在"}`, `{"status_code":200,"message":"success","data":{"trade_id":"T","status":2}}`} {
		reply.Store(body)
		if _, err := w.save(siteScope(w.siteA), in); !isField(err, "/settings") {
			t.Fatalf("saved without authenticated query protocol (%s): %v", body, err)
		}
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.merchants"); got != 0 {
		t.Fatal("failed save wrote a merchant", got)
	}
	reply.Store(`{"code":-1,"msg":"订单不存在"}`)
	m := w.mustSave(siteScope(w.siteA), in)
	in.ID, in.Rev, in.Secrets = MerchantID(m.ID), m.Rev, nil
	reply.Store("404")
	in.Name = "must not save"
	if _, err := w.save(siteScope(w.siteA), in); !isField(err, "/settings") {
		t.Fatal("saved update after query disappeared", err)
	}
	w.epusdtFailedUpdateUnchanged(m)

	in.Enabled = false
	disabled := w.mustSave(siteScope(w.siteA), in)
	if disabled.Enabled {
		t.Fatal("gateway outage prevented disabling merchant")
	}
	in.Rev, in.Enabled = disabled.Rev, true
	if _, err := w.save(siteScope(w.siteA), in); !isField(err, "/settings") {
		t.Fatal("re-enabled merchant without query", err)
	}
}

// Epusdt's only query route is its public cashier status, which carries neither the merchant identity nor our order
// number or fiat amount. A paid-looking answer there must not let an enabled merchant be saved, and the save probe
// must not substitute it for the merchant query.
func TestEpusdtPublicStatusCannotAuthorizeSave(t *testing.T) {
	w := newTestWorld(t, EasyPay{})
	var probes, statuses atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/payments/epay/v1/order/create-transaction/api.php":
			probes.Add(1)
			wr.WriteHeader(http.StatusNotFound)
		case strings.HasPrefix(r.URL.Path, "/pay/check-status/"):
			statuses.Add(1)
			easyPayWrite(t, wr, `{"status_code":200,"message":"success","data":{"trade_id":"T","status":2}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			wr.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	w.p.client = easyPayConfig(t, upstream.URL, upstream.Certificate()).Client
	in := w.input(w.siteA, MethodInput{Method: "usdt.tron", Upstream: "usdt.tron", Enabled: true})
	in.Kind, in.Settings = "easypay", epusdtConfig(t, upstream.URL+"/payments/epay/v1/order/create-transaction")
	if _, err := w.save(siteScope(w.siteA), in); !isField(err, "/settings") {
		t.Fatal("public status authorized an enabled merchant", err)
	}
	if probes.Load() != 1 || statuses.Load() != 0 {
		t.Fatalf("save probe asked %d merchant queries and %d public statuses", probes.Load(), statuses.Load())
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.merchants"); got != 0 {
		t.Fatal("failed save wrote a merchant", got)
	}
}

func TestEpusdtConfirmedCredit(t *testing.T) {
	w := newTestWorld(t, EasyPay{})
	gateway := &easyPayGateway{t: t, orders: map[string]easyPayGatewayOrder{}}
	var unavailable atomic.Bool
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/submit.php" {
			if unavailable.Load() {
				wr.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			gateway.ServeHTTP(wr, r)
			return
		}
		v := r.URL.Query()
		if v.Get("sign") != epaySignature(v, theKey) || v.Get("type") != "usdt.tron" || v.Get("currency") != "cny" || v.Get("money") != "100.00" {
			t.Error("incorrect signed fiat checkout")
		}
		id := v.Get("out_trade_no")
		gateway.mu.Lock()
		gateway.orders[id] = easyPayGatewayOrder{money: v.Get("money"), trade: "T-" + id, upstream: v.Get("type"), notify: v.Get("notify_url")}
		gateway.mu.Unlock()
		http.Redirect(wr, r, "/cashier/test", http.StatusFound)
	}))
	t.Cleanup(upstream.Close)
	w.p.client = easyPayConfig(t, upstream.URL, upstream.Certificate()).Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA, MethodInput{Method: "usdt.tron", Upstream: "usdt.tron", Enabled: true})
	in.Kind, in.Settings = "easypay", epusdtConfig(t, upstream.URL)
	merchant := w.mustSave(siteScope(w.siteA), in)
	o, checkout, err := w.placeAs(w.accountA(), CreateOrder{Site: w.site(w.siteA, "CNY"), Account: w.account, User: w.user, Method: "usdt.tron", Credits: 100 * yuan, Key: "epusdt-paid", ClientIP: netip.MustParseAddr("203.0.113.7"), ReturnURL: "https://console.example.com/balance"})
	if err != nil {
		t.Fatal(err)
	}
	w.epusdtIdentityCannotChange(in, merchant)
	epusdtVisitCheckout(t, upstream, checkout.PayURL)
	v := gateway.settle(o.ID, "100.0000")
	v.Set("type", "usdt.tron")
	v.Set("sign", epaySignature(v, theKey))
	unavailable.Store(true)
	w.epusdtUnconfirmed(o, v)
	unavailable.Store(false)
	if response := w.callback(o.MerchantID, http.MethodGet, v); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body)
	}
	if response := w.callback(o.MerchantID, http.MethodGet, v); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body)
	}
	w.assertCredit(o, 100*yuan)
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatal(state)
	}
	gateway.mu.Lock()
	queries := gateway.queries
	gateway.mu.Unlock()
	if queries != 1 {
		t.Fatal("confirmation query missing or replay queried twice", queries)
	}
}

func (w *world) epusdtIdentityCannotChange(in MerchantInput, merchant Merchant) {
	w.t.Helper()
	// Removing the preset must not let an in-flight crypto order lose mandatory query confirmation.
	in.ID, in.Rev, in.Secrets = MerchantID(merchant.ID), merchant.Rev, nil
	in.Settings = []byte(strings.ReplaceAll(merchant.Settings, `"preset":"epusdt",`, ""))
	if _, err := w.save(siteScope(w.siteA), in); !isField(err, "/settings") {
		w.t.Fatal("removed preset with an order in flight", err)
	}
}

func epusdtVisitCheckout(t *testing.T, upstream *httptest.Server, payURL string) {
	t.Helper()
	browser := upstream.Client()
	browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, payURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusFound {
		t.Fatal("checkout did not create a gateway order", response.StatusCode)
	}
}

func TestEpusdtPresetRejectsUnsupportedConfiguration(t *testing.T) {
	for name, edit := range map[string]func(*MerchantConfig, *easyPaySettings){
		"pid/text":         func(_ *MerchantConfig, s *easyPaySettings) { s.PID = "merchant" },
		"pid/leading zero": func(_ *MerchantConfig, s *easyPaySettings) { s.PID = "01001" },
		"pid/zero":         func(_ *MerchantConfig, s *easyPaySettings) { s.PID = "0" },
		"pid/negative":     func(_ *MerchantConfig, s *easyPaySettings) { s.PID = "-1" },
		"pid/plus":         func(_ *MerchantConfig, s *easyPaySettings) { s.PID = "+1" },
		"pid/overflow":     func(_ *MerchantConfig, s *easyPaySettings) { s.PID = "9223372036854775808" },
		"preset":           func(_ *MerchantConfig, s *easyPaySettings) { s.Preset = "unknown" },
		"API":              func(_ *MerchantConfig, s *easyPaySettings) { s.Mode = "api" },
		"close":            func(_ *MerchantConfig, s *easyPaySettings) { s.Close = true },
		"refund":           func(_ *MerchantConfig, s *easyPaySettings) { s.Refund = true },
		"cid":              func(_ *MerchantConfig, s *easyPaySettings) { s.CIDs = map[Method]string{"usdt.tron": "1"} },
		"crypto amount":    func(c *MerchantConfig, _ *easyPaySettings) { c.Currency = currency(t, "BTC") },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := easyPayConfig(t, "https://pay.example.com")
			s := easyPaySettings{Gateway: "https://pay.example.com", PID: "1001", Preset: "epusdt"}
			edit(&cfg, &s)
			cfg.Settings = easyPayJSON(t, s)
			if _, err := (EasyPay{}).Open(cfg); err == nil {
				t.Fatal("accepted unsupported Epusdt configuration")
			}
		})
	}
}

func (w *world) epusdtUnconfirmed(o Order, v url.Values) {
	w.t.Helper()
	if response := w.callback(o.MerchantID, http.MethodGet, v); response.Code != http.StatusBadGateway {
		w.t.Fatal("unconfirmed callback acknowledged", response.Code)
	}
	w.assertCredit(o, 0)
	if state, _, _ := w.stored(o.ID); state != "pending" {
		w.t.Fatal(state)
	}
}

func epusdtProbeReply(t *testing.T, wr http.ResponseWriter, r *http.Request, body string) {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Error(err)
		return
	}
	v := r.PostForm
	if r.URL.Path != "/api.php" || v.Get("act") != "order" || v.Get("pid") != "1001" || v.Get("key") != theKey || !strings.HasPrefix(v.Get("out_trade_no"), "ord_") {
		t.Error("wrong query probe")
	}
	if body == "404" {
		http.NotFound(wr, r)
		return
	}
	easyPayWrite(t, wr, body)
}

func (w *world) epusdtFailedUpdateUnchanged(m Merchant) {
	w.t.Helper()
	if err := w.tx(siteScope(w.siteA), func(tx pg.Tx) error {
		stored, err := ReadMerchant(w.t.Context(), tx, MerchantID(m.ID))
		if err == nil && (stored.Rev != m.Rev || stored.Name != m.Name) {
			w.t.Error("failed update changed merchant")
		}
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}
