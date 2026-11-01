package payments

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v85/webhook"

	"axiom/internal/platform/secret"
)

const stripeTestWebhookSecret = "whsec_test_secret_0123456789"

func stripeSigned(t *testing.T, payload []byte, secretValue string) Callback {
	t.Helper()
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload, Secret: secretValue, Timestamp: time.Now(),
	})
	return Callback{Method: http.MethodPost, Body: payload, Header: http.Header{"Stripe-Signature": []string{signed.Header}}}
}

func stripeEvent(t *testing.T, object map[string]any, kind, apiVersion string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": "evt_1", "object": "event", "type": kind, "api_version": apiVersion,
		"created": time.Now().Unix(), "livemode": false, "data": map[string]any{"object": object},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func stripeTestChannel(t *testing.T) *stripeChannel {
	t.Helper()
	return &stripeChannel{
		currency: currency(t, "USD"), webhook: secret.New(stripeTestWebhookSecret),
		oldWebhook: secret.New("whsec_old_secret_0123456789"),
	}
}

func TestStripeWebhookCheckoutEvents(t *testing.T) {
	c := stripeTestChannel(t)
	object := map[string]any{
		"id": "cs_1", "object": "checkout.session", "status": "complete", "payment_status": "paid",
		"amount_total": 1234, "currency": "usd", "payment_intent": "pi_1", "client_reference_id": "ord_1",
	}
	for _, tc := range []struct {
		name    string
		kind    string
		object  map[string]any
		success bool
		paid    int64
		order   string
		trade   string
		raw     bool
	}{
		{"completed paid", "checkout.session.completed", object, true, 1234, "ord_1", "pi_1", true},
		{"async succeeded", "checkout.session.async_payment_succeeded", object, true, 1234, "ord_1", "pi_1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := c.Verify(t.Context(), stripeSigned(t, stripeEvent(t, tc.object, tc.kind, "2020-08-27"), stripeTestWebhookSecret))
			if err != nil {
				t.Fatal(err)
			}
			if !n.Relevant || !n.Success || n.Order != tc.order || n.TradeNo != tc.trade || n.Paid.Minor != tc.paid ||
				n.Paid.Currency.String() != "USD" || n.Raw == "" || n.Dispute != nil {
				t.Fatalf("notice = %+v", n)
			}
		})
	}

	// An asynchronous method has completed checkout but not paid yet; the async_payment_succeeded event follows.
	unpaid := map[string]any{"id": "cs_2", "status": "complete", "payment_status": "unpaid", "payment_intent": "pi_2", "client_reference_id": "ord_2", "amount_total": 500, "currency": "usd"}
	n, err := c.Verify(t.Context(), stripeSigned(t, stripeEvent(t, unpaid, "checkout.session.completed", "2020-08-27"), stripeTestWebhookSecret))
	if err != nil || n.Relevant || n.Success {
		t.Fatalf("unpaid completed = %+v, %v; want not relevant", n, err)
	}

	for _, kind := range []string{"checkout.session.expired", "checkout.session.async_payment_failed"} {
		n, err := c.Verify(t.Context(), stripeSigned(t, stripeEvent(t, map[string]any{
			"id": "cs_3", "payment_intent": "pi_3", "client_reference_id": "ord_3",
		}, kind, "2020-08-27"), stripeTestWebhookSecret))
		if err != nil || !n.Relevant || n.Success || n.Order != "ord_3" || n.TradeNo != "pi_3" {
			t.Fatalf("%s = %+v, %v", kind, n, err)
		}
	}

	if n, err := c.Verify(t.Context(), stripeSigned(t, stripeEvent(t, object, "customer.created", "2020-08-27"), stripeTestWebhookSecret)); err != nil || n.Relevant {
		t.Fatalf("unrelated event = %+v, %v; want ignored", n, err)
	}
}

func TestStripeWebhookDisputes(t *testing.T) {
	c := stripeTestChannel(t)
	base := map[string]any{
		"id": "dp_1", "object": "dispute", "amount": 1234, "currency": "usd", "reason": "fraudulent",
		"charge":         map[string]any{"id": "ch_1", "metadata": map[string]string{"order": "ord_1"}},
		"payment_intent": map[string]any{"id": "pi_1"},
	}
	for _, tc := range []struct {
		name   string
		kind   string
		status string
		action DisputeAction
	}{
		{"created open", "charge.dispute.created", "needs_response", DisputeOpen},
		{"funds withdrawn open", "charge.dispute.funds_withdrawn", "under_review", DisputeOpen},
		{"closed lost", "charge.dispute.closed", "lost", DisputeLost},
		{"closed won", "charge.dispute.closed", "won", DisputeWon},
		{"funds reinstated won", "charge.dispute.funds_reinstated", "won", DisputeWon},
		{"warning", "charge.dispute.created", "warning_needs_response", DisputeWarning},
		{"prevented", "charge.dispute.closed", "prevented", DisputeWarning},
		{"unknown status stays warning", "charge.dispute.updated", "needs_response", DisputeWarning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := make(map[string]any, len(base)+1)
			for k, v := range base {
				object[k] = v
			}
			object["status"] = tc.status
			payload := stripeEvent(t, object, tc.kind, "2020-08-27")
			if tc.kind == "charge.dispute.updated" {
				// updated is not subscribed; Verify must not claim it.
				if n, err := c.Verify(t.Context(), stripeSigned(t, payload, stripeTestWebhookSecret)); err != nil || n.Relevant {
					t.Fatalf("updated = %+v, %v; want ignored", n, err)
				}
				return
			}
			n, err := c.Verify(t.Context(), stripeSigned(t, payload, stripeTestWebhookSecret))
			if err != nil || !n.Relevant || n.Dispute == nil {
				t.Fatalf("notice = %+v, %v", n, err)
			}
			d := n.Dispute
			if d.Provider != "stripe" || d.ID != "dp_1" || d.PaymentIntent != "pi_1" || d.Order != "ord_1" ||
				d.Status != tc.status || d.Reason != "fraudulent" || d.Amount.Minor != 1234 || d.Amount.Currency.String() != "USD" || d.Action != tc.action {
				t.Fatalf("dispute = %+v", d)
			}
			if n.Raw == "" {
				t.Fatal("raw event reference is empty")
			}
		})
	}
}

func TestStripeWebhookDisputeWithStringReferences(t *testing.T) {
	c := stripeTestChannel(t)
	object := map[string]any{
		"id": "dp_2", "object": "dispute", "amount": 500, "currency": "usd", "reason": "fraudulent",
		"status": "needs_response", "charge": "ch_2", "payment_intent": "pi_2",
	}
	n, err := c.Verify(t.Context(), stripeSigned(t, stripeEvent(t, object, "charge.dispute.created", "2020-08-27"), stripeTestWebhookSecret))
	if err != nil || n.Dispute == nil || n.Dispute.PaymentIntent != "pi_2" || n.Dispute.Order != "" {
		t.Fatalf("string references = %+v, %v", n, err)
	}
}

func TestStripeWebhookSignatureAndRotation(t *testing.T) {
	c := stripeTestChannel(t)
	payload := stripeEvent(t, map[string]any{"id": "cs_1", "status": "complete", "payment_status": "paid", "amount_total": 1, "currency": "usd", "payment_intent": "pi_1", "client_reference_id": "o_1"}, "checkout.session.completed", "2020-08-27")

	if _, err := c.Verify(t.Context(), stripeSigned(t, payload, "whsec_wrong")); err == nil {
		t.Fatal("a wrong secret verified")
	}
	// The old secret remains valid while Stripe rotates the endpoint's secret.
	n, err := c.Verify(t.Context(), stripeSigned(t, payload, "whsec_old_secret_0123456789"))
	if err != nil || !n.Success {
		t.Fatalf("old secret = %+v, %v", n, err)
	}
	bad := stripeSigned(t, payload, stripeTestWebhookSecret)
	bad.Method = http.MethodGet
	if _, err := c.Verify(t.Context(), bad); err == nil {
		t.Fatal("GET verified as a callback")
	}
	missing := stripeSigned(t, payload, stripeTestWebhookSecret)
	missing.Header = http.Header{}
	if _, err := c.Verify(t.Context(), missing); err == nil {
		t.Fatal("missing signature verified")
	}
	if _, err := c.Verify(t.Context(), Callback{Method: http.MethodPost, Body: []byte(`{`), Header: http.Header{"Stripe-Signature": []string{"t=1,v1=00"}}}); err == nil {
		t.Fatal("malformed event verified")
	}
}
