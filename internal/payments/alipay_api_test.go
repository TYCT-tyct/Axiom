package payments

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"uuid"

	"axiom/internal/platform/jsonx"
)

func alipayQueryNode(outTradeNo, tradeNo, status, amount string) string {
	return fmt.Sprintf(`{"code":"10000","msg":"Success","out_trade_no":%q,"trade_no":%q,"trade_status":%q,"total_amount":%q}`,
		outTradeNo, tradeNo, status, amount)
}

//nolint:gocognit // the table and its assertions are the test
func TestAlipayQuery(t *testing.T) {
	for name, tc := range map[string]struct {
		node                  string
		signed, corrupt, omit bool
		state                 PayState
		minor                 int64
		wantErr               bool
	}{
		"paid":            {node: alipayQueryNode("ord_1", "T1", "TRADE_SUCCESS", "100.00"), signed: true, state: PayPaid, minor: 10_000},
		"finished":        {node: alipayQueryNode("ord_1", "T1", "TRADE_FINISHED", "100.0"), signed: true, state: PayPaid, minor: 10_000},
		"pending":         {node: alipayQueryNode("ord_1", "T1", "WAIT_BUYER_PAY", "100.00"), signed: true, state: PayPending, minor: 10_000},
		"closed":          {node: alipayQueryNode("ord_1", "T1", "TRADE_CLOSED", "100.00"), signed: true, state: PayClosed, minor: 10_000},
		"missing amount":  {node: alipayQueryNode("ord_1", "T1", "TRADE_SUCCESS", ""), signed: true, state: PayPaid},
		"invalid amount":  {node: alipayQueryNode("ord_1", "T1", "TRADE_SUCCESS", "1.001"), signed: true, state: PayPaid, minor: -1},
		"unknown status":  {node: alipayQueryNode("ord_1", "T1", "TRADE_UNKNOWN", "1.00"), signed: true, wantErr: true},
		"other order":     {node: alipayQueryNode("ord_2", "T1", "TRADE_SUCCESS", "1.00"), signed: true, wantErr: true},
		"not exists":      {node: `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.TRADE_NOT_EXIST"}`, signed: true, state: PayPending},
		"system error":    {node: `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.SYSTEM_ERROR"}`, signed: true, wantErr: true},
		"unsigned error":  {node: `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.TRADE_NOT_EXIST"}`, wantErr: true},
		"unsigned result": {node: `{"code":"10000","msg":"Success"}`, signed: false, wantErr: true},
		"corrupt sign":    {node: alipayQueryNode("ord_1", "T1", "TRADE_SUCCESS", "1.00"), signed: true, corrupt: true, wantErr: true},
		"missing sign":    {node: alipayQueryNode("ord_1", "T1", "TRADE_SUCCESS", "1.00"), signed: true, omit: true, wantErr: true},
		"invalid result":  {node: `not json`, signed: true, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			ch, _ := alipayChannelWithGateway(t, &alipayTestGateway{
				t: t, corruptSignature: tc.corrupt, omitSignature: tc.omit,
				respond: func(method string, biz map[string]jsonx.Raw, _ url.Values) (string, bool) {
					if method != "alipay.trade.query" || alipayBiz(t, biz, "out_trade_no") != "ord_1" {
						t.Errorf("query %s %v", method, biz)
					}
					return tc.node, tc.signed
				},
			})
			payment, err := ch.Query(t.Context(), "ord_1", "")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %+v", payment)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if payment.State != tc.state || payment.Paid.Minor != tc.minor || tc.state == PayPaid && payment.TradeNo != "T1" {
				t.Fatalf("%+v", payment)
			}
		})
	}
}

func TestAlipayClose(t *testing.T) {
	for name, tc := range map[string]struct {
		node        string
		signed, err bool
	}{
		"closed":         {node: `{"code":"10000","msg":"Success","out_trade_no":"ord_1"}`, signed: true},
		"already absent": {node: `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.TRADE_NOT_EXIST"}`, signed: true},
		"refused":        {node: `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.SYSTEM_ERROR"}`, signed: true, err: true},
		"unsigned":       {node: `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.TRADE_NOT_EXIST"}`, err: true},
	} {
		t.Run(name, func(t *testing.T) {
			order := OrderID(mustUUID(t, "00000000-0000-0000-0000-000000000001"))
			ch, gateway := alipayChannelWith(t, func(method string, biz map[string]jsonx.Raw, _ url.Values) (string, bool) {
				if method != "alipay.trade.close" || alipayBiz(t, biz, "out_trade_no") != order.TypeID() {
					t.Errorf("close %s %v", method, biz)
				}
				return tc.node, tc.signed
			})
			err := ch.Close(t.Context(), order, "")
			if (err != nil) != tc.err {
				t.Fatalf("%v", err)
			}
			if gateway.closes != 1 {
				t.Fatalf("closes %d", gateway.closes)
			}
		})
	}
}

//nolint:cyclop,gocognit // one outcome per row; the table is the test
func TestAlipayRefund(t *testing.T) {
	call := func() RefundCall {
		return RefundCall{
			Refund: RefundID(mustUUID(t, "00000000-0000-0000-0000-000000000002")),
			Order:  OrderID(mustUUID(t, "00000000-0000-0000-0000-000000000001")),
			Amount: Money{currency(t, "CNY"), 1234},
			Reason: strings.Repeat("退", 300),
		}
	}
	requestID := func(r RefundCall) string { return r.Refund.TypeID() }
	for name, tc := range map[string]struct {
		fundChange string
		failure    string
		signed     bool
		state      RefundOutcome
		rejected   bool
		wantErr    bool
		refundFee  string
	}{
		"succeeded":    {fundChange: "Y", signed: true, state: RefundSucceeded},
		"pending":      {fundChange: "N", signed: true, state: RefundPending},
		"not exists":   {failure: "ACQ.TRADE_NOT_EXIST", signed: true, rejected: true},
		"status error": {failure: "ACQ.TRADE_STATUS_ERROR", signed: true, rejected: true},
		"amount error": {failure: "ACQ.REFUND_AMT_NOT_EQUAL_TOTAL", signed: true, rejected: true},
		"system error": {failure: "ACQ.SYSTEM_ERROR", signed: true, wantErr: true},
		"unsigned":     {failure: "ACQ.TRADE_NOT_EXIST", wantErr: true},
		"wrong amount": {fundChange: "Y", signed: true, wantErr: true, refundFee: "12.35"},
	} {
		t.Run(name, func(t *testing.T) {
			r := call()
			ch, gateway := alipayChannelWith(t, func(method string, biz map[string]jsonx.Raw, _ url.Values) (string, bool) {
				if method != "alipay.trade.refund" {
					t.Errorf("method %s", method)
				}
				if alipayBiz(t, biz, "out_trade_no") != r.Order.TypeID() || alipayBiz(t, biz, "out_request_no") != requestID(r) ||
					alipayBiz(t, biz, "refund_amount") != "12.34" {
					t.Errorf("refund biz %v", biz)
				}
				if got := utf8.RuneCountInString(alipayBiz(t, biz, "refund_reason")); got != 256 {
					t.Errorf("reason runes %d", got)
				}
				if tc.failure != "" {
					return fmt.Sprintf(`{"code":"40004","msg":"Business Failed","sub_code":%q}`, tc.failure), tc.signed
				}
				fee := tc.refundFee
				if fee == "" {
					fee = "12.34"
				}
				return fmt.Sprintf(`{"code":"10000","msg":"Success","out_trade_no":%q,"trade_no":"T1","fund_change":%q,"refund_fee":%q}`,
					r.Order.TypeID(), tc.fundChange, fee), tc.signed
			})
			state, err := ch.Refund(t.Context(), r)
			var rejected *Rejected
			if tc.rejected {
				if !errors.As(err, &rejected) || rejected.Code != "alipay."+tc.failure {
					t.Fatalf("%v %+v", err, rejected)
				}
			} else if tc.wantErr {
				if err == nil || errors.As(err, &rejected) {
					t.Fatalf("%v", err)
				}
			} else {
				if err != nil || state.State != tc.state || state.ProviderRef != requestID(r) {
					t.Fatalf("%+v %v", state, err)
				}
			}
			if gateway.refunds != 1 {
				t.Fatalf("refunds %d", gateway.refunds)
			}
			if tc.failure != "" && err != nil && !strings.Contains(err.Error(), tc.failure) {
				t.Fatalf("error lost the gateway code: %v", err)
			}
		})
	}

	t.Run("invalid amounts never reach the gateway", func(t *testing.T) {
		for _, amount := range []Money{{currency(t, "CNY"), 0}, {currency(t, "USD"), 100}, {currency(t, "CNY"), -1}} {
			r := call()
			r.Amount = amount
			ch, gateway := alipayChannelWith(t, func(string, map[string]jsonx.Raw, url.Values) (string, bool) {
				t.Error("gateway called")
				return "", true
			})
			_, err := ch.Refund(t.Context(), r)
			var rejected *Rejected
			if !errors.As(err, &rejected) || gateway.refunds != 0 {
				t.Fatalf("%v %d", err, gateway.refunds)
			}
		}
	})

	t.Run("other order echo", func(t *testing.T) {
		r := call()
		ch, _ := alipayChannelWith(t, func(string, map[string]jsonx.Raw, url.Values) (string, bool) {
			return `{"code":"10000","msg":"Success","out_trade_no":"ord_other","fund_change":"Y"}`, true
		})
		if _, err := ch.Refund(t.Context(), r); err == nil {
			t.Fatal("refund echoed another order")
		}
	})

	t.Run("unsigned success", func(t *testing.T) {
		r := call()
		ch, _ := alipayChannelWith(t, func(string, map[string]jsonx.Raw, url.Values) (string, bool) {
			return fmt.Sprintf(`{"code":"10000","out_trade_no":%q,"fund_change":"Y"}`, r.Order.TypeID()), false
		})
		if _, err := ch.Refund(t.Context(), r); err == nil {
			t.Fatal("unsigned refund accepted")
		}
	})
}

//nolint:cyclop,gocognit // one outcome per row; the table is the test
func TestAlipayQueryRefund(t *testing.T) {
	makeCall := func(t *testing.T) RefundCall {
		return RefundCall{
			Refund: RefundID(mustUUID(t, "00000000-0000-0000-0000-000000000002")),
			Order:  OrderID(mustUUID(t, "00000000-0000-0000-0000-000000000001")),
			Amount: Money{currency(t, "CNY"), 1234},
		}
	}
	for name, tc := range map[string]struct {
		node     string
		signed   bool
		sentAt   time.Time
		provider string
		state    RefundOutcome
		wantErr  bool
	}{
		"success":       {node: `{"code":"10000","msg":"Success","out_request_no":"%s","refund_status":"REFUND_SUCCESS","refund_amount":"12.34"}`, signed: true, state: RefundSucceeded},
		"recent empty":  {node: `{"code":"10000","msg":"Success"}`, signed: true, sentAt: time.Now(), state: RefundPending},
		"no send time":  {node: `{"code":"10000","msg":"Success"}`, signed: true, state: RefundPending},
		"old empty":     {node: `{"code":"10000","msg":"Success"}`, signed: true, sentAt: time.Now().Add(-11 * time.Minute), state: RefundFailed},
		"old zero":      {node: `{"code":"10000","msg":"Success","refund_amount":"0.00"}`, signed: true, sentAt: time.Now().Add(-11 * time.Minute), state: RefundFailed},
		"old invalid":   {node: `{"code":"10000","msg":"Success","refund_amount":"oops"}`, signed: true, sentAt: time.Now().Add(-11 * time.Minute), state: RefundPending},
		"grace edge":    {node: `{"code":"10000","msg":"Success"}`, signed: true, sentAt: time.Now().Add(-9*time.Minute - 59*time.Second), state: RefundPending},
		"not exists":    {node: `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.TRADE_NOT_EXIST"}`, signed: true, state: RefundFailed},
		"system error":  {node: `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.SYSTEM_ERROR"}`, signed: true, wantErr: true},
		"unsigned":      {node: `{"code":"10000","msg":"Success"}`, wantErr: true},
		"other request": {node: `{"code":"10000","msg":"Success","out_request_no":"rfd_other","refund_status":"REFUND_SUCCESS"}`, signed: true, wantErr: true},
		"other amount":  {node: `{"code":"10000","msg":"Success","out_request_no":"%s","refund_status":"REFUND_SUCCESS","refund_amount":"12.35"}`, signed: true, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := makeCall(t)
			r.SentAt = tc.sentAt
			r.ProviderRef = tc.provider
			expected := r.Refund.TypeID()
			if r.ProviderRef != "" {
				expected = r.ProviderRef
			}
			node := strings.ReplaceAll(tc.node, "%s", expected)
			ch, _ := alipayChannelWith(t, func(method string, biz map[string]jsonx.Raw, _ url.Values) (string, bool) {
				if method != "alipay.trade.fastpay.refund.query" || alipayBiz(t, biz, "out_request_no") != expected ||
					alipayBiz(t, biz, "out_trade_no") != r.Order.TypeID() {
					t.Errorf("query refund %s %v", method, biz)
				}
				return node, tc.signed
			})
			state, err := ch.QueryRefund(t.Context(), r)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("%+v", state)
				}
				return
			}
			if err != nil || state.State != tc.state || state.ProviderRef != expected {
				t.Fatalf("%+v %v", state, err)
			}
		})
	}

	t.Run("provider reference wins", func(t *testing.T) {
		r := makeCall(t)
		r.ProviderRef = "rfd_other"
		ch, _ := alipayChannelWith(t, func(_ string, biz map[string]jsonx.Raw, _ url.Values) (string, bool) {
			if alipayBiz(t, biz, "out_request_no") != "rfd_other" {
				t.Errorf("%v", biz)
			}
			return `{"code":"10000","msg":"Success","out_request_no":"rfd_other","refund_status":"REFUND_SUCCESS"}`, true
		})
		if state, err := ch.QueryRefund(t.Context(), r); err != nil || state.State != RefundSucceeded {
			t.Fatal(state, err)
		}
	})
}

func TestAlipayTestConfig(t *testing.T) {
	t.Run("not exists is accepted", func(t *testing.T) {
		var probe string
		ch, _ := alipayChannelWith(t, func(method string, biz map[string]jsonx.Raw, _ url.Values) (string, bool) {
			if method != "alipay.trade.query" {
				t.Errorf("method %s", method)
			}
			probe = alipayBiz(t, biz, "out_trade_no")
			return `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.TRADE_NOT_EXIST"}`, true
		})
		if err := ch.(*alipayChannel).testConfig(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(probe, "probe_") {
			t.Fatalf("probe %q", probe)
		}
	})
	t.Run("bad credentials fail", func(t *testing.T) {
		ch, _ := alipayChannelWith(t, func(string, map[string]jsonx.Raw, url.Values) (string, bool) {
			return `{"code":"40002","msg":"Invalid Arguments","sub_code":"isv.invalid-app-id"}`, false
		})
		if err := ch.(*alipayChannel).testConfig(t.Context()); err == nil {
			t.Fatal("invalid credentials accepted")
		}
	})
	t.Run("trade found fails", func(t *testing.T) {
		ch, _ := alipayChannelWith(t, func(string, map[string]jsonx.Raw, url.Values) (string, bool) {
			return alipayQueryNode("probe_x", "T1", "TRADE_SUCCESS", "1.00"), true
		})
		if err := ch.(*alipayChannel).testConfig(t.Context()); err == nil {
			t.Fatal("a found trade accepted as a probe")
		}
	})
	t.Run("gateway failure fails", func(t *testing.T) {
		ch, _ := alipayChannelWithGateway(t, &alipayTestGateway{t: t, status: http.StatusBadGateway})
		if err := ch.(*alipayChannel).testConfig(t.Context()); err == nil {
			t.Fatal("gateway failure accepted")
		}
	})
}

func TestAlipayAck(t *testing.T) {
	ch := alipayOpen(t, alipayTestConfig(t, ""))
	if status, body := ch.Ack(true); status != http.StatusOK || string(body) != "success" {
		t.Fatal(status, string(body))
	}
	if status, body := ch.Ack(false); status != http.StatusInternalServerError || string(body) != "fail" {
		t.Fatal(status, string(body))
	}
}

func mustUUID(t *testing.T, text string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
