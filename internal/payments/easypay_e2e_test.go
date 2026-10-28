package payments

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

type easyPayGatewayOrder struct {
	money, trade, upstream, cid, notify string
	paid, closed                        bool
}

type easyPayGateway struct {
	t                        *testing.T
	mu                       sync.Mutex
	orders                   map[string]easyPayGatewayOrder
	queries, closes, refunds int
}

func (g *easyPayGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		g.t.Error(err)
		return
	}
	v := r.PostForm
	g.mu.Lock()
	defer g.mu.Unlock()
	id := v.Get("out_trade_no")
	if r.URL.Path == "/mapi.php" {
		if v.Get("sign") != epaySignature(v, theKey) {
			g.t.Error("invalid gateway creation signature")
		}
		trade := "T-" + id
		g.orders[id] = easyPayGatewayOrder{money: v.Get("money"), trade: trade, upstream: v.Get("type"), cid: v.Get("cid"), notify: v.Get("notify_url")}
		easyPayWrite(g.t, w, fmt.Sprintf(`{"code":"1","trade_no":%q,"payurl":"https://pay.example.com/checkout"}`, trade))
		return
	}
	if diff := cmp.Diff([]string{theKey, "1001"}, []string{v.Get("key"), v.Get("pid")}); diff != "" {
		g.t.Error("invalid gateway credentials")
	}
	o, ok := g.orders[id]
	if !ok {
		easyPayWrite(g.t, w, `{"code":0,"msg":"订单不存在"}`)
		return
	}
	switch v.Get("act") {
	case "order":
		if r.URL.RawQuery != v.Encode() {
			g.t.Error("query missing URL credentials")
		}
		g.queries++
		status := "0"
		if o.paid {
			status = "1"
		}
		easyPayWrite(g.t, w, fmt.Sprintf(`{"code":"1","data":{"status":%q,"money":%q,"trade_no":%q,"out_trade_no":%q}}`, status, o.money, o.trade, id))
	case "close":
		g.closes++
		o.closed = true
		g.orders[id] = o
		easyPayWrite(g.t, w, `{"code":"1"}`)
	case "refund":
		g.refunds++
		easyPayWrite(g.t, w, `{"code":"1"}`)
	default:
		g.t.Error("unexpected gateway operation")
	}
}

func (g *easyPayGateway) settle(id OrderID, money string) url.Values {
	g.mu.Lock()
	defer g.mu.Unlock()
	o := g.orders[id.TypeID()]
	if money != "" {
		o.money = money
	}
	o.paid = true
	g.orders[id.TypeID()] = o
	v := url.Values{"pid": {"1001"}, "out_trade_no": {id.TypeID()}, "trade_no": {o.trade}, "money": {o.money}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"MD5"}}
	v.Set("sign", epaySignature(v, theKey))
	return v
}

func TestEasyPayLocalGatewayEndToEnd(t *testing.T) {
	w := newTestWorld(t, EasyPay{})
	gateway := &easyPayGateway{t: t, orders: map[string]easyPayGatewayOrder{}}
	upstream := httptest.NewTLSServer(gateway)
	t.Cleanup(upstream.Close)
	cfg := easyPayConfig(t, upstream.URL+"/mapi.php?discard=normalization#fragment", upstream.Certificate())
	w.p.client = cfg.Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA)
	in.Kind = "easypay"
	in.Settings = []byte(strings.ReplaceAll(string(cfg.Settings), "https://", "http://"))
	if _, err := w.save(siteScope(w.siteA), in); !isField(err, "/settings") {
		t.Fatalf("saved plaintext gateway: %v", err)
	}
	in.Settings = cfg.Settings
	m := w.mustSave(siteScope(w.siteA), in)
	easyPayNoSecrets(t, m.Settings, "mapi.php", "discard", "fragment")
	o := w.mustPlace(10*yuan, "gateway-paid")
	in.ID, in.Rev, in.Secrets = MerchantID(m.ID), m.Rev, nil
	if updated := w.mustSave(siteScope(w.siteA), in); updated.Settings != m.Settings {
		t.Fatal("equivalent gateway spelling changed sealed identity")
	}
	callbackMux := http.NewServeMux()
	callbackMux.Handle("GET "+NotifyPath+"{merchant}", w.p.Notify())
	callbackMux.Handle("POST "+NotifyPath+"{merchant}", w.p.Notify())
	ingress := httptest.NewServer(callbackMux)
	t.Cleanup(ingress.Close)

	v := gateway.settle(o.ID, "")
	w.easyPayHTTPNotify(ingress.URL, o.MerchantID, http.MethodGet, v)
	w.easyPayHTTPNotify(ingress.URL, o.MerchantID, http.MethodPost, v)
	w.assertCredit(o, o.Credits)
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatal(state)
	}
	gateway.mu.Lock()
	queries := gateway.queries
	gateway.mu.Unlock()
	if queries != 1 || w.count(w.p.platform, "SELECT count(*) FROM payments.order_payments") != 1 {
		t.Fatal("callback replay queried or credited twice")
	}
	// Verified invalid amounts remain visible even when the confirmation query agrees with the callback.
	for _, money := range []string{"1.001", "0.00", "1,000.00"} {
		invalid := w.mustPlace(yuan, "gateway-invalid-"+money)
		w.easyPayHTTPNotify(ingress.URL, o.MerchantID, http.MethodPost, gateway.settle(invalid.ID, money))
		w.assertCredit(invalid, 0)
		if state, _, _ := w.stored(invalid.ID); state != "review" {
			t.Fatalf("invalid amount %s state %s", money, state)
		}
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 3 {
		t.Fatal("invalid payment has no discrepancy")
	}
	w.easyPayCloseAndRefund(gateway, o)
	w.easyPaySanitizedEvents()
}

func TestEasyPayInvalidAmountWithoutQueryGoesToReview(t *testing.T) {
	w := newTestWorld(t, EasyPay{})
	cfg := easyPayConfig(t, "https://pay.example.com")
	var settings easyPaySettings
	if err := jsonx.UnmarshalKnown(cfg.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Mode = "redirect"
	settings.Query = false
	raw, err := jsonx.MarshalCanonical(settings)
	if err != nil {
		t.Fatal(err)
	}
	w.p.client = cfg.Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA)
	in.Kind = "easypay"
	in.Settings = raw
	w.mustSave(siteScope(w.siteA), in)
	for _, money := range []string{"1.001", "0.00", "0.0000", "1,000.00", "1e3"} {
		for _, trade := range []string{"T-precision", ""} {
			o := w.mustPlace(yuan, "invalid-"+money+trade)
			v := callbackValues(o)
			v.Set("trade_no", trade)
			v.Set("money", money)
			v.Set("sign", epaySignature(v, theKey))
			if response := w.callback(o.MerchantID, http.MethodGet, v); response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body)
			}
			if state, _, _ := w.stored(o.ID); state != "review" {
				t.Fatalf("invalid amount %s state %s", money, state)
			}
			w.assertCredit(o, 0)
		}
	}
}

func (w *world) easyPayHTTPNotify(base string, merchant MerchantID, method string, values url.Values) {
	w.t.Helper()
	path := base + NotifyPath + merchant.TypeID()
	var body io.Reader
	if method == http.MethodGet {
		path += "?" + values.Encode()
	} else {
		body = strings.NewReader(values.Encode())
	}
	req, err := http.NewRequestWithContext(w.t.Context(), method, path, body)
	if err != nil {
		w.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	data, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil || resp.StatusCode != http.StatusOK || string(data) != "success" {
		w.t.Fatalf("callback %d %q, read=%v close=%v", resp.StatusCode, data, readErr, closeErr)
	}
}

func (w *world) easyPayCloseAndRefund(gateway *easyPayGateway, o Order) {
	t := w.t
	cancelled := w.mustPlace(yuan, "gateway-cancel")
	if _, err := w.p.Cancel(t.Context(), w.accountA(), cancelled.ID); err != nil {
		t.Fatal(err)
	}
	ch, err := w.channel(o.MerchantID)
	if err != nil {
		t.Fatal(err)
	}
	if refund, err := ch.Refund(t.Context(), RefundCall{Order: o.ID, TradeNo: o.TradeNo, Amount: o.Total}); err != nil || refund.State != RefundSucceeded {
		t.Fatal(refund, err)
	}
	gateway.mu.Lock()
	closed := gateway.orders[cancelled.ID.TypeID()].closed
	closes, refunds := gateway.closes, gateway.refunds
	gateway.mu.Unlock()
	if diff := cmp.Diff([]bool{true, true, true}, []bool{closed, closes == 1, refunds == 1}); diff != "" {
		t.Fatalf("close/refund: %v %d %d", closed, closes, refunds)
	}
}

func (w *world) easyPaySanitizedEvents() {
	t := w.t
	easyPayNoSecrets(t, w.logs.String(), theKey, "?key=", "sign=")
	var raw string
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT string_agg(raw::text, '') FROM payments.order_events").Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	easyPayNoSecrets(t, raw, theKey, "sign=")
}

func TestEasyPayMethodChangesKeepTheMerchantIdentity(t *testing.T) {
	w := newTestWorld(t, EasyPay{})
	gateway := &easyPayGateway{t: t, orders: map[string]easyPayGatewayOrder{}}
	server := httptest.NewTLSServer(gateway)
	t.Cleanup(server.Close)
	cfg := easyPayConfig(t, server.URL, server.Certificate())
	w.p.client = cfg.Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA)
	in.Kind = "easypay"
	in.Settings = cfg.Settings
	in.Methods[0].Upstream = "ali"
	merchant := w.mustSave(siteScope(w.siteA), in)
	first := w.mustPlace(yuan, "mapping-first")
	gateway.assertCreated(first.ID, "ali", "2", "https://console.example.com"+NotifyPath+first.MerchantID.TypeID())
	var settings easyPaySettings
	if err := jsonx.UnmarshalKnown(cfg.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	settings.CIDs["alipay"] = "9"
	settings.Query, settings.Close, settings.Refund = false, false, false
	in.ID, in.Rev, in.Secrets = MerchantID(merchant.ID), merchant.Rev, nil
	in.Methods[0].Upstream = "ali_v2"
	in.Methods = append(in.Methods, MethodInput{Method: "wxpay", Upstream: "wechat", Enabled: true})
	in.Settings = easyPayJSON(t, settings)
	merchant = w.mustSave(siteScope(w.siteA), in)
	second := w.mustPlace(yuan, "mapping-second")
	gateway.assertCreated(second.ID, "ali_v2", "9", "https://console.example.com"+NotifyPath+second.MerchantID.TypeID())
	ch, err := w.channel(first.MerchantID)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Caps() != 0 {
		t.Fatal("operational capability change did not apply", ch.Caps())
	}
	in.Rev = merchant.Rev
	settings.Mode = "redirect"
	in.Settings = easyPayJSON(t, settings)
	in.Methods = in.Methods[1:]
	merchant = w.mustSave(siteScope(w.siteA), in)
	if len(merchant.Methods) != 1 || merchant.Methods[0].Method != "wxpay" {
		t.Fatal("could not remove a method with orders in flight")
	}
	in.Rev = merchant.Rev
	w.easyPayIdentityEditsFail(in, settings)
}

func (g *easyPayGateway) assertCreated(id OrderID, upstream, cid, notify string) {
	g.t.Helper()
	g.mu.Lock()
	order := g.orders[id.TypeID()]
	g.mu.Unlock()
	if diff := cmp.Diff([]string{upstream, cid, notify}, []string{order.upstream, order.cid, order.notify}); diff != "" {
		g.t.Fatal(diff)
	}
}

func (w *world) easyPayIdentityEditsFail(in MerchantInput, settings easyPaySettings) {
	w.t.Helper()
	for _, edit := range []func(*easyPaySettings){
		func(s *easyPaySettings) { s.Gateway = "https://different.example.com" },
		func(s *easyPaySettings) { s.PID = "other-merchant" },
		func(s *easyPaySettings) { s.SignType = "other-signature" },
	} {
		changed := settings
		edit(&changed)
		candidate := in
		candidate.Settings = easyPayJSON(w.t, changed)
		if _, err := w.save(siteScope(w.siteA), candidate); !isField(err, "/settings") {
			w.t.Fatalf("identity update with pending orders: %v", err)
		}
	}
}
