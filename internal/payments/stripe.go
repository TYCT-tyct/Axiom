package payments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/webhook"

	"axiom/internal/platform"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

// Stripe binds Stripe Checkout's hosted page. The card number never reaches this program: the browser enters it on
// checkout.stripe.com, and this channel only creates the session, verifies signed events and moves refunds.
type Stripe struct{}

// Kind is the protocol name stored on merchant accounts.
func (Stripe) Kind() Kind { return "stripe" }

// stripeAPIBase is Stripe's API; tests point it at a local server. It is not a merchant setting: an administrator
// cannot redirect a merchant's secret key to another host.
var stripeAPIBase = "https://api.stripe.com"

func (Stripe) normalizeSettings(raw []byte) ([]byte, error) {
	var s struct{}
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil {
		return nil, errors.New("stripe: invalid settings JSON")
	}
	return []byte("{}"), nil
}

func (Stripe) identitySettings(raw []byte) ([]byte, error) {
	var s struct{}
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil {
		return nil, errors.New("stripe: invalid identity settings")
	}
	return []byte("{}"), nil
}

type stripeChannel struct {
	currency   platform.Currency
	client     *stripe.Client
	webhook    secret.Secret
	oldWebhook secret.Secret
}

// Open validates configuration without a network call. Secrets are only checked for shape here; probe proves the API
// key, and only a real event proves the webhook secret.
func (Stripe) Open(c MerchantConfig) (Channel, error) {
	var s struct{}
	if err := jsonx.UnmarshalKnown(c.Settings, &s); err != nil {
		return nil, errors.New("stripe: invalid settings JSON")
	}
	if c.Client == nil || c.Currency == (platform.Currency{}) {
		return nil, errors.New("stripe: require client and currency")
	}
	key := c.Secrets["secret_key"].Reveal()
	webhookSecret := c.Secrets["webhook_secret"].Reveal()
	oldWebhook := c.Secrets["old_webhook_secret"].Reveal()
	if !strings.HasPrefix(key, "sk_") && !strings.HasPrefix(key, "rk_") {
		return nil, errors.New("stripe: secret_key must start with sk_ or rk_")
	}
	if !strings.HasPrefix(webhookSecret, "whsec_") {
		return nil, errors.New("stripe: webhook_secret must start with whsec_")
	}
	if oldWebhook != "" && !strings.HasPrefix(oldWebhook, "whsec_") {
		return nil, errors.New("stripe: old_webhook_secret must start with whsec_")
	}
	for name := range c.Secrets {
		switch name {
		case "secret_key", "webhook_secret", "old_webhook_secret":
		default:
			return nil, errors.New("stripe: unknown secret name")
		}
	}
	return &stripeChannel{
		currency: c.Currency, client: newStripeClient(key, c.Client),
		webhook: c.Secrets["webhook_secret"], oldWebhook: c.Secrets["old_webhook_secret"],
	}, nil
}

func (Stripe) validateMerchant(in MerchantInput) error {
	for _, m := range in.Methods {
		if m.Enabled && !stripeUpstream(m.Upstream) {
			return fmt.Errorf("stripe: upstream %q is not card, alipay, wechat_pay or link", m.Upstream)
		}
	}
	return nil
}

func stripeUpstream(s string) bool {
	switch s {
	case "card", "alipay", "wechat_pay", "link":
		return true
	}
	return false
}

func newStripeClient(key string, client *egress.Client) *stripe.Client {
	hc := &http.Client{
		Transport: stripeTransport{client},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		HTTPClient: hc,
		// The core retries with the same idempotency key; a hidden SDK retry would make the caller unable to tell
		// whether a timeout happened before or after Stripe acted.
		MaxNetworkRetries: stripe.Int64(0),
		LeveledLogger:     &stripe.LeveledLogger{Level: stripe.LevelNull},
		URL:               stripe.String(stripeAPIBase),
	})
	return stripe.NewClient(key, stripe.WithBackends(backends))
}

type stripeTransport struct{ client *egress.Client }

func (t stripeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.client.Do(req)
}

func (*stripeChannel) Caps() Caps {
	return CapQuery | CapClose | CapRefund | CapQueryRefund | CapRefundKey
}

// Methods is nil: the site may name any method, and Create checks the merchant's upstream mapping against the four
// payment methods this channel can present.
func (*stripeChannel) Methods() []Method { return nil }

func (c *stripeChannel) Create(ctx context.Context, r CreateRequest) (Checkout, error) {
	ctx, cancel := stripeDeadline(ctx)
	defer cancel()
	if r.Total.Minor <= 0 || r.Total.Currency != c.currency {
		return Checkout{}, &Rejected{Code: "amount", Message: "the total is not payable in this account's currency"}
	}
	upstream := r.Upstream
	if upstream == "" {
		upstream = "card"
	}
	if !stripeUpstream(upstream) {
		return Checkout{}, &Rejected{Code: "method", Message: "unsupported Stripe payment method"}
	}
	if err := httpsURL(r.ReturnURL); err != nil {
		return Checkout{}, &Rejected{Code: "return_url", Message: "the return address is not https"}
	}
	md := map[string]string{"order": r.Order.TypeID(), "merchant": r.Merchant.TypeID()}
	params := &stripe.CheckoutSessionCreateParams{
		Mode:               stripe.String(stripe.CheckoutSessionModePayment),
		SuccessURL:         stripe.String(r.ReturnURL),
		CancelURL:          stripe.String(r.ReturnURL),
		ClientReferenceID:  stripe.String(r.Order.TypeID()),
		Metadata:           md,
		PaymentIntentData:  &stripe.CheckoutSessionCreatePaymentIntentDataParams{Metadata: md},
		PaymentMethodTypes: []*string{stripe.String(upstream)},
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{{
			Quantity: new(int64(1)),
			PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
				Currency:    stripe.String(strings.ToLower(c.currency.String())),
				UnitAmount:  new(r.Total.Minor),
				ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{Name: stripe.String(stripeSubject(r.Subject))},
			},
		}},
	}
	if upstream == "wechat_pay" {
		params.PaymentMethodOptions = &stripe.CheckoutSessionCreatePaymentMethodOptionsParams{
			WeChatPay: &stripe.CheckoutSessionCreatePaymentMethodOptionsWeChatPayParams{Client: stripe.String("web")},
		}
	}
	params.ExpiresAt = stripeExpiry(r.ExpiresAt)
	params.SetIdempotencyKey("cs_" + r.Order.TypeID())
	session, err := c.client.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return Checkout{}, stripeFailure(err)
	}
	if session.ID == "" || session.URL == "" {
		return Checkout{}, errors.New("stripe: checkout session without an id or URL")
	}
	return Checkout{TradeNo: session.ID, PayURL: session.URL}, nil
}

func (c *stripeChannel) Query(ctx context.Context, outTradeNo, tradeNo string) (Payment, error) {
	ctx, cancel := stripeDeadline(ctx)
	defer cancel()
	if tradeNo == "" {
		return Payment{State: PayPending}, nil
	}
	// A pending order carries the Checkout Session, while the paid order's trade number becomes the PaymentIntent
	// (the notification's identifier). Both name the same payment, so the query accepts either.
	if strings.HasPrefix(tradeNo, "pi_") {
		intent, err := c.client.V1PaymentIntents.Retrieve(ctx, tradeNo, nil)
		if err != nil {
			return Payment{}, stripeFailure(err)
		}
		switch intent.Status { //nolint:exhaustive // only succeeded and canceled are terminal here
		case stripe.PaymentIntentStatusSucceeded:
			return Payment{
				State: PayPaid, TradeNo: intent.ID,
				Paid: stripeMoney(c.currency, intent.Amount, intent.Currency),
			}, nil
		case stripe.PaymentIntentStatusCanceled:
			return Payment{State: PayClosed, TradeNo: intent.ID}, nil
		}
		return Payment{State: PayPending, TradeNo: intent.ID}, nil
	}
	session, err := c.client.V1CheckoutSessions.Retrieve(ctx, tradeNo, nil)
	if err != nil {
		return Payment{}, stripeFailure(err)
	}
	p := Payment{TradeNo: stripePaymentIntent(session)}
	switch session.Status { //nolint:exhaustive // open is the pending default
	case stripe.CheckoutSessionStatusExpired:
		return Payment{State: PayClosed, TradeNo: p.TradeNo}, nil
	case stripe.CheckoutSessionStatusComplete:
		if session.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid {
			return Payment{State: PayPaid, TradeNo: p.TradeNo, Paid: stripeMoney(c.currency, session.AmountTotal, session.Currency)}, nil
		}
	}
	return Payment{State: PayPending, TradeNo: p.TradeNo}, nil
}

func (c *stripeChannel) Close(ctx context.Context, order OrderID, tradeNo string) error {
	ctx, cancel := stripeDeadline(ctx)
	defer cancel()
	if tradeNo == "" {
		return &Rejected{Code: "no_session", Message: "the order has no checkout session"}
	}
	// Expire is not idempotent at Stripe: a second call on an already expired session is a 400. Read first so closing
	// an order twice stays a success, and never try to expire a session that is already complete.
	session, err := c.client.V1CheckoutSessions.Retrieve(ctx, tradeNo, nil)
	if err != nil {
		return stripeFailure(err)
	}
	switch session.Status { //nolint:exhaustive // open is the pending default
	case stripe.CheckoutSessionStatusExpired:
		return nil
	case stripe.CheckoutSessionStatusComplete:
		return &Rejected{Code: "complete", Message: "the checkout session is already complete"}
	}
	if _, err := c.client.V1CheckoutSessions.Expire(ctx, tradeNo, nil); err != nil {
		return stripeFailure(err)
	}
	return nil
}

func (c *stripeChannel) Refund(ctx context.Context, r RefundCall) (RefundState, error) {
	ctx, cancel := stripeDeadline(ctx)
	defer cancel()
	if r.Amount.Minor <= 0 || r.Amount.Currency != c.currency {
		return RefundState{}, &Rejected{Code: "amount", Message: "the refund is not in this account's currency"}
	}
	pi, err := c.paymentIntent(ctx, r.TradeNo)
	if err != nil {
		return RefundState{}, err
	}
	params := &stripe.RefundCreateParams{
		PaymentIntent: stripe.String(pi),
		Amount:        new(r.Amount.Minor),
		Metadata: map[string]string{
			"refund": r.Refund.TypeID(),
			"order":  r.Order.TypeID(),
			"reason": cutRunes(r.Reason, 500),
		},
	}
	params.SetIdempotencyKey("re_" + r.Refund.TypeID())
	refund, err := c.client.V1Refunds.Create(ctx, params)
	if err != nil {
		return RefundState{}, stripeFailure(err)
	}
	return stripeRefund(refund)
}

func (c *stripeChannel) QueryRefund(ctx context.Context, r RefundCall) (RefundState, error) {
	ctx, cancel := stripeDeadline(ctx)
	defer cancel()
	if r.ProviderRef != "" {
		refund, err := c.client.V1Refunds.Retrieve(ctx, r.ProviderRef, nil)
		if err != nil {
			return RefundState{}, stripeFailure(err)
		}
		if !stripeRefundMatches(refund, r) {
			return RefundState{}, errors.New("stripe: the refund found does not match its amount or currency")
		}
		return stripeRefund(refund)
	}
	pi, err := c.paymentIntent(ctx, r.TradeNo)
	if err != nil {
		return RefundState{}, err
	}
	list := c.client.V1Refunds.List(ctx, &stripe.RefundListParams{PaymentIntent: stripe.String(pi)})
	var found *stripe.Refund
	for refund, err := range list.All(ctx) {
		if err != nil {
			return RefundState{}, stripeFailure(err)
		}
		if refund.Metadata["refund"] != r.Refund.TypeID() {
			continue
		}
		if found != nil {
			return RefundState{}, errors.New("stripe: more than one refund carries this refund id")
		}
		found = refund
	}
	if found == nil {
		return RefundState{State: RefundPending}, nil
	}
	if !stripeRefundMatches(found, r) {
		return RefundState{}, errors.New("stripe: the refund found does not match its amount or currency")
	}
	return stripeRefund(found)
}

func (c *stripeChannel) paymentIntent(ctx context.Context, sessionID string) (string, error) {
	if sessionID == "" {
		return "", &Rejected{Code: "no_session", Message: "the order has no checkout session"}
	}
	if strings.HasPrefix(sessionID, "pi_") {
		return sessionID, nil
	}
	session, err := c.client.V1CheckoutSessions.Retrieve(ctx, sessionID, nil)
	if err != nil {
		return "", stripeFailure(err)
	}
	pi := stripePaymentIntent(session)
	if pi == "" {
		return "", errors.New("stripe: the checkout session has no payment intent")
	}
	return pi, nil
}

func (*stripeChannel) Ack(ok bool) (int, []byte) {
	if ok {
		return http.StatusOK, []byte("{\"received\":true}")
	}
	return http.StatusInternalServerError, []byte("{\"retry\":true}")
}

func (c *stripeChannel) probe(ctx context.Context) error {
	ctx, cancel := stripeDeadline(ctx)
	defer cancel()
	if _, err := c.client.V1Balance.Retrieve(ctx, nil); err != nil {
		return stripeFailure(err)
	}
	return nil
}

func (c *stripeChannel) Verify(ctx context.Context, cb Callback) (Notice, error) {
	if cb.Method != http.MethodPost {
		return Notice{}, errors.New("stripe: a webhook must be POST")
	}
	signature := cb.Header.Get("Stripe-Signature")
	if signature == "" {
		return Notice{}, errors.New("stripe: missing Stripe-Signature")
	}
	secretValue := c.webhook.Reveal()
	event, err := webhook.ConstructEventWithOptions(cb.Body, signature, secretValue, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil && c.oldWebhook.Reveal() != "" {
		event, err = webhook.ConstructEventWithOptions(cb.Body, signature, c.oldWebhook.Reveal(), webhook.ConstructEventOptions{
			IgnoreAPIVersionMismatch: true,
		})
	}
	if err != nil {
		return Notice{}, errors.New("stripe: invalid webhook signature")
	}
	return c.notice(event)
}

func stripeDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 10*time.Second)
}

func stripeFailure(err error) error {
	if se, ok := errors.AsType[*stripe.Error](err); ok {
		code := string(se.Code)
		if code == "" {
			code = string(se.Type)
		}
		if se.HTTPStatusCode >= 400 && se.HTTPStatusCode < 500 {
			// Stripe can echo the rejected value in Msg, and an auth failure may name part of the key. Keep errors
			// useful without copying gateway text into logs or discrepancy details.
			return &Rejected{Code: code, Message: "Stripe rejected the request"}
		}
		return fmt.Errorf("stripe: request failed (%s, HTTP %d)", code, se.HTTPStatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("stripe: request timed out: %w", err)
	}
	return errors.New("stripe: request failed")
}

func stripeMoney(fallback platform.Currency, minor int64, currency stripe.Currency) Money {
	if minor <= 0 {
		return Money{Currency: fallback, Minor: -1}
	}
	parsed, err := platform.ParseCurrency(strings.ToUpper(string(currency)))
	if err != nil || parsed != fallback {
		return Money{Currency: fallback, Minor: -1}
	}
	return Money{Currency: parsed, Minor: minor}
}

func stripePaymentIntent(s *stripe.CheckoutSession) string {
	if s.PaymentIntent == nil {
		return ""
	}
	return s.PaymentIntent.ID
}

func stripeExpiry(want time.Time) *int64 {
	now := time.Now()
	// Stripe wants at least 30 minutes and at most 24 hours; keep a minute of slack on both ends for the request's
	// travel, while the order's own expiry still decides when this side stops waiting.
	at := want
	if at.IsZero() || at.Before(now.Add(31*time.Minute)) {
		at = now.Add(31 * time.Minute)
	}
	if max := now.Add(24*time.Hour - time.Minute); at.After(max) {
		at = max
	}
	return new(at.Unix())
}

func stripeSubject(s string) string {
	if s == "" {
		s = "Top-up"
	}
	return cutRunes(s, 250)
}

func httpsURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("not an https URL")
	}
	return nil
}

func stripeRefundMatches(r *stripe.Refund, call RefundCall) bool {
	return r.Amount == call.Amount.Minor && string(r.Currency) == strings.ToLower(call.Amount.Currency.String())
}

func stripeRefund(r *stripe.Refund) (RefundState, error) {
	out := RefundState{ProviderRef: r.ID}
	switch r.Status {
	case stripe.RefundStatusSucceeded:
		out.State = RefundSucceeded
	case stripe.RefundStatusFailed, stripe.RefundStatusCanceled:
		out.State = RefundFailed
	case stripe.RefundStatusPending, stripe.RefundStatusRequiresAction:
		out.State = RefundPending
	default:
		return RefundState{}, fmt.Errorf("stripe: unknown refund status %q", r.Status)
	}
	return out, nil
}
