package payments

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"axiom/internal/platform/pg"
)

func TestPollExpiresBatchBeforeBlockedClose(t *testing.T) {
	var w *world
	var orders []Order
	calls := make(map[MerchantID]int)
	var stalled MerchantID
	pendingAtClose := -1
	w = newTestWorld(t, fakePlugin{caps: CapClose, create: goodCheckout, close: func(ctx context.Context, id OrderID, _ string) error {
		var merchant MerchantID
		for _, o := range orders {
			if o.ID == id {
				merchant = o.MerchantID
			}
		}
		calls[merchant]++
		if stalled == (MerchantID{}) {
			stalled = merchant
			pendingAtClose = w.count(w.p.platform, "SELECT count(*) FROM payments.orders WHERE state = 'pending'")
			// Exercise the real cancellation boundary, without a sleep or an elapsed-time assertion.
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}})
	w.waiting()
	w.mustSave(siteScope(w.siteA), w.input(w.siteA))
	for i := range 3 {
		orders = append(orders, w.mustPlace(yuan, fmt.Sprintf("due-%d", i)))
	}
	w.exec("UPDATE payments.orders SET expires_at = created_at - interval '1 minute'")
	if err := w.p.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pendingAtClose != 0 {
		t.Fatalf("gateway close started with %d orders still pending", pendingAtClose)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.orders WHERE state = 'expired'"); n != 3 {
		t.Fatalf("%d expired orders, want 3", n)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.merchant_usage WHERE used <> 0"); n != 0 {
		t.Fatalf("%d reservations survived expiry", n)
	}
	if len(calls) != 2 || calls[stalled] != 1 {
		t.Fatalf("stalled merchant closes %d, merchants reached %d", calls[stalled], len(calls))
	}
}

func TestUnavailableChannelFailsBeforeCreate(t *testing.T) {
	var w *world
	creates := 0
	unavailable := errors.New("injected configuration open failure")
	w = newTestWorld(t, fakePlugin{
		open: func() error {
			if w.count(w.p.platform, "SELECT count(*) FROM payments.orders") != 0 {
				return unavailable
			}
			return nil
		},
		create: func(r CreateRequest) (Checkout, error) {
			creates++
			return goodCheckout(r)
		},
	})
	m := w.waiting()
	o, c, err := w.place(yuan, "not-sent")
	if !errors.Is(err, unavailable) || errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("configuration failure classified as %v", err)
	}
	if creates != 0 || c != (Checkout{}) {
		t.Fatalf("%d gateway creates, checkout %+v", creates, c)
	}
	if state, _, _ := w.stored(o.ID); state != "failed" {
		t.Fatalf("unsent order state %s", state)
	}
	if w.used(m.ID) != 0 || w.events(o.ID) != 2 {
		t.Fatalf("usage %d, events %d", w.used(m.ID), w.events(o.ID))
	}
}

func TestUnsafeCheckoutClosesAfterRejectionCommit(t *testing.T) {
	var w *world
	closes := 0
	w = newTestWorld(t, fakePlugin{
		caps: CapClose,
		create: func(CreateRequest) (Checkout, error) {
			return Checkout{TradeNo: "gateway-unsafe", PayURL: "javascript:pay()"}, nil
		},
		close: func(_ context.Context, id OrderID, trade string) error {
			closes++
			state, storedTrade, checkout := w.stored(id)
			if [4]string{state, storedTrade, trade, checkout} != [4]string{"failed", "gateway-unsafe", "gateway-unsafe", "{}"} {
				return fmt.Errorf("uncommitted rejection: %s %s %s %s", state, storedTrade, trade, checkout)
			}
			return nil
		},
	})
	m := w.waiting()
	_, _, err := w.place(yuan, "unsafe")
	if !errors.Is(err, ErrUnsafeLink) || closes != 1 || w.used(m.ID) != 0 {
		t.Fatalf("rejection %v, %d closes, usage %d", err, closes, w.used(m.ID))
	}
	if logs := w.logs.String(); logs != "" {
		t.Fatalf("closing rejected checkout failed: %s", logs)
	}
}

func TestOrderTimesComeFromDatabase(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	o := w.mustPlace(yuan, "database-clock")
	var stored Order
	err := w.tx(w.accountA(), func(tx pg.Tx) (err error) {
		stored, err = loadOrder(t.Context(), tx, o.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string][2]time.Time{
		"created": {o.CreatedAt, stored.CreatedAt}, "expiry": {o.ExpiresAt, stored.ExpiresAt},
		"updated": {o.UpdatedAt, stored.UpdatedAt},
	} {
		if !v[0].Equal(v[1]) {
			t.Errorf("%s: returned %s, stored %s", name, v[0], v[1])
		}
	}
	if got := o.ExpiresAt.Sub(o.CreatedAt); got != time.Duration(m.TtlSeconds)*time.Second {
		t.Fatalf("validity %s, want %d seconds", got, m.TtlSeconds)
	}
}

func TestFirstTopUpUsesDefaultForNullSetting(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	if _, _, err := w.place(defaultFirstTop+1, "over-default"); !errors.Is(err, ErrFirstLimit) {
		t.Fatalf("NULL first limit: %v", err)
	}
	w.mustPlace(defaultFirstTop, "at-default")
}
