package payments

import (
	"context"
	"net/http"
	"net/netip"

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
	ID        MerchantID
	Currency  platform.Currency
	NotifyURL string // the callback address on the site's domain; empty while a save is being validated
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
	Methods() []Method
	Create(ctx context.Context, r CreateRequest) (Checkout, error)
	Verify(ctx context.Context, c Callback) (Notice, error)
	Query(ctx context.Context, outTradeNo, tradeNo string) (Payment, error)
	Close(ctx context.Context, order OrderID, tradeNo string) error
	Refund(ctx context.Context, r RefundCall) (RefundState, error)
	QueryRefund(ctx context.Context, r RefundCall) (RefundState, error)
	Ack(ok bool) (status int, body []byte)
}

// CreateRequest asks the channel for a checkout; the core builds ReturnURL from the site's domain.
type CreateRequest struct {
	Order     OrderID
	Method    Method
	Total     Money
	Subject   string
	ReturnURL string
	ClientIP  netip.Addr
	Mobile    bool
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

// Rejected is the channel's definite refusal, the only error that may fail an order or a refund.
type Rejected struct{ Code, Message string }

func (e *Rejected) Error() string {
	return "payments: the channel rejected it: " + e.Code + " " + e.Message
}
