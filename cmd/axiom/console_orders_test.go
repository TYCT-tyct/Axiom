package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// fakeGateway is the payment channel behind the console tests: it hands out a checkout for every order unless a test
// makes it fail, remembers what it was asked, and reports an order paid when a test says so.
type fakeGateway struct {
	mu      sync.Mutex
	failure error                        // what Create answers instead of a checkout
	during  func(payments.CreateRequest) // runs while Create is called, after the order is committed and before it answers
	created []payments.CreateRequest
	queried int
	paid    map[string]payments.Money // by order number
}

func (g *fakeGateway) create(r payments.CreateRequest) (payments.Checkout, error) {
	g.mu.Lock()
	during := g.during
	g.mu.Unlock()
	if during != nil {
		during(r)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.created = append(g.created, r)
	if g.failure != nil {
		return payments.Checkout{}, g.failure
	}
	n := r.Order.TypeID()
	return payments.Checkout{TradeNo: "T" + n, PayURL: "https://pay.gateway.example/" + n, ClientSecret: "cs_test"}, nil
}

func (g *fakeGateway) query(order string) payments.Payment {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.queried++
	if total, ok := g.paid[order]; ok {
		return payments.Payment{State: payments.PayPaid, TradeNo: "T" + order, Paid: total}
	}
	return payments.Payment{State: payments.PayPending}
}

// meanwhile runs f while the channel is called for an order, as another request of the same account would.
func (g *fakeGateway) meanwhile(f func(payments.CreateRequest)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.during = f
}

// fails makes the channel answer every order with err; nil makes it answer well again.
func (g *fakeGateway) fails(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failure = err
}

// pays makes the channel report the order paid, with total in the currency it was ordered in.
func (g *fakeGateway) pays(order string, total payments.Money) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paid == nil {
		g.paid = map[string]payments.Money{}
	}
	g.paid[order] = total
}

func (g *fakeGateway) asked() (creates, queries int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.created), g.queried
}

type gatewayPlugin struct{ g *fakeGateway }

func (gatewayPlugin) Kind() payments.Kind { return "testgw" }
func (p gatewayPlugin) Open(payments.MerchantConfig) (payments.Channel, error) {
	return gatewayChannel(p), nil
}

type gatewayChannel struct{ g *fakeGateway }

func (gatewayChannel) Caps() payments.Caps        { return payments.CapQuery }
func (gatewayChannel) Methods() []payments.Method { return []payments.Method{"alipay"} }
func (c gatewayChannel) Create(_ context.Context, r payments.CreateRequest) (payments.Checkout, error) {
	return c.g.create(r)
}

func (gatewayChannel) Verify(context.Context, payments.Callback) (payments.Notice, error) {
	return payments.Notice{}, errors.New("testgw: callbacks are not signed")
}

func (c gatewayChannel) Query(_ context.Context, order, _ string) (payments.Payment, error) {
	return c.g.query(order), nil
}
func (gatewayChannel) Close(context.Context, payments.OrderID, string) error { return nil }
func (gatewayChannel) Refund(context.Context, payments.RefundCall) (payments.RefundState, error) {
	panic("testgw: refund is not declared")
}

func (gatewayChannel) QueryRefund(context.Context, payments.RefundCall) (payments.RefundState, error) {
	panic("testgw: refund is not declared")
}
func (gatewayChannel) Ack(bool) (int, []byte) { return http.StatusOK, []byte("success") }

const (
	tenCredits = "10000000000" // billionths: ten units of the site's currency
	topUpBody  = `{"method":"alipay","credits":{"amount":"` + tenCredits + `","currency":"USD"}}`
)

// openTopUps opens top-ups on the site with one merchant account taking alipay in the site's own currency, and then
// applies the settings (SQL assignments to payments.settings).
func (f consoleFixture) openTopUps(settings ...string) {
	f.t.Helper()
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err = f.pay.SaveMerchant(f.t.Context(), pg.PlatformScope(), audit.System("test"), payments.MerchantInput{
		Site: uuid.UUID(f.siteID()), Kind: "testgw", Name: "Gateway", Enabled: true, Currency: usd, TTLSeconds: 1800,
		Settings: []byte(`{}`), Secrets: map[string]secret.Secret{"key": secret.New("gateway-key")},
		Methods: []payments.MethodInput{{Method: "alipay", Upstream: "alipay", Fee: payments.Fee{PPM: 30_000}, Enabled: true}},
	}, "test save"); err != nil {
		f.t.Fatal(err)
	}
	f.run(fmt.Sprintf("INSERT INTO payments.settings (site_id, open) VALUES ('%s', true)", uuid.UUID(f.siteID())))
	f.settings(settings...)
}

// settings changes the site's payment settings: assignments such as "pending_limit = 2".
func (f consoleFixture) settings(assignments ...string) {
	f.t.Helper()
	for _, a := range assignments {
		f.run("UPDATE payments.settings SET " + a)
	}
}

func from(remote string) mod { return func(r *http.Request) { r.RemoteAddr = remote } }

func (f consoleFixture) createOrder(s session, key, body string, mods ...mod) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(http.MethodPost, "/orders", body, append([]mod{as(s), header("Idempotency-Key", key)}, mods...)...)
}

// order places the ten-credit order and returns the order the response says, whose status must be want.
func (f consoleFixture) order(s session, key string, want int, mods ...mod) consoleapi.Order {
	f.t.Helper()
	rec := f.createOrder(s, key, topUpBody, mods...)
	expect(f.t, "createOrder", rec, want, "")
	var o consoleapi.Order
	if rec.Code < 300 {
		decode(f.t, rec, &o)
	}
	return o
}

func (f consoleFixture) post(s session, path string) *httptest.ResponseRecorder {
	return f.do(http.MethodPost, path, "", as(s))
}

// registered is a second user of the site, signed in.
func (f consoleFixture) registered(owner session, email string) session {
	f.t.Helper()
	var info consoleapi.Session
	decode(f.t, f.do(http.MethodGet, "/session", "", as(owner)), &info)
	expect(f.t, "register", f.do(http.MethodPost, "/auth/register",
		`{"invite_code":"`+info.User.InviteCode+`","email":"`+email+`","password":"another horse battery"}`), http.StatusCreated, "")
	signedIn := f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+email+`","password":"another horse battery"}`)
	expect(f.t, "sign in", signedIn, http.StatusOK, "")
	return f.sessionOf(signedIn)
}

func retryAfter(t testing.TB, rec *httptest.ResponseRecorder) int {
	t.Helper()
	n, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After %q of %d %s", rec.Header().Get("Retry-After"), rec.Code, rec.Body)
	}
	return n
}

func TestReturnURL(t *testing.T) {
	t.Parallel()
	for origin, want := range map[string]string{
		"https://console.example.com":      "https://console.example.com/console/balance",
		"https://console.example.com:8443": "https://console.example.com:8443/console/balance",
		"http://localhost:317":             "",
		"console.example.com":              "",
		"":                                 "",
	} {
		got, err := returnURL(origin)
		if got != want || (err != nil) != (want == "") {
			t.Errorf("returnURL(%q) = %q, %v; want %q", origin, got, err, want)
		}
	}
}

// TestARefusalOutranksTheCauseItWraps: an order refused because its merchant account could not be opened wraps a
// merchant that was not found, and is still the refusal of the order, not a 404.
func TestARefusalOutranksTheCauseItWraps(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("%w: open the channel before creating an order: %w", payments.ErrRefused, payments.ErrNotFound)
	if got := failure(err); got == nil || got.status != http.StatusUnprocessableEntity || got.code != codeUnprocessable {
		t.Errorf("a refused order whose merchant is gone: %+v", got)
	}
	if got := failure(payments.ErrNotFound); got == nil || got.status != http.StatusNotFound {
		t.Errorf("an order that is not found: %+v", got)
	}
}
