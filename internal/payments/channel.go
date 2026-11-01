package payments

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"time"

	"axiom/internal/platform"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/secret"
)

// The channel interface, after sub2api's Provider and its extension interfaces (backend/internal/payment/types.go:
// 217-263 at 8f2cbb5), merged into one interface and a capability set (README.md「渠道接口」).

// Kind is a kind of payment channel, the protocol a plugin speaks: "easypay", "stripe".
type Kind string

// Method is a payment method of the site, what the user picks on the top-up page: "alipay", "wxpay", "usdt.tron".
type Method string

// Caps is what a merchant's channel can do beyond creating and verifying payments. An easypay gateway's abilities
// depend on the gateway, so a channel declares them per merchant instead of per kind. Calling a method whose
// capability is not declared is a programming error and panics.
type Caps uint8

// The capabilities.
const (
	CapQuery       Caps = 1 << iota // Query
	CapClose                        // Close
	CapRefund                       // Refund
	CapQueryRefund                  // QueryRefund
	CapRefundKey                    // the channel takes RefundCall.Refund as an idempotency key, so a refund can be resent
	CapStatements                   // daily statements
)

// Has reports whether every capability of want is in c.
func (c Caps) Has(want Caps) bool { return c&want == want }

// Plugin turns a merchant's configuration into a channel. Open validates as well as binds: saving a merchant calls it
// and refuses a configuration it refuses.
type Plugin interface {
	Kind() Kind
	Open(c MerchantConfig) (Channel, error)
}

// MerchantConfig is what a plugin gets of a merchant: never the database, so state and money stay in the core.
type MerchantConfig struct {
	ID       MerchantID
	Currency platform.Currency
	// Settings is canonical JSON the plugin defines and parses strictly; the core has checked its hash against the one
	// sealed with the secrets.
	Settings []byte
	Secrets  map[string]secret.Secret // only in memory
	Client   *egress.Client
}

// Channel is one merchant's binding of a plugin. Its errors and logs never carry a request's query or form, which
// may hold the merchant's key. An error that is not *Rejected means the outcome is unknown.
type Channel interface {
	Caps() Caps
	Methods() []Method // nil permits any configured method; otherwise the list is exhaustive
	Create(ctx context.Context, r CreateRequest) (Checkout, error)
	Verify(ctx context.Context, c Callback) (Notice, error)
	Query(ctx context.Context, outTradeNo, tradeNo string) (Payment, error)
	Close(ctx context.Context, order OrderID, tradeNo string) error
	Refund(ctx context.Context, r RefundCall) (RefundState, error)
	QueryRefund(ctx context.Context, r RefundCall) (RefundState, error)
	Ack(ok bool) (status int, body []byte)
}

// ErrRefundNotFound means QueryRefund found no refund with the call's provider reference or metadata. The core may
// send the refund again with the same idempotency key only within the channel's key window; an error is not *Rejected,
// because absence of a refund does not prove the first request never reached the channel.
var ErrRefundNotFound = errors.New("payments: the channel has no such refund")

// NotifyPath is shared by core URL generation and the running program's callback routes.
const NotifyPath = "/api/payments/v1/notify/"

// CreateRequest carries the merchant mapping and server-generated addresses to the channel.
type CreateRequest struct {
	Order     OrderID
	Merchant  MerchantID
	Method    Method
	Upstream  string // the configured merchant method mapping
	Total     Money
	Subject   string
	ReturnURL string
	NotifyURL string // built by the core from the server-provided site origin
	ClientIP  netip.Addr
	Mobile    bool
	ExpiresAt time.Time // when the order stops being payable; a channel may clamp it to its own limits
}

// Checkout is where the user pays. ClientSecret goes to the browser only, never to the database.
type Checkout struct {
	TradeNo      string `json:"trade_no,omitempty"`
	PayURL       string `json:"pay_url,omitempty"`
	QRCode       string `json:"qr_code,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// Callback is a notification as received.
type Callback struct {
	Method   string
	RawQuery string
	Body     []byte
	Header   http.Header
}

// Notice is a verified callback; an event that is not about a payment is Relevant false.
// Paid.Minor is zero for missing settlement data, or -1 for a verified but unrepresentable amount requiring review.
type Notice struct {
	Relevant, Success bool
	Order, TradeNo    string
	Paid              Money
	Raw               string
	// Dispute is set when the event is a chargeback. It takes precedence over Success and Paid; the core books it
	// against the order the payment intent belongs to.
	Dispute *DisputeNotice
}

// DisputeAction is what a channel's chargeback event says happened, normalized so the core never parses channel
// status strings.
type DisputeAction uint8

// The dispute actions. Open and Lost both take the money; Won gives it back; Warning records the event only.
const (
	DisputeWarning DisputeAction = iota
	DisputeOpen
	DisputeLost
	DisputeWon
)

// DisputeNotice is one verified chargeback event.
type DisputeNotice struct {
	Provider      string // the channel plugin kind, such as "stripe"
	ID            string // the channel's dispute ID, unique per merchant
	PaymentIntent string // the payment's trade number
	Order         string // the order the channel says it is, if any; the core still checks it against the payment
	Status        string // the channel's status, for the record and the page
	Reason        string
	Amount        Money // the net cash the channel pulled back, in the payment currency
	Action        DisputeAction
}

// PayState is a payment's state at the channel.
type PayState uint8

// The payment states.
const (
	PayPending PayState = iota
	PayPaid
	PayClosed
)

// Payment is what a query found; Paid.Minor uses the same missing/invalid distinction as Notice.
type Payment struct {
	State   PayState
	TradeNo string
	Paid    Money
}

// RefundCall asks the channel to refund or reports on a refund.
type RefundCall struct {
	Refund               RefundID
	Order                OrderID
	TradeNo, ProviderRef string
	Amount               Money
	Reason               string
	SentAt               time.Time // when the refund was first sent; zero when unknown
}

// RefundOutcome is a refund's state at the channel.
type RefundOutcome uint8

// The refund outcomes.
const (
	RefundPending RefundOutcome = iota
	RefundSucceeded
	RefundFailed
)

// RefundState is what the channel said of a refund.
type RefundState struct {
	State       RefundOutcome
	ProviderRef string
}

// Rejected is a definite failure: the channel refused the request or it was certainly not executed. It is the only
// error that may fail an order or a refund.
type Rejected struct{ Code, Message string }

func (e *Rejected) Error() string {
	return "payments: the channel rejected it or did not execute it: " + e.Code + " " + e.Message
}
