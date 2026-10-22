package payments

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"
)

func TestFakeChannelSignatureVector(t *testing.T) {
	// Fields and key from sub2api easypay_sign_test.go, TestEasyPaySignConsistentOutput, 21cc92693 (2026-10-22).
	v := url.Values{"pid": {"1001"}, "type": {"alipay"}, "out_trade_no": {"ORDER123"}, "name": {"Test Product"}, "money": {"10.00"}}
	const want = "4a5e4a489ee20c0ff8ef42be7c9effee"
	if got := epaySignature(v, "test_secret_key"); got != want {
		t.Fatalf("signature %s, want %s", got, want)
	}
	v.Set("sign", "ignored")
	v.Set("sign_type", "MD5")
	v.Set("empty", "")
	if got := epaySignature(v, "test_secret_key"); got != want {
		t.Fatal("signature included excluded parameters")
	}
}

func TestCallbackAcknowledgesUnmatchedAndDoesNotCreditWrongMerchant(t *testing.T) {
	for _, wrongMerchant := range []bool{false, true} {
		t.Run(fmt.Sprint(wrongMerchant), func(t *testing.T) {
			var o Order
			w := newTestWorld(t, fakePlugin{create: goodCheckout, verify: signedNotice, query: func(context.Context, string, string) (Payment, error) { return receipt(o).payment, nil }})
			w.waiting()
			o = w.mustPlace(yuan, "unmatched")
			merchant := o.MerchantID
			v := callbackValues(o)
			if wrongMerchant {
				m := w.mustSave(siteScope(w.siteB), w.input(w.siteB))
				merchant = MerchantID(m.ID)
			} else {
				v.Set("out_trade_no", "foreign-order")
				v.Set("sign", epaySignature(v, theKey))
			}
			for range 2 {
				if r := w.callback(merchant, http.MethodGet, v); r.Code != 200 {
					t.Fatalf("unknown order kept gateway retrying: %d %s", r.Code, r.Body)
				}
			}
			w.assertCredit(o, 0)
			if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 1 || w.count(w.p.platform, "SELECT count(*) FROM payments.order_payments") != 0 {
				t.Fatal("unmatched payment was lost or credited")
			}
		})
	}
}

func TestCallbackWithoutQueryAndReorderedFailure(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout, verify: signedNotice, noQuery: true})
	w.waiting()
	o := w.mustPlace(yuan, "no-query")
	if r := w.callback(o.MerchantID, http.MethodPost, callbackValues(o)); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	v := callbackValues(o)
	v.Set("trade_status", "TRADE_CLOSED")
	v.Set("sign", epaySignature(v, theKey))
	if r := w.callback(o.MerchantID, http.MethodGet, v); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	w.assertCredit(o, o.Credits)
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatal("late closed notice reversed a payment")
	}
	if w.count(w.accountB(), "SELECT count(*) FROM payments.order_payments") != 0 {
		t.Fatal("payment leaked to another account")
	}
}

func TestCallbackBodyBoundAndIngressLimit(t *testing.T) {
	var verified atomic.Int64
	w := newTestWorld(t, fakePlugin{verify: func(Callback, string) (Notice, error) { verified.Add(1); return Notice{}, nil }})
	m := w.waiting()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", callbackMax+1)))
	r.SetPathValue("merchant", MerchantID(m.ID).TypeID())
	rw := httptest.NewRecorder()
	w.p.Notify().ServeHTTP(rw, r)
	if rw.Code != 400 || verified.Load() != 0 {
		t.Fatal("oversize callback reached signature verification")
	}
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	ip := netip.MustParseAddr("203.0.113.1")
	for i := range 121 {
		if got := w.p.allowCallback(MerchantID(m.ID), ip, now); got != (i < 120) {
			t.Fatalf("callback %d allowed=%v", i, got)
		}
	}
	if !w.p.allowCallback(MerchantID(m.ID), ip, now.Add(time.Minute)) || !w.p.allowCallback(MerchantID(uuid.NewV7()), ip, now) {
		t.Fatal("callback window or merchant isolation failed")
	}
}

func TestPollCreditsBeforeExpiryAndWhenCallbackIsLost(t *testing.T) {
	var o Order
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) { return receipt(o).payment, nil }})
	m := w.waiting()
	o = w.mustPlace(yuan, "poll-paid")
	w.exec("UPDATE payments.orders SET expires_at = transaction_timestamp() - interval '1 minute'")
	if err := w.p.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.assertCredit(o, o.Credits)
	if state, _, _ := w.stored(o.ID); state != "paid" || w.used(m.ID) != o.Total.Minor {
		t.Fatal("paid order expired or released its usage")
	}
}
