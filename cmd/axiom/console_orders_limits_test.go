package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"axiom/internal/payments"
	"axiom/internal/platform/consoleapi"
)

// TestOrderRequestsAreGuarded: the operations that change data need the site's origin, the session's CSRF token and a
// session; the request cannot carry a return address or a field the contract lacks; and a host that is no console
// reaches nothing.
func TestOrderRequestsAreGuarded(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	o := f.order(s, "guard-1", http.StatusCreated)
	noCSRF := func(r *http.Request) { r.AddCookie(s.cookie) }
	for _, c := range []struct {
		name, path string
		body       string
	}{
		{"place", "/orders", topUpBody},
		{"cancel", "/orders/" + o.Id + "/cancel", ""},
		{"verify", "/orders/" + o.Id + "/verify", ""},
	} {
		key := header("Idempotency-Key", "guard-x")
		expect(t, c.name+" without a session", f.do(http.MethodPost, c.path, c.body, key), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
		expect(t, c.name+" without the CSRF token", f.do(http.MethodPost, c.path, c.body, key, noCSRF), http.StatusForbidden, consoleapi.ErrorCodeOriginRejected)
		expect(t, c.name+" from another origin", f.do(http.MethodPost, c.path, c.body, key, as(s), header("Origin", "https://evil.example")), http.StatusForbidden, consoleapi.ErrorCodeOriginRejected)
		expect(t, c.name+" on a host that is no console", f.do(http.MethodPost, c.path, c.body, key, as(s), atHost("evil.example")), http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	}
	for _, path := range []string{"/orders/" + o.Id, "/payment-methods?credits=" + tenCredits} {
		expect(t, "read "+path+" without a session", f.do(http.MethodGet, path, ""), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	}
	for name, c := range map[string]struct{ key, body string }{
		"a return address":   {"guard-2", `{"method":"alipay","return_url":"https://evil.example/","credits":{"amount":"` + tenCredits + `","currency":"USD"}}`},
		"another currency":   {"guard-3", `{"method":"alipay","credits":{"amount":"` + tenCredits + `","currency":"EUR"}}`},
		"no credits":         {"guard-4", `{"method":"alipay","credits":{"amount":"0","currency":"USD"}}`},
		"negative credits":   {"guard-5", `{"method":"alipay","credits":{"amount":"-5","currency":"USD"}}`},
		"credits past int64": {"guard-6", `{"method":"alipay","credits":{"amount":"9999999999999999999","currency":"USD"}}`},
		"a method code":      {"guard-7", `{"method":"Ali Pay","credits":{"amount":"` + tenCredits + `","currency":"USD"}}`},
		"a key with spaces":  {"guard 8", topUpBody},
		"a key too long":     {strings.Repeat("k", 129), topUpBody},
	} {
		rec := f.createOrder(s, c.key, c.body)
		expect(t, name, rec, http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	}
	missing := f.do(http.MethodPost, "/orders", topUpBody, as(s))
	expect(t, "no Idempotency-Key", missing, http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	if n, _ := f.gateway.asked(); n != 1 {
		t.Errorf("the channel was asked %d times, only the first order was valid", n)
	}
	for _, credits := range []string{"abc", "0", "-1", "9223372036854775808", ""} {
		expect(t, "quote for "+credits, f.do(http.MethodGet, "/payment-methods?credits="+credits, "", as(s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	}
}

// TestOrdersOfAnotherAccount: a user sees and acts only on their own orders: another account's are as good as absent for
// reading, cancelling and checking, and the channel is not asked about them; each account has its own keys.
func TestOrdersOfAnotherAccount(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	owner := f.owner()
	theirs := f.order(owner, "shared-key", http.StatusCreated)
	other := f.registered(owner, "other@example.com")
	for _, rec := range []*httptest.ResponseRecorder{
		f.do(http.MethodGet, "/orders/"+theirs.Id, "", as(other)),
		f.post(other, "/orders/"+theirs.Id+"/cancel"),
		f.post(other, "/orders/"+theirs.Id+"/verify"),
		f.do(http.MethodGet, "/orders/ord_notanid", "", as(other)),
		f.do(http.MethodGet, "/orders/"+strings.Replace(theirs.Id, "ord_", "key_", 1), "", as(other)),
	} {
		expect(t, "another account's order", rec, http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	}
	if _, queries := f.gateway.asked(); queries != 0 {
		t.Errorf("the channel was asked about another account's order %d times", queries)
	}
	var kept consoleapi.Order
	decode(t, f.do(http.MethodGet, "/orders/"+theirs.Id, "", as(owner)), &kept)
	if kept.State != consoleapi.Pending {
		t.Errorf("the owner's order after the attempts: %s", kept.State)
	}
	mine := f.order(other, "shared-key", http.StatusCreated)
	if mine.Id == theirs.Id {
		t.Error("the same key of another account returned the first account's order")
	}
}

// TestOrderLimitsAnswerWithWhatToDoNext: each limit of the account and of the site is its own answer, in the contract's
// codes, and a wait is told as Retry-After.
func TestOrderLimitsAnswerWithWhatToDoNext(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps("pending_limit = 2", "cancel_limit = 1")
	s := f.owner()
	first := f.order(s, "limit-1", http.StatusCreated)
	f.order(s, "limit-2", http.StatusCreated)
	full := f.createOrder(s, "limit-3", topUpBody)
	expect(t, "a third waiting order", full, http.StatusConflict, consoleapi.ErrorCodeConflict)
	if !strings.Contains(errorOf(t, full).Message, "pay or cancel") {
		t.Errorf("the answer does not say what to do: %s", full.Body)
	}
	expect(t, "cancel", f.post(s, "/orders/"+first.Id+"/cancel"), http.StatusOK, "")
	barred := f.createOrder(s, "limit-4", topUpBody)
	expect(t, "an order after a cancellation", barred, http.StatusTooManyRequests, consoleapi.ErrorCodeRateLimited)
	if wait := retryAfter(t, barred); wait != 3600 {
		t.Errorf("Retry-After %d, want the cancellation window, 3600", wait)
	}

	f.settings("credits_min = 5000000000", "daily_credits = 12000000000", "first_credits = 20000000000")
	f.run("DELETE FROM payments.order_events", "DELETE FROM payments.orders")
	small := f.createOrder(s, "range-1", `{"method":"alipay","credits":{"amount":"1000000000","currency":"USD"}}`)
	expect(t, "below the site's smallest top-up", small, http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
	expect(t, "quote below the site's smallest top-up", f.do(http.MethodGet, "/payment-methods?credits=1000000000", "", as(s)), http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
	f.order(s, "range-2", http.StatusCreated)
	expect(t, "past the daily limit", f.createOrder(s, "range-3", topUpBody), http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
	f.settings("daily_credits = NULL")
	expect(t, "past the first top-up limit", f.createOrder(s, "range-4", `{"method":"alipay","credits":{"amount":"30000000000","currency":"USD"}}`), http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
	expect(t, "a method no merchant takes", f.createOrder(s, "range-5", `{"method":"paypal","credits":{"amount":"`+tenCredits+`","currency":"USD"}}`), http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)

	f.settings("open = false")
	closed := f.do(http.MethodGet, "/payment-methods?credits="+tenCredits, "", as(s))
	expect(t, "quote on a site that has not opened top-ups", closed, http.StatusUnprocessableEntity, consoleapi.ErrorCodeNotReady)
	expect(t, "an order on it", f.createOrder(s, "closed-1", topUpBody), http.StatusUnprocessableEntity, consoleapi.ErrorCodeNotReady)
}

// TestIPLimitsOnOrders: per site and IP group, the orders and the failed orders in the hour are limited, an IPv6 address
// counts by its /64, the answer is a 429 with the time left, and a counter that cannot be written refuses the order
// instead of letting it through.
func TestIPLimitsOnOrders(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps("pending_limit = 50", "ip_order_limit = 2", "ip_failure_limit = 2")
	s := f.owner()
	f.order(s, "ip-1", http.StatusCreated)
	f.order(s, "ip-2", http.StatusCreated)
	limited := f.createOrder(s, "ip-3", topUpBody)
	expect(t, "a third order from the IP", limited, http.StatusTooManyRequests, consoleapi.ErrorCodeRateLimited)
	if wait := retryAfter(t, limited); wait < 3590 || wait > 3600 {
		t.Errorf("Retry-After %d, want what is left of the hour", wait)
	}
	if strings.Contains(limited.Body.String(), "192.0.2.1") {
		t.Errorf("the answer repeats the address: %s", limited.Body)
	}
	f.order(s, "ip-2", http.StatusCreated) // the replay of a placed order takes nothing
	f.order(s, "ip-4", http.StatusCreated, from("198.51.100.9:4000"))

	f.settings("ip_order_limit = 1")
	f.order(s, "v6-1", http.StatusCreated, from("[2001:db8:5:6::1]:4000"))
	expect(t, "another address of the /64", f.createOrder(s, "v6-2", topUpBody, from("[2001:db8:5:6:ffff::2]:4000")), http.StatusTooManyRequests, consoleapi.ErrorCodeRateLimited)
	f.order(s, "v6-3", http.StatusCreated, from("[2001:db8:5:7::1]:4000"))

	f.settings("ip_order_limit = 50")
	f.gateway.fails(&payments.Rejected{Code: "declined", Message: "no"})
	for _, key := range []string{"fail-1", "fail-2"} {
		expect(t, "a refused order", f.createOrder(s, key, topUpBody, from("203.0.113.5:4000")), http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
	}
	f.gateway.fails(nil)
	before, _ := f.gateway.asked()
	barred := f.createOrder(s, "fail-3", topUpBody, from("203.0.113.5:4000"))
	expect(t, "an order after two failures", barred, http.StatusTooManyRequests, consoleapi.ErrorCodeRateLimited)
	if after, _ := f.gateway.asked(); after != before {
		t.Error("the channel was asked for an order the IP's failures barred")
	}
	f.run("UPDATE payments.ip_usage SET window_start = window_start - interval '2 hours'")
	f.order(s, "fail-4", http.StatusCreated, from("203.0.113.5:4000"))

	f.run(`CREATE FUNCTION payments.refuse_ip_usage() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected counting fault'; END $$;
		CREATE TRIGGER refuse_ip_usage BEFORE INSERT OR UPDATE ON payments.ip_usage
		FOR EACH ROW EXECUTE FUNCTION payments.refuse_ip_usage()`)
	count := func() string { return f.text("SELECT count(*)::text FROM payments.orders") }
	orders := count()
	asks, _ := f.gateway.asked()
	broken := f.createOrder(s, "fault-1", topUpBody, from("192.0.2.77:4000"))
	expect(t, "an order with the counting broken", broken, http.StatusInternalServerError, consoleapi.ErrorCodeInternal)
	if after, _ := f.gateway.asked(); after != asks || count() != orders {
		t.Errorf("an order was placed or sent to the channel while its counting failed")
	}
}
