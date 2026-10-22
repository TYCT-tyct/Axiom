package payments

import (
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // the fake channel replays the documented EasyPay signature format, not a new protocol
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// epaySignature adapts sub2api backend/internal/payment/provider/easypay.go, easyPaySign, 21cc92693 (2026-10-22).
// Kept in the fake channel only: the real EasyPay plugin is E1-3.
func epaySignature(v url.Values, key string) string {
	var parts []string
	for k, values := range v {
		if k != "sign" && k != "sign_type" && values[0] != "" {
			parts = append(parts, k+"="+values[0])
		}
	}
	slices.Sort(parts)
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key)) //nolint:gosec // compatibility vector for the fake channel
	return hex.EncodeToString(sum[:])
}

func signedNotice(cb Callback, key string) (Notice, error) {
	raw := cb.RawQuery
	if cb.Method == http.MethodPost {
		raw = string(cb.Body)
	}
	v, err := url.ParseQuery(raw)
	if err != nil {
		return Notice{}, err
	}
	for name, values := range v {
		if !slices.Contains([]string{"pid", "trade_no", "out_trade_no", "money", "trade_status", "sign", "sign_type"}, name) || len(values) != 1 || strings.ContainsAny(values[0], "&=") {
			return Notice{}, errors.New("fake: ambiguous callback")
		}
	}
	if v.Get("pid") != "1001" || !hmac.Equal([]byte(v.Get("sign")), []byte(epaySignature(v, key))) {
		return Notice{}, errors.New("fake: invalid signature or merchant")
	}
	n := Notice{Relevant: true, Success: v.Get("trade_status") == "TRADE_SUCCESS", Order: v.Get("out_trade_no"), TradeNo: v.Get("trade_no")}
	n.Paid.Currency, err = platform.ParseCurrency("CNY")
	if err != nil {
		return Notice{}, err
	}
	value, ok := new(big.Rat).SetString(v.Get("money"))
	if !ok {
		return Notice{}, errors.New("fake: invalid money")
	}
	value.Mul(value, big.NewRat(100, 1))
	if !value.IsInt() || !value.Num().IsInt64() {
		n.Paid.Minor = -1 // preserve a verified but unrepresentable amount for the core's discrepancy path
	} else {
		n.Paid.Minor = value.Num().Int64()
	}
	return n, nil
}

func callbackValues(o Order) url.Values {
	v := url.Values{"pid": {"1001"}, "trade_no": {o.TradeNo}, "out_trade_no": {o.ID.TypeID()},
		"money": {fmt.Sprintf("%d.%02d", o.Total.Minor/100, o.Total.Minor%100)}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"MD5"}}
	v.Set("sign", epaySignature(v, theKey))
	return v
}

func (w *world) callback(merchant MerchantID, method string, v url.Values) *httptest.ResponseRecorder {
	w.t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /api/payments/v1/notify/{merchant}", w.p.Notify())
	mux.Handle("POST /api/payments/v1/notify/{merchant}", w.p.Notify())
	path := "/api/payments/v1/notify/" + merchant.TypeID()
	var r *http.Request
	if method == http.MethodGet {
		r = httptest.NewRequest(method, path+"?"+v.Encode(), nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r = r.WithContext(w.t.Context())
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, r)
	return rw
}

func receipt(o Order) confirmed {
	return confirmed{merchant: o.MerchantID, order: o.ID.TypeID(), source: "query", payment: Payment{State: PayPaid, TradeNo: o.TradeNo, Paid: o.Total}}
}

func (w *world) receive(c confirmed) {
	w.t.Helper()
	if err := w.p.receive(w.t.Context(), c); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) assertCredit(o Order, want int64) {
	w.t.Helper()
	var got, balance, ledger int64
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(w.t.Context(), `SELECT coalesce((SELECT amount FROM billing.credit_batches WHERE id = $1), 0), b.balance,
			coalesce((SELECT -sum(amount) FROM billing.entries WHERE account_id = $2 AND book = 'user_balance'), 0)::bigint
			FROM billing.balances b WHERE b.account_id = $2`, uuid.UUID(o.ID), o.AccountID).Scan(&got, &balance, &ledger)
	}); err != nil {
		w.t.Fatal(err)
	}
	if got != want {
		w.t.Fatalf("credited %d, want %d", got, want)
	}
	if balance != ledger {
		w.t.Fatalf("balance %d differs from ledger %d", balance, ledger)
	}
}

func TestCallbacksCommitOnceAndReplayWithoutQuery(t *testing.T) {
	var calls atomic.Int64
	var o Order
	w := newTestWorld(t, fakePlugin{create: goodCheckout, verify: signedNotice, query: func(context.Context, string, string) (Payment, error) {
		calls.Add(1)
		return receipt(o).payment, nil
	}})
	w.waiting()
	o = w.mustPlace(10*yuan, "callback")
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodGet} {
		r := w.callback(o.MerchantID, method, callbackValues(o))
		if r.Code != 200 || r.Body.String() != "success" {
			t.Fatalf("callback %d %s", r.Code, r.Body)
		}
	}
	w.assertCredit(o, o.Credits)
	if calls.Load() != 1 || w.count(w.p.platform, "SELECT count(*) FROM payments.order_payments") != 1 {
		t.Fatal("a replay queried or recorded a second payment")
	}
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatal(state)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM (SELECT transaction_id FROM billing.entries GROUP BY transaction_id, currency HAVING sum(amount) <> 0) x") != 0 {
		t.Fatal("unbalanced ledger")
	}
}

func TestUnsignedOrAmbiguousCallbackWritesNothing(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout, verify: signedNotice})
	w.waiting()
	o := w.mustPlace(yuan, "bad-signature")
	events := w.events(o.ID)
	for _, mutate := range []func(url.Values){
		func(v url.Values) { v.Del("sign") },
		func(v url.Values) { v.Set("sign", "wrong") },
		func(v url.Values) { v.Set("money", "99.00") },
		func(v url.Values) { v.Set("pid", "1002"); v.Set("sign", epaySignature(v, theKey)) },
		func(v url.Values) { v.Set("extra", "value"); v.Set("sign", epaySignature(v, theKey)) },
		func(v url.Values) { v.Add("money", "1.00") },
	} {
		v := callbackValues(o)
		mutate(v)
		if r := w.callback(o.MerchantID, http.MethodGet, v); r.Code != 400 {
			t.Fatalf("invalid signature: %d %s", r.Code, r.Body)
		}
	}
	if w.events(o.ID) != events || w.count(w.p.platform, "SELECT count(*) FROM payments.order_payments") != 0 || w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 0 {
		t.Fatal("unverified callback wrote data")
	}
	w.assertCredit(o, 0)
}

func TestCallbackQueryFailureAndMismatch(t *testing.T) {
	for _, failure := range []bool{true, false} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			var o Order
			w := newTestWorld(t, fakePlugin{create: goodCheckout, verify: signedNotice, query: func(context.Context, string, string) (Payment, error) {
				if failure {
					return Payment{}, errors.New("fake timeout")
				}
				p := receipt(o).payment
				p.Paid.Minor++
				return p, nil
			}})
			w.waiting()
			o = w.mustPlace(yuan, "confirm")
			r := w.callback(o.MerchantID, http.MethodPost, callbackValues(o))
			if failure && r.Code != 502 || !failure && r.Code != 200 {
				t.Fatalf("callback: %d %s", r.Code, r.Body)
			}
			w.assertCredit(o, 0)
			want := 1
			if failure {
				want = 0
			}
			if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != want {
				t.Fatal("query failure or mismatch was dropped")
			}
		})
	}
}

func TestPaymentConflictsAreRecordedAndAcknowledged(t *testing.T) {
	for _, scenario := range []string{"unknown", "wrong merchant", "trade conflict", "multiple", "one cent less", "one cent more", "currency", "zero"} {
		t.Run(scenario, func(t *testing.T) {
			w := newTestWorld(t, fakePlugin{create: goodCheckout})
			w.waiting()
			o := w.mustPlace(yuan, "target")
			c := receipt(o)
			switch scenario {
			case "unknown":
				c.order = "external-order"
			case "wrong merchant":
				m := w.mustSave(siteScope(w.siteB), w.input(w.siteB))
				c.merchant = MerchantID(m.ID)
			case "trade conflict":
				other := w.mustPlace(yuan, "other")
				w.receive(receipt(other))
				c.payment.TradeNo = other.TradeNo
			case "multiple":
				w.receive(c)
				c.payment.TradeNo = "second-payment"
			case "one cent less":
				c.payment.Paid.Minor--
			case "one cent more":
				c.payment.Paid.Minor++
			case "currency":
				c.payment.Paid.Currency = currency(t, "USD")
			case "zero":
				c.payment.Paid.Minor = 0
			}
			w.receive(c)
			w.receive(c)
			if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 1 {
				t.Fatal("discrepancy missing or duplicated")
			}
			want := int64(0)
			if scenario == "multiple" {
				want = o.Credits
			}
			w.assertCredit(o, want)
		})
	}
}

func TestPaymentAndCreditRollbackTogether(t *testing.T) {
	for _, phase := range []string{"billing", "commit"} {
		t.Run(phase, func(t *testing.T) {
			var o Order
			w := newTestWorld(t, fakePlugin{create: goodCheckout, verify: signedNotice, query: func(context.Context, string, string) (Payment, error) { return receipt(o).payment, nil }})
			w.waiting()
			o = w.mustPlace(yuan, "rollback")
			events := w.events(o.ID)
			w.exec(`CREATE FUNCTION payments.reject_paid() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'injected payment failure'; END $$`)
			if phase == "billing" {
				w.exec(`CREATE TRIGGER reject_paid BEFORE INSERT ON billing.credit_batches FOR EACH ROW EXECUTE FUNCTION payments.reject_paid()`)
			} else {
				w.exec(`CREATE CONSTRAINT TRIGGER reject_paid AFTER INSERT ON payments.order_payments DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION payments.reject_paid()`)
			}
			if r := w.callback(o.MerchantID, http.MethodGet, callbackValues(o)); r.Code < 300 {
				t.Fatalf("acknowledged a failed %s: %d", phase, r.Code)
			}
			w.assertCredit(o, 0)
			if state, _, _ := w.stored(o.ID); state != "pending" || w.events(o.ID) != events || w.count(w.p.platform, "SELECT count(*) FROM payments.order_payments") != 0 {
				t.Fatal("partial receipt survived rollback")
			}
			table := "payments.order_payments"
			if phase == "billing" {
				table = "billing.credit_batches"
			}
			w.exec("DROP TRIGGER reject_paid ON " + table)
			if r := w.callback(o.MerchantID, http.MethodGet, callbackValues(o)); r.Code != 200 {
				t.Fatal(r.Code, r.Body)
			}
			w.assertCredit(o, o.Credits)
		})
	}
}

func TestCallbackPollAndVerifyRace(t *testing.T) {
	var mu sync.RWMutex
	var o Order
	entered, release := make(chan struct{}, 3), make(chan struct{})
	w := newTestWorld(t, fakePlugin{create: goodCheckout, verify: signedNotice, query: func(ctx context.Context, _, _ string) (Payment, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return Payment{}, ctx.Err()
		}
		mu.RLock()
		defer mu.RUnlock()
		return receipt(o).payment, nil
	}})
	w.waiting()
	mu.Lock()
	o = w.mustPlace(yuan, "race")
	mu.Unlock()
	w.exec("UPDATE payments.orders SET next_query_at = transaction_timestamp() - interval '1 minute'")
	done := make(chan error, 3)
	go func() { _, err := w.p.Verify(t.Context(), w.accountA(), o.ID); done <- err }()
	go func() { done <- w.p.queryPending(t.Context()) }()
	go func() {
		r := w.callback(o.MerchantID, http.MethodGet, callbackValues(o))
		if r.Code != 200 {
			done <- fmt.Errorf("callback %d", r.Code)
			return
		}
		done <- nil
	}()
	for range 3 {
		<-entered
	}
	close(release)
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	w.assertCredit(o, o.Credits)
	if w.count(w.p.platform, "SELECT count(*) FROM payments.order_payments") != 1 || w.count(w.p.platform, "SELECT count(*) FROM billing.transactions WHERE kind = 'credit'") != 1 {
		t.Fatal("concurrent credit was not unique")
	}
}
