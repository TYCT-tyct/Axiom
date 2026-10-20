package payments

import (
	"context"
	"errors"
	"strings"

	"axiom/internal/platform/jsonx"
)

// fakePlugin is a channel kind for tests: its settings name a gateway and the methods it offers, and it needs a key.
type fakePlugin struct{}

type fakeSettings struct {
	Gateway string   `json:"gateway"`
	Methods []Method `json:"methods"`
}

func (fakePlugin) Kind() Kind { return "fake" }

func (fakePlugin) Open(c MerchantConfig) (Channel, error) {
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
	return fakeChannel{s, c.Secrets["key"].Reveal()}, nil
}

// fakeChannel offers its methods and does nothing else; key is what it would sign with.
type fakeChannel struct {
	settings fakeSettings
	key      string
}

func (fakeChannel) Caps() Caps          { return CapQuery }
func (c fakeChannel) Methods() []Method { return c.settings.Methods }
func (fakeChannel) Create(context.Context, CreateRequest) (Checkout, error) {
	return Checkout{}, &Rejected{"fake", "no checkout"}
}
func (fakeChannel) Verify(context.Context, Callback) (Notice, error) { return Notice{}, nil }
func (fakeChannel) Query(context.Context, string, string) (Payment, error) {
	return Payment{State: PayPending}, nil
}
func (fakeChannel) Close(context.Context, OrderID, string) error { return nil }
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
