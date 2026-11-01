package payments

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"

	"github.com/stripe/stripe-go/v85"
)

func stripeTestServer(t *testing.T, handle http.HandlerFunc) (*stripeChannel, *[]*http.Request) {
	t.Helper()
	requests := new([]*http.Request)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.Clone(r.Context()))
		handle(w, r)
	}))
	t.Cleanup(server.Close)
	old := stripeAPIBase
	stripeAPIBase = server.URL
	t.Cleanup(func() { stripeAPIBase = old })
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	t.Cleanup(client.CloseIdleConnections)
	return &stripeChannel{
		currency: currency(t, "USD"), client: newStripeClient("sk_test_123", client),
		webhook: secret.New("whsec_test"), oldWebhook: secret.New("whsec_old"),
	}, requests
}

func writeStripeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	raw, err := jsonx.MarshalCanonical(v)
	if err != nil {
		t.Error(err)
		return
	}
	if _, err := w.Write(raw); err != nil {
		t.Error(err)
	}
}

func stripeSession(id, status, paymentStatus string) map[string]any {
	return map[string]any{
		"id": id, "object": "checkout.session", "status": status, "payment_status": paymentStatus,
		"amount_total": 1234, "currency": "usd", "payment_intent": map[string]any{"id": "pi_1"},
		"client_reference_id": "ord_1", "metadata": map[string]string{"order": "ord_1"},
	}
}

//nolint:cyclop // the test checks the request form, idempotency and expiry in one scenario
func TestStripeCreateCheckout(t *testing.T) {
	var form url.Values
	var idempotency, method string
	c, requests := stripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkout/sessions" {
			http.NotFound(w, r)
			return
		}
		method = r.Method
		idempotency = r.Header.Get("Idempotency-Key")
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form = r.PostForm
		session := stripeSession("cs_1", "open", "unpaid")
		session["url"] = "https://checkout.stripe.com/c/pay/cs_1"
		session["expires_at"] = time.Now().Add(time.Hour).Unix()
		writeStripeJSON(t, w, session)
	})
	order, merchant := OrderID(uuid.NewV7()), MerchantID(uuid.NewV7())
	before := time.Now()
	got, err := c.Create(t.Context(), CreateRequest{
		Order: order, Merchant: merchant, Method: "alipay", Upstream: "alipay",
		Total: Money{Currency: currency(t, "USD"), Minor: 1234}, Subject: "Top-up",
		ReturnURL: "https://console.example.com/pay/return", ExpiresAt: time.Now().Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 1 || method != http.MethodPost || idempotency != "cs_"+order.TypeID() {
		t.Fatalf("request method=%s idempotency=%q requests=%d", method, idempotency, len(*requests))
	}
	if got.TradeNo != "cs_1" || got.PayURL == "" || got.ClientSecret != "" {
		t.Fatalf("checkout = %+v", got)
	}
	for key, want := range map[string]string{
		"mode": "payment", "client_reference_id": order.TypeID(),
		"metadata[order]": order.TypeID(), "metadata[merchant]": merchant.TypeID(),
		"payment_method_types[0]": "alipay", "line_items[0][price_data][currency]": "usd",
		"line_items[0][price_data][unit_amount]": "1234", "line_items[0][quantity]": "1",
		"success_url": "https://console.example.com/pay/return", "cancel_url": "https://console.example.com/pay/return",
		"payment_intent_data[metadata][order]": order.TypeID(),
	} {
		if form.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, form.Get(key), want)
		}
	}
	expires, err := strconv.ParseInt(form.Get("expires_at"), 10, 64)
	if err != nil || expires < before.Add(30*time.Minute).Unix() || expires > before.Add(24*time.Hour).Unix() {
		t.Fatalf("expires_at = %q, err=%v", form.Get("expires_at"), err)
	}
}

func TestStripeCreateRejectsUnsafeInputs(t *testing.T) {
	c, requests := stripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	})
	usd := currency(t, "USD")
	for _, tc := range []struct {
		name string
		in   CreateRequest
	}{
		{"zero amount", CreateRequest{Order: OrderID(uuid.NewV7()), Total: Money{Currency: usd}, ReturnURL: "https://example.com"}},
		{"wrong currency", CreateRequest{Order: OrderID(uuid.NewV7()), Total: Money{Currency: currency(t, "CNY"), Minor: 1}, ReturnURL: "https://example.com"}},
		{"http return", CreateRequest{Order: OrderID(uuid.NewV7()), Total: Money{Currency: usd, Minor: 1}, ReturnURL: "http://example.com"}},
		{"unknown method", CreateRequest{Order: OrderID(uuid.NewV7()), Upstream: "paypal", Total: Money{Currency: usd, Minor: 1}, ReturnURL: "https://example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Create(t.Context(), tc.in); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if len(*requests) != 0 {
		t.Fatalf("rejected inputs reached Stripe: %d requests", len(*requests))
	}
}

//nolint:cyclop // the test walks the session states and probe in one fake server
func TestStripeQueryCloseAndProbe(t *testing.T) {
	var expired, probed bool
	c, _ := stripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/cs_open") && strings.HasSuffix(r.URL.Path, "/expire"):
			expired = true
			writeStripeJSON(t, w, stripeSession("cs_open", "expired", "unpaid"))
		case r.URL.Path == "/v1/checkout/sessions/cs_open":
			writeStripeJSON(t, w, stripeSession("cs_open", "open", "unpaid"))
		case r.URL.Path == "/v1/checkout/sessions/cs_expired":
			writeStripeJSON(t, w, stripeSession("cs_expired", "expired", "unpaid"))
		case r.URL.Path == "/v1/payment_intents/pi_1":
			writeStripeJSON(t, w, map[string]any{"id": "pi_1", "object": "payment_intent", "status": "succeeded", "amount": 1234, "currency": "usd"})
		case strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/cs_"):
			writeStripeJSON(t, w, stripeSession("cs_1", "complete", "paid"))
		case r.URL.Path == "/v1/balance":
			probed = true
			writeStripeJSON(t, w, map[string]any{"object": "balance", "available": []any{}, "pending": []any{}})
		default:
			http.NotFound(w, r)
		}
	})
	p, err := c.Query(t.Context(), "ord_1", "cs_1")
	if err != nil || p.State != PayPaid || p.TradeNo != "pi_1" || p.Paid.Minor != 1234 || p.Paid.Currency.String() != "USD" {
		t.Fatalf("query = %+v, %v", p, err)
	}
	if p, err := c.Query(t.Context(), "ord_1", "pi_1"); err != nil || p.State != PayPaid || p.TradeNo != "pi_1" || p.Paid.Minor != 1234 {
		t.Fatalf("query by payment intent = %+v, %v", p, err)
	}
	if err := c.Close(t.Context(), OrderID(uuid.NewV7()), "cs_open"); err != nil || !expired {
		t.Fatalf("close = %v, expired=%t", err, expired)
	}
	if err := c.Close(t.Context(), OrderID(uuid.NewV7()), "cs_expired"); err != nil {
		t.Fatalf("closing an expired session = %v", err)
	}
	if err := c.probe(t.Context()); err != nil || !probed {
		t.Fatalf("probe = %v, probed=%t", err, probed)
	}
	if err := c.Close(t.Context(), OrderID(uuid.NewV7()), ""); err == nil {
		t.Fatal("close without a session succeeded")
	}
}

//nolint:cyclop,gocognit // the refund table and its provider queries are the test
func TestStripeRefunds(t *testing.T) {
	var created url.Values
	var idempotency string
	call := RefundCall{
		Refund: RefundID(uuid.NewV7()), Order: OrderID(uuid.NewV7()), TradeNo: "cs_1",
		Amount: Money{Currency: currency(t, "USD"), Minor: 500}, Reason: "customer request",
	}
	refund := map[string]any{
		"id": "re_1", "object": "refund", "status": "succeeded", "amount": 500, "currency": "usd",
		"payment_intent": map[string]any{"id": "pi_1"}, "metadata": map[string]string{"refund": call.Refund.TypeID()},
	}
	c, _ := stripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/refunds":
			idempotency = r.Header.Get("Idempotency-Key")
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			created = r.PostForm
			writeStripeJSON(t, w, refund)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/refunds/re_1":
			writeStripeJSON(t, w, refund)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/refunds":
			writeStripeJSON(t, w, map[string]any{"object": "list", "url": "/v1/refunds", "has_more": false, "data": []any{refund}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/"):
			writeStripeJSON(t, w, stripeSession("cs_1", "complete", "paid"))
		default:
			http.NotFound(w, r)
		}
	})
	state, err := c.Refund(t.Context(), call)
	if err != nil || state.State != RefundSucceeded || state.ProviderRef != "re_1" {
		t.Fatalf("refund = %+v, %v", state, err)
	}
	if idempotency != "re_"+call.Refund.TypeID() || created.Get("payment_intent") != "pi_1" || created.Get("amount") != "500" ||
		created.Get("metadata[refund]") != call.Refund.TypeID() || created.Get("metadata[order]") != call.Order.TypeID() || created.Get("metadata[reason]") != "customer request" {
		t.Fatalf("idempotency=%q form=%v", idempotency, created)
	}
	if state, err := c.QueryRefund(t.Context(), RefundCall{Refund: call.Refund, TradeNo: "cs_1", ProviderRef: "re_1", Amount: call.Amount}); err != nil || state.State != RefundSucceeded {
		t.Fatalf("query by ref = %+v, %v", state, err)
	}
	call.ProviderRef = ""
	if state, err := c.QueryRefund(t.Context(), call); err != nil || state.State != RefundSucceeded || state.ProviderRef != "re_1" {
		t.Fatalf("query by intent = %+v, %v", state, err)
	}
}

func TestStripeQueryRefundNotFound(t *testing.T) {
	call := RefundCall{
		Refund: RefundID(uuid.NewV7()), Order: OrderID(uuid.NewV7()), TradeNo: "cs_1",
		Amount: Money{Currency: currency(t, "USD"), Minor: 500},
	}
	c, _ := stripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/refunds/re_"):
			w.WriteHeader(http.StatusNotFound)
			writeStripeJSON(t, w, map[string]any{"error": map[string]any{
				"type": "invalid_request_error", "message": "No such refund", "code": "resource_missing",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/refunds":
			writeStripeJSON(t, w, map[string]any{"object": "list", "url": "/v1/refunds", "has_more": false, "data": []any{}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/"):
			writeStripeJSON(t, w, stripeSession("cs_1", "complete", "paid"))
		default:
			http.NotFound(w, r)
		}
	})
	if _, err := c.QueryRefund(t.Context(), RefundCall{Refund: call.Refund, TradeNo: call.TradeNo, ProviderRef: "re_missing", Amount: call.Amount}); !errors.Is(err, ErrRefundNotFound) {
		t.Fatalf("query by a missing ref = %v, want ErrRefundNotFound", err)
	}
	if _, err := c.QueryRefund(t.Context(), call); !errors.Is(err, ErrRefundNotFound) {
		t.Fatalf("query with no matching list entry = %v, want ErrRefundNotFound", err)
	}
}

func stripeRefundForTest(status string) stripe.Refund {
	return stripe.Refund{ID: "re_1", Status: stripe.RefundStatus(status)}
}

func TestStripeRefundStatus(t *testing.T) {
	for status, want := range map[string]RefundOutcome{
		"pending": RefundPending, "requires_action": RefundPending, "succeeded": RefundSucceeded,
		"failed": RefundFailed, "canceled": RefundFailed,
	} {
		refund := stripeRefundForTest(status)
		if got, err := stripeRefund(&refund); err != nil || got.State != want {
			t.Errorf("%s = %+v, %v; want %v", status, got, err, want)
		}
	}
}
