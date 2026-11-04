package payments

import (
	"bytes"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/stripe/stripe-go/v85/webhook"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

const stripeTestWebhookSecret = "whsec_test_secret_0123456789" //nolint:gosec // test-only signing secret

func stripeSigned(t *testing.T, payload []byte, secretValue string) Callback {
	t.Helper()
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload, Secret: secretValue, Timestamp: time.Now(),
	})
	return Callback{Method: http.MethodPost, Body: payload, Header: http.Header{"Stripe-Signature": []string{signed.Header}}}
}

func stripeEvent(t *testing.T, object map[string]any, kind string) []byte {
	t.Helper()
	raw, err := jsonx.MarshalCanonical(map[string]any{
		"id": "evt_1", "object": "event", "type": kind, "api_version": "2020-08-27",
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

// The table maps signed events to notices; TestStripeWebhookCheckoutCreditsOnce carries the paid event through the
// HTTP callback and asserts the credit and ledger.
//
//nolint:cyclop,gocognit // the event table and its notices are the test
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
			n, err := c.Verify(t.Context(), stripeSigned(t, stripeEvent(t, tc.object, tc.kind), stripeTestWebhookSecret))
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
	n, err := c.Verify(t.Context(), stripeSigned(t, stripeEvent(t, unpaid, "checkout.session.completed"), stripeTestWebhookSecret))
	if err != nil || n.Relevant || n.Success {
		t.Fatalf("unpaid completed = %+v, %v; want not relevant", n, err)
	}

	for _, kind := range []string{"checkout.session.expired", "checkout.session.async_payment_failed"} {
		n, err := c.Verify(t.Context(), stripeSigned(t, stripeEvent(t, map[string]any{
			"id": "cs_3", "payment_intent": "pi_3", "client_reference_id": "ord_3",
		}, kind), stripeTestWebhookSecret))
		if err != nil || !n.Relevant || n.Success || n.Order != "ord_3" || n.TradeNo != "pi_3" {
			t.Fatalf("%s = %+v, %v", kind, n, err)
		}
	}

	if n, err := c.Verify(t.Context(), stripeSigned(t, stripeEvent(t, object, "customer.created"), stripeTestWebhookSecret)); err != nil || n.Relevant {
		t.Fatalf("unrelated event = %+v, %v; want ignored", n, err)
	}
}

// The table maps signed events to dispute notices; TestStripeWebhookDisputeSuspendsTheAccount carries them through the
// HTTP callback into the dispute flow, the ledger and the suspension.
//
//nolint:cyclop,gocognit // the dispute table and its actions are the test
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
			maps.Copy(object, base)
			object["status"] = tc.status
			payload := stripeEvent(t, object, tc.kind)
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
	n, err := c.Verify(t.Context(), stripeSigned(t, stripeEvent(t, object, "charge.dispute.created"), stripeTestWebhookSecret))
	if err != nil || n.Dispute == nil || n.Dispute.PaymentIntent != "pi_2" || n.Dispute.Order != "" {
		t.Fatalf("string references = %+v, %v", n, err)
	}
}

func TestStripeWebhookSignatureAndRotation(t *testing.T) {
	c := stripeTestChannel(t)
	payload := stripeEvent(t, map[string]any{"id": "cs_1", "status": "complete", "payment_status": "paid", "amount_total": 1, "currency": "usd", "payment_intent": "pi_1", "client_reference_id": "o_1"}, "checkout.session.completed")

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

// stripeWebhookWorld runs the callback path end to end: an order is placed through Create, the signed event is handed
// to the same HTTP handler the program mounts, and the core, payments tables and billing ledger are real.
type stripeWebhookWorld struct {
	world    *world
	merchant Merchant
	amount   atomic.Int64 // amount_total the fake Stripe API reports for the next payment intent
	queries  atomic.Int64 // payment intents read while confirming a callback
}

func newStripeWebhookWorld(t *testing.T) *stripeWebhookWorld {
	t.Helper()
	s := &stripeWebhookWorld{world: newTestWorld(t, Stripe{})}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/balance":
			writeStripeJSON(t, w, map[string]any{"object": "balance", "available": []any{}, "pending": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/checkout/sessions":
			session := stripeSession("cs_1", "open", "unpaid")
			session["url"] = "https://checkout.stripe.com/c/pay/cs_1"
			session["expires_at"] = time.Now().Add(time.Hour).Unix()
			writeStripeJSON(t, w, session)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/payment_intents/"):
			s.queries.Add(1)
			writeStripeJSON(t, w, map[string]any{
				"id": "pi_1", "object": "payment_intent", "status": "succeeded",
				"amount": s.amount.Load(), "currency": "usd",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	old := stripeAPIBase
	stripeAPIBase = server.URL
	t.Cleanup(func() { stripeAPIBase = old })
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	t.Cleanup(client.CloseIdleConnections)
	s.world.p.client = client
	s.world.open(s.world.siteA, RoundRobin)
	in := s.world.input(s.world.siteA, MethodInput{Method: "card", Upstream: "card", Fee: Fee{PPM: 30_000}, Enabled: true})
	in.Kind, in.Settings, in.Currency = "stripe", []byte("{}"), currency(t, "USD")
	in.Secrets = map[string]secret.Secret{
		"secret_key": secret.New("sk_test_123"), "webhook_secret": secret.New(stripeTestWebhookSecret),
	}
	s.merchant = s.world.mustSave(siteScope(s.world.siteA), in)
	return s
}

// place creates an order through the Stripe plugin; amount is what the fake API answers when the callback confirms.
func (s *stripeWebhookWorld) place(t *testing.T, key string) Order {
	t.Helper()
	o, _, err := s.world.p.CreateOrder(t.Context(), s.world.accountA(), CreateOrder{
		Site: s.world.site(s.world.siteA, "USD"), Account: s.world.account, User: s.world.user,
		Method: "card", Credits: 10 * yuan, Key: key, ReturnURL: "https://console.example.com/topup",
		ClientIP: netip.MustParseAddr("203.0.113.7"),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.amount.Store(o.Total.Minor)
	return o
}

// send posts a signed event to the merchant's callback route.
func (s *stripeWebhookWorld) send(t *testing.T, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	cb := stripeSigned(t, payload, stripeTestWebhookSecret)
	merchant := MerchantID(s.merchant.ID).TypeID()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, NotifyPath+merchant, bytes.NewReader(cb.Body))
	r.Header = cb.Header.Clone()
	r.SetPathValue("merchant", merchant)
	rw := httptest.NewRecorder()
	s.world.p.Notify().ServeHTTP(rw, r)
	return rw
}

func stripeCheckoutPaid(t *testing.T, o Order) []byte {
	t.Helper()
	return stripeEvent(t, map[string]any{
		"id": "cs_1", "object": "checkout.session", "status": "complete", "payment_status": "paid",
		"amount_total": o.Total.Minor, "currency": "usd", "payment_intent": "pi_1",
		"client_reference_id": o.ID.TypeID(),
	}, "checkout.session.completed")
}

// TestStripeWebhookCheckoutCreditsOnce: a signed checkout event over the HTTP callback credits the order once, writes
// the ledger, and a duplicate neither queries the gateway nor credits again.
func TestStripeWebhookCheckoutCreditsOnce(t *testing.T) {
	s := newStripeWebhookWorld(t)
	o := s.place(t, "stripe-checkout")
	for range 2 {
		if r := s.send(t, stripeCheckoutPaid(t, o)); r.Code != http.StatusOK || r.Body.String() != `{"received":true}` {
			t.Fatalf("callback: %d %s", r.Code, r.Body)
		}
	}
	s.world.assertCredit(o, o.Credits)
	if state, _, _ := s.world.stored(o.ID); state != "paid" {
		t.Fatalf("order state = %q, want paid", state)
	}
	if n := s.queries.Load(); n != 1 {
		t.Fatalf("the callback confirmed with %d gateway queries, want one: the replay must not query or credit twice", n)
	}
	if n := s.world.count(s.world.p.platform, "SELECT count(*) FROM billing.credit_batches WHERE id = $1", uuid.UUID(o.ID)); n != 1 {
		t.Fatalf("credit batches = %d, want one", n)
	}
	if n := s.world.count(s.world.p.platform, "SELECT count(*) FROM billing.transactions WHERE kind = 'credit'"); n != 1 {
		t.Fatalf("credit ledger transactions = %d, want one", n)
	}
	if n := s.world.count(s.world.p.platform, "SELECT count(*) FROM payments.order_payments WHERE order_id = $1", uuid.UUID(o.ID)); n != 1 {
		t.Fatalf("recorded payments = %d, want one", n)
	}
	if n := s.world.count(s.world.p.platform, "SELECT count(*) FROM payments.order_events WHERE order_id = $1 AND kind = 'payment'", uuid.UUID(o.ID)); n != 1 {
		t.Fatalf("payment events = %d, want one", n)
	}
	if n := s.world.count(s.world.p.platform, `SELECT count(*) FROM (SELECT transaction_id FROM billing.entries
		GROUP BY transaction_id, currency HAVING sum(amount) <> 0) unbalanced`); n != 0 {
		t.Fatal("the credit ledger is unbalanced")
	}
}

// TestStripeWebhookDisputeSuspendsTheAccount: a warning is recorded without money, the signed open event books the
// chargeback and suspends the account through the existing dispute flow, and its duplicate moves neither.
//
//nolint:cyclop,gocognit // the warning/open/duplicate flow and its ledger assertions are the test
func TestStripeWebhookDisputeSuspendsTheAccount(t *testing.T) {
	s := newStripeWebhookWorld(t)
	o := s.place(t, "stripe-dispute")
	if r := s.send(t, stripeCheckoutPaid(t, o)); r.Code != http.StatusOK {
		t.Fatalf("checkout: %d %s", r.Code, r.Body)
	}
	s.world.assertCredit(o, o.Credits)
	before := s.world.count(s.world.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", o.AccountID)
	dispute := func(status string) []byte {
		return stripeEvent(t, map[string]any{
			"id": "dp_1", "object": "dispute", "amount": o.Total.Minor, "currency": "usd", "reason": "fraudulent",
			"status":         status,
			"charge":         map[string]any{"id": "ch_1", "metadata": map[string]string{"order": o.ID.TypeID()}},
			"payment_intent": map[string]any{"id": "pi_1"},
		}, "charge.dispute.created")
	}
	if r := s.send(t, dispute("warning_needs_response")); r.Code != http.StatusOK {
		t.Fatalf("warning: %d %s", r.Code, r.Body)
	}
	if got := s.world.states.recorded(); len(got) != 0 {
		t.Fatalf("a warning suspended the account: %+v", got)
	}
	if state, _, _ := s.world.stored(o.ID); state != "paid" {
		t.Fatalf("order state after a warning = %q, want paid", state)
	}

	open := dispute("needs_response")
	for range 2 {
		if r := s.send(t, open); r.Code != http.StatusOK {
			t.Fatalf("dispute: %d %s", r.Code, r.Body)
		}
	}
	if state, _, _ := s.world.stored(o.ID); state != "charged_back" {
		t.Fatalf("order state = %q, want charged_back", state)
	}
	if got := s.world.count(s.world.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", o.AccountID); got != before-int(o.Credits) {
		t.Fatalf("balance = %d, want %d", got, before-int(o.Credits))
	}
	var state string
	var credits, cash, fee int64
	var suspended bool
	if err := s.world.tx(s.world.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT state, credits, cash, fee, suspended_at IS NOT NULL
			FROM payments.order_disputes WHERE provider_id = 'dp_1'`).Scan(&state, &credits, &cash, &fee, &suspended)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "applied" || credits != o.Credits || cash != o.Total.Minor || fee != o.Fee || !suspended {
		t.Fatalf("dispute row = %s credits=%d cash=%d fee=%d suspended=%t; want applied, %d, %d, %d, true",
			state, credits, cash, fee, suspended, o.Credits, o.Total.Minor, o.Fee)
	}
	if n := s.world.count(s.world.p.platform, "SELECT count(*) FROM billing.transactions WHERE kind = 'chargeback'"); n != 1 {
		t.Fatalf("chargeback transactions = %d, want one", n)
	}
	if n := s.world.count(s.world.p.platform, `SELECT count(*) FROM (SELECT transaction_id FROM billing.entries
		GROUP BY transaction_id, currency HAVING sum(amount) <> 0) unbalanced`); n != 0 {
		t.Fatal("the chargeback ledger is unbalanced")
	}
	calls := s.world.states.recorded()
	if len(calls) != 1 || uuid.UUID(calls[0].account) != o.AccountID || !calls[0].suspended ||
		!strings.Contains(calls[0].reason, "stripe/dp_1") {
		t.Fatalf("suspension calls = %+v, want one for the disputed account", calls)
	}
}
