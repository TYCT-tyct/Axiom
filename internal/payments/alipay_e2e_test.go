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

//nolint:cyclop,gocognit // one protocol walk: each step's settled state is the next step's input
func TestAlipayLocalGatewayEndToEnd(t *testing.T) {
	w := newTestWorld(t, Alipay{})
	state := &alipayE2EState{orders: map[string]*alipayE2EOrder{}, refunds: map[string]string{}}
	gateway := alipayE2EGateway(t, state)
	server := httptest.NewTLSServer(gateway)
	t.Cleanup(server.Close)
	cfg := alipayTestConfig(t, server.URL, server.Certificate())
	w.p.client = cfg.Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA, MethodInput{Method: "alipay", Upstream: alipayPagePay, Fee: Fee{PPM: 30_000}, Enabled: true})
	in.Kind, in.Settings, in.Secrets = "alipay", cfg.Settings, cfg.Secrets
	in.RefundEnabled, in.UserRefund = true, true
	merchant := w.mustSave(siteScope(w.siteA), in)

	o, checkout, err := w.place(10*yuan, "alipay-paid")
	if err != nil {
		t.Fatal(err)
	}
	keys := alipayKeys(t)
	u, err := url.Parse(checkout.PayURL)
	if err != nil {
		t.Fatal(err)
	}
	form := u.Query()
	if !alipayVerify([]byte(alipaySignText(form, true)), form.Get("sign"), &keys.app.PublicKey) {
		t.Fatal("checkout signature does not verify")
	}
	var biz map[string]string
	if err := jsonx.Unmarshal([]byte(form.Get("biz_content")), &biz); err != nil {
		t.Fatal(err)
	}
	amount := biz["total_amount"]
	if amount != formatMinorText(o.Total) || biz["out_trade_no"] != o.ID.TypeID() {
		t.Fatalf("%v", biz)
	}
	state.settle(o.ID.TypeID(), amount, "T-"+o.ID.TypeID())

	v := url.Values{
		"app_id": {alipayTestAppID}, "seller_id": {alipayTestSellerID},
		"out_trade_no": {o.ID.TypeID()}, "trade_no": {"T-" + o.ID.TypeID()},
		"trade_status": {"TRADE_SUCCESS"}, "total_amount": {amount},
		"notify_type": {"trade_status_sync"}, "sign_type": {"RSA2"},
	}
	v = alipayNotification(t, v, keys.alipay)
	response := w.callback(MerchantID(merchant.ID), http.MethodPost, v)
	if response.Code != http.StatusOK || response.Body.String() != "success" {
		t.Fatalf("callback %d %s", response.Code, response.Body)
	}
	w.assertCredit(o, o.Credits)
	if state, trade, _ := w.stored(o.ID); state != "paid" || trade != "T-"+o.ID.TypeID() {
		t.Fatalf("%s %s", state, trade)
	}
	if queries := gateway.counts(); queries != 1 {
		t.Fatalf("confirmation queried %d times", queries)
	}

	// A replay of the exact committed receipt neither queries nor credits again.
	response = w.callback(MerchantID(merchant.ID), http.MethodPost, v)
	if response.Code != http.StatusOK || response.Body.String() != "success" {
		t.Fatalf("replay %d %s", response.Code, response.Body)
	}
	w.assertCredit(o, o.Credits)
	if queries := gateway.counts(); queries != 1 {
		t.Fatalf("replay queried %d times", queries)
	}

	// An invalid amount is a verified payment with nowhere to go: review, no credit, one discrepancy.
	bad, _, err := w.place(yuan, "alipay-invalid")
	if err != nil {
		t.Fatal(err)
	}
	state.settle(bad.ID.TypeID(), "1.001", "T-"+bad.ID.TypeID())
	badValues := url.Values{
		"app_id": {alipayTestAppID}, "seller_id": {alipayTestSellerID},
		"out_trade_no": {bad.ID.TypeID()}, "trade_no": {"T-" + bad.ID.TypeID()},
		"trade_status": {"TRADE_SUCCESS"}, "total_amount": {"1.001"},
		"notify_type": {"trade_status_sync"}, "sign_type": {"RSA2"},
	}
	if response := w.callback(MerchantID(merchant.ID), http.MethodPost, alipayNotification(t, badValues, keys.alipay)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body)
	}
	w.assertCredit(bad, 0)
	if state, _, _ := w.stored(bad.ID); state != "review" {
		t.Fatalf("invalid amount state %s", state)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 1 {
		t.Fatal("invalid payment has no discrepancy")
	}

	// A lost callback is recovered by the scheduled query.
	lost, _, err := w.place(yuan, "alipay-lost")
	if err != nil {
		t.Fatal(err)
	}
	state.settle(lost.ID.TypeID(), formatMinorText(lost.Total), "T-"+lost.ID.TypeID())
	w.exec(fmt.Sprintf("UPDATE payments.orders SET next_query_at = clock_timestamp() - interval '1 second' WHERE id = '%s'", uuid.UUID(lost.ID)))
	w.p.pollOrder(t.Context(), lost.ID)
	w.assertCredit(lost, lost.Credits)
	if state, _, _ := w.stored(lost.ID); state != "paid" {
		t.Fatalf("lost callback state %s", state)
	}

	// Close a waiting order, then refund the paid one through the core.
	cancelled, _, err := w.place(yuan, "alipay-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.p.Cancel(t.Context(), w.accountA(), cancelled.ID); err != nil {
		t.Fatal(err)
	}
	if !state.isClosed(cancelled.ID.TypeID()) {
		t.Fatal("close did not reach the gateway")
	}

	refund := w.mustRefund(o, o.Credits, "alipay-refund")
	if refund.State != "frozen" {
		t.Fatalf("refund state %s", refund.State)
	}
	w.approve(refund)
	w.dueRefunds()
	stored, err := w.p.GetRefund(t.Context(), w.accountA(), refund.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != "succeeded" || stored.ProviderRef != refund.ID.TypeID() {
		t.Fatalf("%+v", stored)
	}
	var balance int64
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT balance FROM billing.balances WHERE account_id = $1", w.account).Scan(&balance)
	}); err != nil {
		t.Fatal(err)
	}
	if balance != lost.Credits {
		t.Fatalf("refund left balance %d, want the other paid order's %d", balance, lost.Credits)
	}

	// An unknown refund stays frozen from the money's point of view and then converges by query on the same request id.
	unknown, _, err := w.place(yuan, "alipay-unknown")
	if err != nil {
		t.Fatal(err)
	}
	state.settle(unknown.ID.TypeID(), formatMinorText(unknown.Total), "T-"+unknown.ID.TypeID())
	unknownValues := url.Values{
		"app_id": {alipayTestAppID}, "seller_id": {alipayTestSellerID},
		"out_trade_no": {unknown.ID.TypeID()}, "trade_no": {"T-" + unknown.ID.TypeID()},
		"trade_status": {"TRADE_SUCCESS"}, "total_amount": {formatMinorText(unknown.Total)},
		"notify_type": {"trade_status_sync"}, "sign_type": {"RSA2"},
	}
	if response := w.callback(MerchantID(merchant.ID), http.MethodPost, alipayNotification(t, unknownValues, keys.alipay)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body)
	}
	state.mu.Lock()
	state.refundMode = "unknown"
	state.mu.Unlock()
	pending := w.mustRefund(unknown, unknown.Credits, "alipay-unknown-refund")
	w.approve(pending)
	w.dueRefunds()
	stored, err = w.p.GetRefund(t.Context(), w.accountA(), pending.ID)
	if err != nil || stored.State != "pending" {
		t.Fatalf("%+v %v", stored, err)
	}
	state.mu.Lock()
	state.refundMode = "success"
	state.mu.Unlock()
	w.dueRefunds()
	stored, err = w.p.GetRefund(t.Context(), w.accountA(), pending.ID)
	if err != nil || stored.State != "succeeded" {
		t.Fatalf("%+v %v", stored, err)
	}
	if stored.ProviderRef != pending.ID.TypeID() {
		t.Fatalf("refund query changed the request id: %s", stored.ProviderRef)
	}

	// No secret in logs or order events.
	var raw string
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT string_agg(raw::text, '') FROM payments.order_events").Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	alipayNoSecrets(t, w.logs.String(), "PRIVATE KEY", "sign=")
	alipayNoSecrets(t, raw, "PRIVATE KEY", "sign=")
}
