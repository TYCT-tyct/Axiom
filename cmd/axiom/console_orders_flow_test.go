package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
)

// read is the order as the account reads it now.
func (f consoleFixture) read(s session, id string) consoleapi.Order {
	f.t.Helper()
	var o consoleapi.Order
	decode(f.t, f.do(http.MethodGet, "/orders/"+id, "", as(s)), &o)
	return o
}

// TestPaymentMethodsQuote: the price per method, in the currency it is paid in.
func TestPaymentMethodsQuote(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	rec := f.do(http.MethodGet, "/payment-methods?credits="+tenCredits, "", as(f.owner()))
	expect(t, "listPaymentMethods", rec, http.StatusOK, "")
	var methods consoleapi.PaymentMethodList
	decode(t, rec, &methods)
	if len(methods.Data) != 1 || methods.Data[0].Method != "alipay" || methods.Data[0].Total.Currency != "USD" || methods.Data[0].Fee.Amount == "0" {
		t.Fatalf("methods: %+v", methods)
	}
}

// pageWithSecret is a checkout that sends the user to a link, with the client secret the channel gave.
func pageWithSecret(c *consoleapi.Checkout) bool {
	return c != nil && c.PayUrl != nil && c.ClientSecret != nil && *c.ClientSecret == "cs_test"
}

// TestTopUpOrder: an order with the page to pay on and the address the server built for the browser to come back to; it
// reads back without the client secret, which is never stored.
func TestTopUpOrder(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	o := f.order(s, "flow-1", http.StatusCreated)
	if o.State != consoleapi.Pending || o.Credits.Amount != tenCredits || o.Credits.Currency != "USD" || !pageWithSecret(o.Checkout) {
		t.Fatalf("the new order: %+v", o)
	}
	if got := f.gateway.created[0]; got.ReturnURL != consoleOrigin+"/console/balance" || got.ClientIP.String() != "192.0.2.1" {
		t.Errorf("the channel was told to return to %q for %s", got.ReturnURL, got.ClientIP)
	}
	seen := f.read(s, o.Id)
	if seen.Id != o.Id || seen.State != consoleapi.Pending || seen.Checkout == nil || seen.Checkout.ClientSecret != nil {
		t.Errorf("the order read back: %+v", seen)
	}
}

// TestTopUpVerifyCreditsTheBalance: "I have paid" finds nothing before the payment and is limited to one a minute, then
// finds the payment and credits the balance, once.
func TestTopUpVerifyCreditsTheBalance(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	o := f.order(s, "verify-1", http.StatusCreated)

	unpaid := f.post(s, "/orders/"+o.Id+"/verify")
	expect(t, "verify before the payment", unpaid, http.StatusOK, "")
	var seen consoleapi.Order
	decode(t, unpaid, &seen)
	if seen.State != consoleapi.Pending {
		t.Errorf("an order the channel does not know as paid is %s", seen.State)
	}
	again := f.post(s, "/orders/"+o.Id+"/verify")
	expect(t, "verify twice in a minute", again, http.StatusTooManyRequests, consoleapi.ErrorCodeRateLimited)
	if wait := retryAfter(t, again); wait != 60 {
		t.Errorf("Retry-After %d, want 60", wait)
	}

	total, err := strconv.ParseInt(o.Total.Amount, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	usd, err := platform.ParseCurrency(o.Total.Currency)
	if err != nil {
		t.Fatal(err)
	}
	f.gateway.pays(o.Id, payments.Money{Currency: usd, Minor: total})
	f.run("UPDATE payments.orders SET next_verify_at = transaction_timestamp() - interval '1 minute'")
	paid := f.post(s, "/orders/"+o.Id+"/verify")
	expect(t, "verify after the payment", paid, http.StatusOK, "")
	var after consoleapi.Order // not seen: decoding into a value keeps a field the answer leaves out
	decode(t, paid, &after)
	if after.State != consoleapi.Paid || after.Checkout != nil {
		t.Errorf("after the payment: %+v", after)
	}
	if got := f.balance(s).Balance.Amount; got != tenCredits {
		t.Errorf("the balance after the top-up: %s, want %s", got, tenCredits)
	}
	expect(t, "cancel a paid order", f.post(s, "/orders/"+o.Id+"/cancel"), http.StatusConflict, consoleapi.ErrorCodeConflict)
}

// receiptFacts reports whether the order carries what its receipt renders.
func receiptFacts(o consoleapi.Order) bool {
	return o.PaidAt != nil && o.PaidTotal != nil && o.Credited != nil
}

// TestReceiptFacts: a paid order carries what a receipt renders — when it was paid, what the channel took and what was
// credited — while a waiting one carries none of it, and another account's read is a 404 like one that does not exist.
// The receipt page shows these facts and its own refunds, never a second record (docs/domains/payments.md「收据与发票」).
func TestReceiptFacts(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	o := f.order(s, "receipt-1", http.StatusCreated)
	if waiting := f.read(s, o.Id); receiptFacts(waiting) {
		t.Errorf("an unpaid order carries receipt facts: %+v", waiting)
	}
	total, err := strconv.ParseInt(o.Total.Amount, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	currency, err := platform.ParseCurrency(o.Total.Currency)
	if err != nil {
		t.Fatal(err)
	}
	f.gateway.pays(o.Id, payments.Money{Currency: currency, Minor: total})
	f.run("UPDATE payments.orders SET next_verify_at = transaction_timestamp() - interval '1 minute'")
	expect(t, "verify", f.post(s, "/orders/"+o.Id+"/verify"), http.StatusOK, "")

	paid := f.read(s, o.Id)
	if paid.State != consoleapi.Paid || !receiptFacts(paid) {
		t.Fatalf("a paid order without its receipt facts: %+v", paid)
	}
	if paid.PaidTotal.Amount != o.Total.Amount || paid.PaidTotal.Currency != o.Total.Currency {
		t.Errorf("the receipt says %s %s was paid, the quote said %s %s",
			paid.PaidTotal.Amount, paid.PaidTotal.Currency, o.Total.Amount, o.Total.Currency)
	}
	if paid.Credited.Amount != tenCredits || paid.Credited.Currency != "USD" {
		t.Errorf("the receipt credits %s %s, want %s USD", paid.Credited.Amount, paid.Credited.Currency, tenCredits)
	}
	other := f.registered(s, "receipt-other@example.com")
	expect(t, "another account's receipt", f.do(http.MethodGet, "/orders/"+o.Id, "", as(other)),
		http.StatusNotFound, consoleapi.ErrorCodeNotFound)
}

// TestReplayOfAWaitingOrder: the same key returns the same order with its page and asks the channel once; the same key
// for another amount is a conflict; once the order is cancelled the replay says so and has nothing to pay on.
func TestReplayOfAWaitingOrder(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	first := f.order(s, "replay-1", http.StatusCreated)
	replay := f.order(s, "replay-1", http.StatusCreated)
	if replay.Id != first.Id || replay.Checkout == nil || replay.Checkout.PayUrl == nil || replay.Checkout.ClientSecret != nil {
		t.Errorf("a replay: %+v, first %+v", replay, first)
	}
	if n, _ := f.gateway.asked(); n != 1 {
		t.Errorf("the channel was asked %d times for one order", n)
	}
	other := f.createOrder(s, "replay-1", `{"method":"alipay","credits":{"amount":"20000000000","currency":"USD"}}`)
	expect(t, "the key for another amount", other, http.StatusConflict, consoleapi.ErrorCodeIdempotencyKeyReused)

	expect(t, "cancel", f.post(s, "/orders/"+first.Id+"/cancel"), http.StatusOK, "")
	cancelled := f.order(s, "replay-1", http.StatusOK)
	if cancelled.State != consoleapi.Cancelled || cancelled.Checkout != nil {
		t.Errorf("a replay after the cancellation: %+v", cancelled)
	}
}

// TestReplayOfARefusedOrder: an order the channel refused is the refusal again for its key, without asking the channel
// again, and without what the merchant's gateway said.
func TestReplayOfARefusedOrder(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	f.gateway.fails(&payments.Rejected{Code: "declined", Message: "SECRET-GATEWAY-DETAIL merchant 42"})
	refused := f.createOrder(s, "refused-1", topUpBody)
	expect(t, "an order the channel refuses", refused, http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
	before, _ := f.gateway.asked()
	replayed := f.createOrder(s, "refused-1", topUpBody)
	expect(t, "its replay", replayed, http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
	if after, _ := f.gateway.asked(); after != before {
		t.Error("a replay of a refused order asked the channel again")
	}
	for _, rec := range []*httptest.ResponseRecorder{refused, replayed} {
		if strings.Contains(rec.Body.String(), "SECRET-GATEWAY-DETAIL") || strings.Contains(rec.Body.String(), "mch_") {
			t.Errorf("the merchant's detail is in the answer: %s", rec.Body)
		}
	}
}

// TestReplayOfAnUnconfirmedOrder: an order the channel did not confirm is a waiting order with nothing to pay on, for
// the request that made it, for its replay and for a read, and it can be cancelled.
func TestReplayOfAnUnconfirmedOrder(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	f.gateway.fails(errors.New("connection reset"))
	unsure := f.order(s, "unsure-1", http.StatusAccepted)
	if unsure.State != consoleapi.Pending || unsure.Checkout != nil {
		t.Errorf("an order the channel did not confirm: %+v", unsure)
	}
	f.gateway.fails(nil)
	again := f.order(s, "unsure-1", http.StatusAccepted)
	if read := f.read(s, unsure.Id); again.Id != unsure.Id || again.Checkout != nil || read.Checkout != nil {
		t.Errorf("its replay %+v and the order read back %+v show something to pay on", again, read)
	}
	expect(t, "cancel the unconfirmed order", f.post(s, "/orders/"+unsure.Id+"/cancel"), http.StatusOK, "")
}

// TestOrdersWithOneKeyAtOnce: a burst of the same request places one order and asks the channel once; each answer is the
// order, with its page or as not confirmed yet.
func TestOrdersWithOneKeyAtOnce(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	recs := make([]*httptest.ResponseRecorder, 8)
	var wg sync.WaitGroup
	for i := range recs {
		wg.Go(func() { recs[i] = f.createOrder(s, "burst", topUpBody) })
	}
	wg.Wait()
	ids, created := map[string]bool{}, 0
	for _, rec := range recs {
		if rec.Code != http.StatusCreated && rec.Code != http.StatusAccepted {
			t.Fatalf("a request of the burst: %d %s", rec.Code, rec.Body)
		}
		if rec.Code == http.StatusCreated {
			created++
		}
		var o consoleapi.Order
		decode(t, rec, &o)
		ids[o.Id] = true
	}
	if n, _ := f.gateway.asked(); len(ids) != 1 || n != 1 || created == 0 {
		t.Errorf("%d orders, %d asks of the channel, %d answers with a page", len(ids), n, created)
	}
}
