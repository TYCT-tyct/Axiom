package payments

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

type alipayE2EOrder struct {
	money, trade, status string
	closed               bool
}

type alipayE2EState struct {
	mu         sync.Mutex
	orders     map[string]*alipayE2EOrder
	refunds    map[string]string // out_request_no to refund_amount
	refundMode string            // "", "unknown" or "success"
}

func (s *alipayE2EState) settle(id, money, trade string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders[id] = &alipayE2EOrder{money: money, trade: trade, status: "TRADE_SUCCESS"}
}

func (s *alipayE2EState) isClosed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.orders[id]
	return o != nil && o.closed
}

//nolint:gocognit // one branch per gateway method; each branch settles the state the next step reads
func alipayE2EGateway(t *testing.T, state *alipayE2EState) *alipayTestGateway {
	t.Helper()
	return &alipayTestGateway{t: t, respond: func(method string, biz map[string]jsonx.Raw, _ url.Values) (string, bool) {
		id := alipayBiz(t, biz, "out_trade_no")
		switch method {
		case "alipay.trade.query":
			state.mu.Lock()
			o := state.orders[id]
			state.mu.Unlock()
			if o == nil {
				return `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.TRADE_NOT_EXIST"}`, true
			}
			return alipayQueryNode(id, o.trade, o.status, o.money), true
		case "alipay.trade.close":
			state.mu.Lock()
			o := state.orders[id]
			if o == nil {
				o = &alipayE2EOrder{}
				state.orders[id] = o
			}
			o.closed = true
			state.mu.Unlock()
			return fmt.Sprintf(`{"code":"10000","msg":"Success","out_trade_no":%q}`, id), true
		case "alipay.trade.refund":
			state.mu.Lock()
			mode := state.refundMode
			amount := alipayBiz(t, biz, "refund_amount")
			state.refunds[alipayBiz(t, biz, "out_request_no")] = amount
			state.mu.Unlock()
			if mode == "unknown" {
				return `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.SYSTEM_ERROR"}`, true
			}
			state.mu.Lock()
			trade := ""
			if o := state.orders[id]; o != nil {
				trade = o.trade
			}
			state.mu.Unlock()
			return fmt.Sprintf(`{"code":"10000","msg":"Success","out_trade_no":%q,"trade_no":%q,"fund_change":"Y","refund_fee":%q}`,
				id, trade, amount), true
		case "alipay.trade.fastpay.refund.query":
			state.mu.Lock()
			mode := state.refundMode
			amount := state.refunds[alipayBiz(t, biz, "out_request_no")]
			state.mu.Unlock()
			if mode == "success" {
				return fmt.Sprintf(`{"code":"10000","msg":"Success","out_request_no":%q,"refund_status":"REFUND_SUCCESS","refund_amount":%q}`,
					alipayBiz(t, biz, "out_request_no"), amount), true
			}
			return `{"code":"10000","msg":"Success"}`, true
		default:
			t.Errorf("unexpected method %s", method)
			return `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.SYSTEM_ERROR"}`, true
		}
	}}
}

// alipayE2E is one local Alipay world across the walk's phases: the paid order is what the refund phase refunds.
type alipayE2E struct {
	t        *testing.T
	w        *world
	state    *alipayE2EState
	gateway  *alipayTestGateway
	merchant Merchant
	keys     *alipayTestKeys
	paid     Order
}

func newAlipayE2E(t *testing.T) *alipayE2E {
	t.Helper()
	f := &alipayE2E{
		t: t, w: newTestWorld(t, Alipay{}),
		state: &alipayE2EState{orders: map[string]*alipayE2EOrder{}, refunds: map[string]string{}},
		keys:  alipayKeys(t),
	}
	f.gateway = alipayE2EGateway(t, f.state)
	server := httptest.NewTLSServer(f.gateway)
	t.Cleanup(server.Close)
	cfg := alipayTestConfig(t, server.URL, server.Certificate())
	f.w.p.client = cfg.Client
	f.w.open(f.w.siteA, RoundRobin)
	in := f.w.input(f.w.siteA, MethodInput{Method: "alipay", Upstream: alipayPagePay, Fee: Fee{PPM: 30_000}, Enabled: true})
	in.Kind, in.Settings, in.Secrets = "alipay", cfg.Settings, cfg.Secrets
	in.RefundEnabled, in.UserRefund = true, true
	f.merchant = f.w.mustSave(siteScope(f.w.siteA), in)
	return f
}

// checkCheckout asserts the checkout carries a verifiable signature and the right amount, and returns that amount.
func (f *alipayE2E) checkCheckout(o Order, checkout Checkout) string {
	f.t.Helper()
	u, err := url.Parse(checkout.PayURL)
	if err != nil {
		f.t.Fatal(err)
	}
	form := u.Query()
	if !alipayVerify([]byte(alipaySignText(form, true)), form.Get("sign"), &f.keys.app.PublicKey) {
		f.t.Fatal("checkout signature does not verify")
	}
	var biz map[string]string
	if err := jsonx.Unmarshal([]byte(form.Get("biz_content")), &biz); err != nil {
		f.t.Fatal(err)
	}
	amount := biz["total_amount"]
	if amount != formatMinorText(o.Total) || biz["out_trade_no"] != o.ID.TypeID() {
		f.t.Fatalf("%v", biz)
	}
	return amount
}

// checkCheckoutAndCallback asserts the checkout carries a verifiable signature and the right amount, the callback
// credits once with a confirmation query, and a replay neither queries nor credits again.
func (f *alipayE2E) checkCheckoutAndCallback() {
	f.t.Helper()
	o, checkout, err := f.w.place(10*yuan, "alipay-paid")
	if err != nil {
		f.t.Fatal(err)
	}
	f.paid = o
	amount := f.checkCheckout(o, checkout)
	f.state.settle(o.ID.TypeID(), amount, "T-"+o.ID.TypeID())
	v := url.Values{
		"app_id": {alipayTestAppID}, "seller_id": {alipayTestSellerID},
		"out_trade_no": {o.ID.TypeID()}, "trade_no": {"T-" + o.ID.TypeID()},
		"trade_status": {"TRADE_SUCCESS"}, "total_amount": {amount},
		"notify_type": {"trade_status_sync"}, "sign_type": {"RSA2"},
	}
	v = alipayNotification(f.t, v, f.keys.alipay)
	response := f.w.callback(MerchantID(f.merchant.ID), http.MethodPost, v)
	if response.Code != http.StatusOK || response.Body.String() != "success" {
		f.t.Fatalf("callback %d %s", response.Code, response.Body)
	}
	f.w.assertCredit(o, o.Credits)
	if state, trade, _ := f.w.stored(o.ID); state != "paid" || trade != "T-"+o.ID.TypeID() {
		f.t.Fatalf("%s %s", state, trade)
	}
	if queries := f.gateway.counts(); queries != 1 {
		f.t.Fatalf("confirmation queried %d times", queries)
	}

	// A replay of the exact committed receipt neither queries nor credits again.
	response = f.w.callback(MerchantID(f.merchant.ID), http.MethodPost, v)
	if response.Code != http.StatusOK || response.Body.String() != "success" {
		f.t.Fatalf("replay %d %s", response.Code, response.Body)
	}
	f.w.assertCredit(o, o.Credits)
	if queries := f.gateway.counts(); queries != 1 {
		f.t.Fatalf("replay queried %d times", queries)
	}
}

// checkInvalidAmount asserts a verified payment whose amount cannot be parsed goes to review with one discrepancy.
func (f *alipayE2E) checkInvalidAmount() {
	f.t.Helper()
	bad, _, err := f.w.place(yuan, "alipay-invalid")
	if err != nil {
		f.t.Fatal(err)
	}
	f.state.settle(bad.ID.TypeID(), "1.001", "T-"+bad.ID.TypeID())
	badValues := url.Values{
		"app_id": {alipayTestAppID}, "seller_id": {alipayTestSellerID},
		"out_trade_no": {bad.ID.TypeID()}, "trade_no": {"T-" + bad.ID.TypeID()},
		"trade_status": {"TRADE_SUCCESS"}, "total_amount": {"1.001"},
		"notify_type": {"trade_status_sync"}, "sign_type": {"RSA2"},
	}
	if response := f.w.callback(MerchantID(f.merchant.ID), http.MethodPost, alipayNotification(f.t, badValues, f.keys.alipay)); response.Code != http.StatusOK {
		f.t.Fatal(response.Code, response.Body)
	}
	f.w.assertCredit(bad, 0)
	if state, _, _ := f.w.stored(bad.ID); state != "review" {
		f.t.Fatalf("invalid amount state %s", state)
	}
	if f.w.count(f.w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 1 {
		f.t.Fatal("invalid payment has no discrepancy")
	}
}

// checkLostCallback asserts the scheduled query recovers a payment whose callback never arrived.
func (f *alipayE2E) checkLostCallback() Order {
	f.t.Helper()
	lost, _, err := f.w.place(yuan, "alipay-lost")
	if err != nil {
		f.t.Fatal(err)
	}
	f.state.settle(lost.ID.TypeID(), formatMinorText(lost.Total), "T-"+lost.ID.TypeID())
	f.w.exec(fmt.Sprintf("UPDATE payments.orders SET next_query_at = clock_timestamp() - interval '1 second' WHERE id = '%s'", uuid.UUID(lost.ID)))
	f.w.p.pollOrder(f.t.Context(), lost.ID)
	f.w.assertCredit(lost, lost.Credits)
	if state, _, _ := f.w.stored(lost.ID); state != "paid" {
		f.t.Fatalf("lost callback state %s", state)
	}
	return lost
}

// checkCancelAndRefund closes a waiting order and refunds the paid one through the core, leaving that order's credit.
func (f *alipayE2E) checkCancelAndRefund(paid, lost Order) {
	f.t.Helper()
	cancelled, _, err := f.w.place(yuan, "alipay-cancel")
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.w.p.Cancel(f.t.Context(), f.w.accountA(), cancelled.ID); err != nil {
		f.t.Fatal(err)
	}
	if !f.state.isClosed(cancelled.ID.TypeID()) {
		f.t.Fatal("close did not reach the gateway")
	}

	refund := f.w.mustRefund(paid, paid.Credits, "alipay-refund")
	if refund.State != "frozen" {
		f.t.Fatalf("refund state %s", refund.State)
	}
	f.w.approve(refund)
	f.w.dueRefunds()
	stored, err := f.w.p.GetRefund(f.t.Context(), f.w.accountA(), refund.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if stored.State != "succeeded" || stored.ProviderRef != refund.ID.TypeID() {
		f.t.Fatalf("%+v", stored)
	}
	var balance int64
	if err := f.w.tx(f.w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(f.t.Context(), "SELECT balance FROM billing.balances WHERE account_id = $1", f.w.account).Scan(&balance)
	}); err != nil {
		f.t.Fatal(err)
	}
	if balance != lost.Credits {
		f.t.Fatalf("refund left balance %d, want the other paid order's %d", balance, lost.Credits)
	}
}

// checkUnknownRefund asserts an unknown refund stays frozen and converges by query on the same request id.
func (f *alipayE2E) checkUnknownRefund() {
	f.t.Helper()
	unknown, _, err := f.w.place(yuan, "alipay-unknown")
	if err != nil {
		f.t.Fatal(err)
	}
	f.state.settle(unknown.ID.TypeID(), formatMinorText(unknown.Total), "T-"+unknown.ID.TypeID())
	unknownValues := url.Values{
		"app_id": {alipayTestAppID}, "seller_id": {alipayTestSellerID},
		"out_trade_no": {unknown.ID.TypeID()}, "trade_no": {"T-" + unknown.ID.TypeID()},
		"trade_status": {"TRADE_SUCCESS"}, "total_amount": {formatMinorText(unknown.Total)},
		"notify_type": {"trade_status_sync"}, "sign_type": {"RSA2"},
	}
	if response := f.w.callback(MerchantID(f.merchant.ID), http.MethodPost, alipayNotification(f.t, unknownValues, f.keys.alipay)); response.Code != http.StatusOK {
		f.t.Fatal(response.Code, response.Body)
	}
	f.state.mu.Lock()
	f.state.refundMode = "unknown"
	f.state.mu.Unlock()
	pending := f.w.mustRefund(unknown, unknown.Credits, "alipay-unknown-refund")
	f.w.approve(pending)
	f.w.dueRefunds()
	stored, err := f.w.p.GetRefund(f.t.Context(), f.w.accountA(), pending.ID)
	if err != nil || stored.State != "pending" {
		f.t.Fatalf("%+v %v", stored, err)
	}
	f.state.mu.Lock()
	f.state.refundMode = "success"
	f.state.mu.Unlock()
	f.w.dueRefunds()
	stored, err = f.w.p.GetRefund(f.t.Context(), f.w.accountA(), pending.ID)
	if err != nil || stored.State != "succeeded" {
		f.t.Fatalf("%+v %v", stored, err)
	}
	if stored.ProviderRef != pending.ID.TypeID() {
		f.t.Fatalf("refund query changed the request id: %s", stored.ProviderRef)
	}
}

// checkNoSecrets asserts neither the log nor the order events carry a key or a signature.
func (f *alipayE2E) checkNoSecrets() {
	f.t.Helper()
	var raw string
	if err := f.w.tx(f.w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(f.t.Context(), "SELECT string_agg(raw::text, '') FROM payments.order_events").Scan(&raw)
	}); err != nil {
		f.t.Fatal(err)
	}
	alipayNoSecrets(f.t, f.w.logs.String(), "PRIVATE KEY", "sign=")
	alipayNoSecrets(f.t, raw, "PRIVATE KEY", "sign=")
}

// TestAlipayLocalGatewayEndToEnd walks one Alipay gateway: checkout, callback, replay, an unparseable amount, a lost
// callback, cancellation, a refund, and a refund whose answer is unknown first.
func TestAlipayLocalGatewayEndToEnd(t *testing.T) {
	f := newAlipayE2E(t)
	f.checkCheckoutAndCallback()
	f.checkInvalidAmount()
	lost := f.checkLostCallback()
	f.checkCancelAndRefund(f.paid, lost)
	f.checkUnknownRefund()
	f.checkNoSecrets()
}
