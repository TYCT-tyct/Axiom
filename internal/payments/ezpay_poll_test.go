package payments

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"uuid"
)

func TestEzPayPollCreditsALostCallback(t *testing.T) {
	w := newTestWorld(t, EasyPay{})
	var paid atomic.Bool
	var order string
	server := httptest.NewTLSServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api.php" || r.URL.Query().Get("act") != "order" {
			http.Error(wr, "wrong query", http.StatusMethodNotAllowed)
			return
		}
		if got := r.URL.Query().Get("out_trade_no"); got != order {
			ezPayWrite(wr, `{"code":-1,"msg":"订单不存在"}`)
			return
		}
		if !paid.Load() {
			ezPayWrite(wr, `{"code":1,"trade_status":"WAIT_BUYER_PAY","money":"10.00","trade_no":"T-wait","out_trade_no":"`+order+`","currency":"USD"}`)
			return
		}
		ezPayWrite(wr, `{"code":1,"trade_status":"TRADE_SUCCESS","money":"10.00","trade_no":"T-paid","out_trade_no":"`+order+`","currency":"USD"}`)
	}))
	t.Cleanup(server.Close)
	w.p.client = ezPayConfig(t, server.URL, server.Certificate()).Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA, MethodInput{Method: "usdt.tron", Upstream: "usdt_trc20", Enabled: true})
	in.Kind, in.Settings, in.Currency = "easypay", ezPaySettingsJSON(t, server.URL, "1001"), currency(t, "USD")
	w.mustSave(siteScope(w.siteA), in)

	o, _, err := w.placeAs(w.accountA(), CreateOrder{
		Site: w.site(w.siteA, "USD"), Account: w.account, User: w.user, Method: "usdt.tron",
		Credits: 10 * yuan, Key: "ezpay-lost", ReturnURL: "https://console.example.com/topup",
		ClientIP: netip.MustParseAddr("203.0.113.7"),
	})
	if err != nil {
		t.Fatal(err)
	}
	order = o.ID.TypeID()
	paid.Store(true)
	w.exec(fmt.Sprintf("UPDATE payments.orders SET expires_at = transaction_timestamp() - interval '1 minute', next_query_at = transaction_timestamp() - interval '1 minute' WHERE id = '%s'", uuid.UUID(o.ID)))
	if err := w.p.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.assertCredit(o, o.Credits)
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatal("poll did not keep the late payment paid", state)
	}
	ch, err := w.channel(o.MerchantID)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Caps() != CapQuery {
		t.Fatal("EzPay must confirm payments and leave close and refund offline", ch.Caps())
	}
}
