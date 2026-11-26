package payments

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/secret"
)

// The replay tests send every plugin family the notifications its provider documents, signed with the documented
// algorithm and a test key, through the callback route and the core, against a gateway that answers the documented
// query and refund shapes. Money invariants every row holds:
//
//   - an order is credited at most once, with its own credits, and only after a verified notification whose amount
//     equals the order total;
//   - a notification signed without the merchant's key is answered 400 and writes nothing;
//   - a verified payment of the wrong amount is never credited: the order goes to review.

// replayProvider is one merchant account on a local gateway that speaks the provider's protocol.
type replayProvider struct {
	w        *world
	merchant MerchantID
	place    func(t *testing.T, key string) Order
	// pay makes the gateway report o as paid with minor units, as its query will answer.
	pay func(o Order, minor int64)
	// notify is the provider's notification of that payment; forged signs it with a key that is not the merchant's.
	notify func(t *testing.T, o Order, minor int64, forged bool) Callback
	refund bool
}

func (w *world) deliver(merchant MerchantID, cb Callback) *httptest.ResponseRecorder {
	w.t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET "+NotifyPath+"{merchant}", w.p.Notify())
	mux.Handle("POST "+NotifyPath+"{merchant}", w.p.Notify())
	target := NotifyPath + merchant.TypeID()
	if cb.RawQuery != "" {
		target += "?" + cb.RawQuery
	}
	r := httptest.NewRequestWithContext(w.t.Context(), cb.Method, target, bytes.NewReader(cb.Body))
	for k, v := range cb.Header {
		r.Header[k] = v
	}
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, r)
	return rw
}

func replayForm(v url.Values) Callback {
	return Callback{Method: http.MethodPost, Body: []byte(v.Encode()), Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}}
}

func minorText(o Order, minor int64) string {
	return formatMinorText(Money{Currency: o.Total.Currency, Minor: minor})
}

// TestProviderNotifyReplay runs one table per plugin family: valid notification, bad signature, wrong amount,
// repeated notification, a late notification after the order expired, and refund.
func TestProviderNotifyReplay(t *testing.T) {
	providers := []struct {
		name string
		open func(t *testing.T) *replayProvider
	}{
		{"alipay", replayAlipay}, {"wechatpay", replayWechatPay}, {"stripe", replayStripe},
		{"easypay", replayEasyPay}, {"epusdt", replayEpusdt}, {"ezpay", replayEzPay},
	}
	for _, provider := range providers {
		t.Run(provider.name, func(t *testing.T) {
			p := provider.open(t)
			for _, row := range replayRows {
				t.Run(row.name, func(t *testing.T) { row.run(t, p) })
			}
		})
	}
}

var replayRows = []struct {
	name string
	run  func(t *testing.T, p *replayProvider)
}{
	{"valid notify credits once", func(t *testing.T, p *replayProvider) {
		o := p.place(t, "replay-valid")
		p.pay(o, o.Total.Minor)
		p.expectAck(t, p.notify(t, o, o.Total.Minor, false))
		p.expectPaid(t, o)
	}},
	{"bad signature writes nothing", func(t *testing.T, p *replayProvider) {
		o := p.place(t, "replay-forged")
		events := p.w.events(o.ID)
		p.pay(o, o.Total.Minor)
		if r := p.w.deliver(p.merchant, p.notify(t, o, o.Total.Minor, true)); r.Code != http.StatusBadRequest {
			t.Fatalf("forged notification answered %d %s", r.Code, r.Body)
		}
		p.w.assertCredit(o, 0)
		if state, _, _ := p.w.stored(o.ID); state != string(Pending) || p.w.events(o.ID) != events {
			t.Fatalf("forged notification moved the order: %s", state)
		}
		if n := p.w.count(p.w.p.platform, "SELECT count(*) FROM payments.order_payments WHERE order_id = $1", uuid.UUID(o.ID)); n != 0 {
			t.Fatalf("forged notification recorded %d payments", n)
		}
	}},
	{"wrong amount goes to review", func(t *testing.T, p *replayProvider) {
		o := p.place(t, "replay-short")
		p.pay(o, o.Total.Minor-1)
		p.expectAck(t, p.notify(t, o, o.Total.Minor-1, false))
		p.w.assertCredit(o, 0)
		if state, _, _ := p.w.stored(o.ID); state != string(Review) {
			t.Fatalf("short payment state %s", state)
		}
	}},
	{"repeated notify credits once", func(t *testing.T, p *replayProvider) {
		o := p.place(t, "replay-repeat")
		p.pay(o, o.Total.Minor)
		cb := p.notify(t, o, o.Total.Minor, false)
		for range 3 {
			p.expectAck(t, cb)
		}
		p.expectPaid(t, o)
	}},
	{"late notify after expiry credits once", func(t *testing.T, p *replayProvider) {
		o := p.place(t, "replay-late")
		p.w.endOrder(o, Expired)
		p.pay(o, o.Total.Minor)
		p.expectAck(t, p.notify(t, o, o.Total.Minor, false))
		p.expectPaid(t, o)
	}},
	{"refund", func(t *testing.T, p *replayProvider) {
		if !p.refund {
			ch, err := p.w.channel(p.merchant)
			if err != nil {
				t.Fatal(err)
			}
			if ch.Caps().Has(CapRefund) {
				t.Fatal("a gateway without a refund API declares refunds")
			}
			return
		}
		o := p.place(t, "replay-refund")
		p.pay(o, o.Total.Minor)
		p.expectAck(t, p.notify(t, o, o.Total.Minor, false))
		refund := p.w.approve(p.w.mustRefund(o, o.Credits, "replay-refund"))
		p.w.dueRefunds()
		stored, err := p.w.p.GetRefund(t.Context(), p.w.accountA(), refund.ID)
		if err != nil || stored.State != "succeeded" {
			t.Fatalf("refund %+v %v", stored, err)
		}
	}},
}

func (p *replayProvider) expectAck(t *testing.T, cb Callback) {
	t.Helper()
	if r := p.w.deliver(p.merchant, cb); r.Code < 200 || r.Code > 299 {
		t.Fatalf("notification answered %d %s", r.Code, r.Body)
	}
}

func (p *replayProvider) expectPaid(t *testing.T, o Order) {
	t.Helper()
	p.w.assertCredit(o, o.Credits)
	if state, _, _ := p.w.stored(o.ID); state != string(Paid) {
		t.Fatalf("order state %s", state)
	}
	for _, table := range []string{"billing.credit_batches WHERE id", "payments.order_payments WHERE order_id"} {
		if n := p.w.count(p.w.p.platform, "SELECT count(*) FROM "+table+" = $1", uuid.UUID(o.ID)); n != 1 {
			t.Fatalf("%s: %d rows, want one", table, n)
		}
	}
}

// replayAlipay follows the 开放平台 asynchronous notification: a POST form signed RSA2 over every parameter but sign and
// sign_type, sorted and unescaped.
func replayAlipay(t *testing.T) *replayProvider {
	f := newAlipayE2E(t)
	return &replayProvider{
		w: f.w, merchant: MerchantID(f.merchant.ID), refund: true,
		place: func(t *testing.T, key string) Order {
			o, _, err := f.w.place(10*yuan, key)
			if err != nil {
				t.Fatal(err)
			}
			return o
		},
		pay: func(o Order, minor int64) { f.state.settle(o.ID.TypeID(), minorText(o, minor), "T-"+o.ID.TypeID()) },
		notify: func(t *testing.T, o Order, minor int64, forged bool) Callback {
			v := url.Values{
				"notify_time": {"2026-11-26 10:00:00"}, "notify_type": {"trade_status_sync"}, "notify_id": {"ac05099524730693a8b330c5ecf72da9786"},
				"app_id": {alipayTestAppID}, "auth_app_id": {alipayTestAppID}, "seller_id": {alipayTestSellerID}, "charset": {"utf-8"},
				"version": {"1.0"}, "sign_type": {"RSA2"}, "trade_no": {"T-" + o.ID.TypeID()}, "out_trade_no": {o.ID.TypeID()},
				"trade_status": {"TRADE_SUCCESS"}, "total_amount": {minorText(o, minor)}, "receipt_amount": {minorText(o, minor)},
				"subject": {"Axiom 充值"}, "gmt_payment": {"2026-11-26 09:59:58"},
			}
			key := f.keys.alipay
			if forged {
				key = f.keys.app
			}
			return replayForm(alipayNotification(t, v, key))
		},
	}
}

// replayWechatPay follows APIv3: an AEAD_AES_256_GCM resource in a JSON envelope signed by the platform certificate.
func replayWechatPay(t *testing.T) *replayProvider {
	f := newWPE2E(t)
	f.state.refundMode = "success"
	return &replayProvider{
		w: f.w, merchant: MerchantID(f.merchant.ID), refund: true,
		place: func(_ *testing.T, key string) Order {
			o, _ := f.place("wxpay", 10*yuan, key)
			return o
		},
		pay: func(o Order, minor int64) { f.state.settle(o.ID.TypeID(), minor) },
		notify: func(t *testing.T, o Order, minor int64, forged bool) Callback {
			signer := wpKeys(t).platform
			if forged {
				signer = wpKeys(t).other
			}
			return wpTestCallback(t, wpTransaction(o.ID.TypeID(), "T-"+o.ID.TypeID(), minor, "SUCCESS"), signer, wpTestPlatformSerial(t))
		},
	}
}

// replayStripe follows Checkout: checkout.session.completed signed with the endpoint's whsec_ secret, confirmed and
// refunded through the PaymentIntent.
func replayStripe(t *testing.T) *replayProvider {
	w := newTestWorld(t, Stripe{})
	var mu sync.Mutex
	intents := map[string]int64{} // PaymentIntent ID to the amount Stripe reports
	server := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/balance":
			writeStripeJSON(t, rw, map[string]any{"object": "balance", "available": []any{}, "pending": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/checkout/sessions":
			session := stripeSession("cs_"+r.PostForm.Get("client_reference_id"), "open", "unpaid")
			session["url"] = "https://checkout.stripe.com/c/pay/cs_1"
			session["expires_at"] = time.Now().Add(time.Hour).Unix()
			writeStripeJSON(t, rw, session)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/payment_intents/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/payment_intents/")
			writeStripeJSON(t, rw, map[string]any{"id": id, "object": "payment_intent", "status": "succeeded", "amount": intents[id], "currency": "usd"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/refunds":
			amount, err := strconv.ParseInt(r.PostForm.Get("amount"), 10, 64)
			if err != nil {
				t.Error(err)
			}
			writeStripeJSON(t, rw, map[string]any{
				"id": "re_" + r.PostForm.Get("payment_intent"), "object": "refund", "status": "succeeded",
				"amount": amount, "currency": "usd", "payment_intent": r.PostForm.Get("payment_intent"),
			})
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(server.Close)
	old := stripeAPIBase
	stripeAPIBase = server.URL
	t.Cleanup(func() { stripeAPIBase = old })
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	t.Cleanup(client.CloseIdleConnections)
	w.p.client = client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA, MethodInput{Method: "card", Upstream: "card", Fee: Fee{PPM: 30_000}, Enabled: true})
	in.Kind, in.Settings, in.Currency = "stripe", []byte("{}"), currency(t, "USD")
	in.Secrets = map[string]secret.Secret{"secret_key": secret.New("sk_test_123"), "webhook_secret": secret.New(stripeTestWebhookSecret)}
	in.RefundEnabled, in.UserRefund = true, true
	merchant := w.mustSave(siteScope(w.siteA), in)
	return &replayProvider{
		w: w, merchant: MerchantID(merchant.ID), refund: true,
		place: func(t *testing.T, key string) Order {
			o, _, err := w.placeAs(w.accountA(), CreateOrder{
				Site: w.site(w.siteA, "USD"), Account: w.account, User: w.user, Method: "card", Credits: 10 * yuan, Key: key,
				ReturnURL: "https://console.example.com/topup", ClientIP: netip.MustParseAddr("203.0.113.7"),
			})
			if err != nil {
				t.Fatal(err)
			}
			return o
		},
		pay: func(o Order, minor int64) {
			mu.Lock()
			defer mu.Unlock()
			intents["pi_"+o.ID.TypeID()] = minor
		},
		notify: func(t *testing.T, o Order, minor int64, forged bool) Callback {
			payload := stripeEvent(t, map[string]any{
				"id": "cs_" + o.ID.TypeID(), "object": "checkout.session", "status": "complete", "payment_status": "paid",
				"amount_total": minor, "currency": "usd", "payment_intent": "pi_" + o.ID.TypeID(),
				"client_reference_id": o.ID.TypeID(), "metadata": map[string]any{"order": o.ID.TypeID(), "merchant": merchant.ID.String()},
			}, "checkout.session.completed")
			secretValue := stripeTestWebhookSecret
			if forged {
				secretValue = "whsec_not_this_endpoint"
			}
			return stripeSigned(t, payload, secretValue)
		},
	}
}

// replayEasyPayGateway answers 彩虹易支付's api.php for orders the test pays directly.
func replayEasyPayGateway(t *testing.T) *easyPayGateway {
	return &easyPayGateway{t: t, orders: map[string]easyPayGatewayOrder{}}
}

func (g *easyPayGateway) pay(o Order, minor int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.orders[o.ID.TypeID()] = easyPayGatewayOrder{money: minorText(o, minor), trade: "T-" + o.ID.TypeID(), paid: true}
}

// easyPayNotify is the documented GET notification: MD5 over the sorted non-empty parameters but sign and sign_type,
// unescaped, with the merchant key appended.
func easyPayNotify(o Order, minor int64, forged bool, extra url.Values) Callback {
	v := url.Values{
		"pid": {"1001"}, "trade_no": {"T-" + o.ID.TypeID()}, "out_trade_no": {o.ID.TypeID()}, "type": {"alipay"},
		"name": {"Axiom 充值"}, "money": {minorText(o, minor)}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"MD5"},
	}
	for k, values := range extra {
		v[k] = values
	}
	key := theKey
	if forged {
		key = "not-the-merchant-key"
	}
	v.Set("sign", epaySignature(v, key))
	return Callback{Method: http.MethodGet, RawQuery: v.Encode()}
}

func replayEasyPay(t *testing.T) *replayProvider {
	w := newTestWorld(t, EasyPay{})
	gateway := replayEasyPayGateway(t)
	upstream := httptest.NewTLSServer(gateway)
	t.Cleanup(upstream.Close)
	cfg := easyPayConfig(t, upstream.URL, upstream.Certificate())
	w.p.client = cfg.Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA)
	in.Kind, in.Settings = "easypay", cfg.Settings
	in.RefundEnabled, in.UserRefund = true, true
	merchant := w.mustSave(siteScope(w.siteA), in)
	return &replayProvider{
		w: w, merchant: MerchantID(merchant.ID), refund: true,
		place: func(_ *testing.T, key string) Order { return w.mustPlace(10*yuan, key) },
		pay:   gateway.pay,
		notify: func(_ *testing.T, o Order, minor int64, forged bool) Callback {
			return easyPayNotify(o, minor, forged, nil)
		},
	}
}

// replayEpusdt uses Epusdt's EPay-compatible route; its callback carries the stored type selector.
func replayEpusdt(t *testing.T) *replayProvider {
	w := newTestWorld(t, EasyPay{})
	gateway := replayEasyPayGateway(t)
	upstream := httptest.NewTLSServer(gateway)
	t.Cleanup(upstream.Close)
	w.p.client = easyPayConfig(t, upstream.URL, upstream.Certificate()).Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA, MethodInput{Method: "usdt.tron", Upstream: "usdt.tron", Enabled: true})
	in.Kind, in.Settings = "easypay", epusdtConfig(t, upstream.URL)
	merchant := w.mustSave(siteScope(w.siteA), in)
	return &replayProvider{
		w: w, merchant: MerchantID(merchant.ID),
		place: func(t *testing.T, key string) Order {
			o, _, err := w.placeAs(w.accountA(), CreateOrder{
				Site: w.site(w.siteA, "CNY"), Account: w.account, User: w.user, Method: "usdt.tron", Credits: 10 * yuan, Key: key,
				ReturnURL: "https://console.example.com/topup", ClientIP: netip.MustParseAddr("203.0.113.7"),
			})
			if err != nil {
				t.Fatal(err)
			}
			return o
		},
		pay: gateway.pay,
		notify: func(_ *testing.T, o Order, minor int64, forged bool) Callback {
			return easyPayNotify(o, minor, forged, url.Values{"type": {"usdt.tron"}})
		},
	}
}

// replayEzPay uses EzPay's EasyPay-compatible API: GET order queries and a callback that adds signed currency fields.
func replayEzPay(t *testing.T) *replayProvider {
	w := newTestWorld(t, EasyPay{})
	var mu sync.Mutex
	paid := map[string]string{} // order to the USD amount EzPay reports
	server := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/api.php" || q.Get("act") != "order" || q.Get("key") != theKey {
			http.Error(rw, "wrong query", http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		money, ok := paid[q.Get("out_trade_no")]
		mu.Unlock()
		if !ok {
			ezPayWrite(rw, `{"code":-1,"msg":"订单不存在"}`)
			return
		}
		id := q.Get("out_trade_no")
		ezPayWrite(rw, fmt.Sprintf(`{"code":1,"trade_status":"TRADE_SUCCESS","money":%q,"trade_no":"T-%s","out_trade_no":%q,"type":"usdt_trc20","currency":"USD"}`, money, id, id))
	}))
	t.Cleanup(server.Close)
	w.p.client = ezPayConfig(t, server.URL, server.Certificate()).Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA, MethodInput{Method: "usdt.tron", Upstream: "usdt_trc20", Enabled: true})
	in.Kind, in.Settings, in.Currency = "easypay", ezPaySettingsJSON(t, server.URL, "1001"), currency(t, "USD")
	merchant := w.mustSave(siteScope(w.siteA), in)
	return &replayProvider{
		w: w, merchant: MerchantID(merchant.ID),
		place: func(t *testing.T, key string) Order {
			o, _, err := w.placeAs(w.accountA(), CreateOrder{
				Site: w.site(w.siteA, "USD"), Account: w.account, User: w.user, Method: "usdt.tron", Credits: 10 * yuan, Key: key,
				ReturnURL: "https://console.example.com/topup", ClientIP: netip.MustParseAddr("203.0.113.7"),
			})
			if err != nil {
				t.Fatal(err)
			}
			return o
		},
		pay: func(o Order, minor int64) {
			mu.Lock()
			defer mu.Unlock()
			paid[o.ID.TypeID()] = minorText(o, minor)
		},
		notify: func(_ *testing.T, o Order, minor int64, forged bool) Callback {
			return easyPayNotify(o, minor, forged, url.Values{
				"type": {"usdt_trc20"}, "currency": {"USD"}, "pay_currency": {"USDT"}, "pay_amount": {minorText(o, minor)},
			})
		},
	}
}
