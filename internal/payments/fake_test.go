package payments

import (
	"context"
	"errors"
	"strings"

	"axiom/internal/platform/jsonx"
)

// fakePlugin is a channel kind for tests: its settings name a gateway and the methods it offers, and it needs a key.
// create, close and caps let a test decide what its channel does, so it can watch the order reach the channel.
type fakePlugin struct {
	create func(CreateRequest) (Checkout, error)
	close  func(OrderID, string) error
	caps   Caps
}

type fakeSettings struct {
	Gateway string   `json:"gateway"`
	Methods []Method `json:"methods"`
}

func (fakePlugin) Kind() Kind { return "fake" }

func (p fakePlugin) Open(c MerchantConfig) (Channel, error) {
	var s fakeSettings
	if err := jsonx.UnmarshalKnown(c.Settings, &s); err != nil {
		return nil, errors.New("settings do not parse")
	}
	if !strings.HasPrefix(s.Gateway, "https://") {
		return nil, errors.New("the gateway must be an https address")
	}
	if c.Secrets["key"].Reveal() == "" {
		return nil, errors.New("a key is required")
	}
	return fakeChannel{settings: s, key: c.Secrets["key"].Reveal(), create: p.create, close: p.close, caps: p.caps}, nil
}

// fakeChannel offers its methods and does nothing else; key is what it would sign with.
type fakeChannel struct {
	settings fakeSettings
	key      string
	create   func(CreateRequest) (Checkout, error)
	close    func(OrderID, string) error
	caps     Caps
}

func (c fakeChannel) Caps() Caps          { return CapQuery | c.caps }
func (c fakeChannel) Methods() []Method { return c.settings.Methods }
func (c fakeChannel) Create(_ context.Context, r CreateRequest) (Checkout, error) {
	if c.create != nil {
		return c.create(r)
	}
	return Checkout{}, &Rejected{"fake", "no checkout"}
}

func (c fakeChannel) Verify(context.Context, Callback) (Notice, error) { return Notice{}, nil }
func (c fakeChannel) Query(context.Context, string, string) (Payment, error) {
	return Payment{State: PayPending}, nil
}

func (c fakeChannel) Close(_ context.Context, id OrderID, tradeNo string) error {
	if c.close != nil {
		return c.close(id, tradeNo)
	}
	return nil
}

func (fakeChannel) Refund(context.Context, RefundCall) (RefundState, error) {
	panic("fake: refund is not declared")
}

func (fakeChannel) QueryRefund(context.Context, RefundCall) (RefundState, error) {
	panic("fake: refund is not declared")
}

func (fakeChannel) Ack(ok bool) (int, []byte) {
	if ok {
		return 200, []byte("success")
	}
	return 500, []byte("fail")
}
