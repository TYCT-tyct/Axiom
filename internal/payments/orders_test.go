package payments

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// goodCheckout is a channel that takes the order: an https link and the client secret only the browser is meant to see.
func goodCheckout(r CreateRequest) (Checkout, error) {
	id := uuid.UUID(r.Order).String()
	return Checkout{TradeNo: "T" + id[:8], PayURL: "https://pay.example.com/checkout/" + id, ClientSecret: "s3cret"}, nil
}

// place asks for credits through the world's first site, as account A.
func (w *world) place(credits int64, key string) (Order, Checkout, error) {
	return w.placeAs(w.accountA(), CreateOrder{
		Site: w.site(w.siteA, "CNY"), Account: w.account, User: w.user, Method: "alipay", Credits: credits,
		Key: key, ReturnURL: "https://console.example.com/topup", ClientIP: netip.MustParseAddr("203.0.113.7"),
	})
}

// placeAs places a request of the test's own, so a test can change one field and watch what that field changes.
func (w *world) placeAs(s pg.Scope, r CreateOrder) (Order, Checkout, error) {
	return w.p.CreateOrder(w.t.Context(), s, r)
}

// mustPlace is place, fatal on error.
func (w *world) mustPlace(credits int64, key string) Order {
	w.t.Helper()
	o, _, err := w.place(credits, key)
	if err != nil {
		w.t.Fatal(err)
	}
	return o
}

// waiting puts the world's first site to work: top-ups open, one merchant account taking alipay.
func (w *world) waiting() Merchant {
	w.t.Helper()
	w.open(w.siteA, RoundRobin)
	return w.mustSave(siteScope(w.siteA), w.input(w.siteA))
}

// limits updates the site's order limits.
func (w *world) limits(settings string) {
	w.t.Helper()
	w.exec(fmt.Sprintf("UPDATE payments.settings SET %s WHERE site_id = '%s'", settings, w.siteA))
}

// stored is an order's row as the account's scope reads it.
func (w *world) stored(id OrderID) (state, tradeNo, checkout string) {
	w.t.Helper()
	err := w.tx(w.accountA(), func(tx pg.Tx) error {
		return tx.QueryRow(w.t.Context(), "SELECT state, trade_no, checkout::text FROM payments.orders WHERE id = $1",
			uuid.UUID(id)).Scan(&state, &tradeNo, &checkout)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return state, tradeNo, checkout
}

// events counts an order's events.
func (w *world) events(id OrderID) int {
	return w.count(w.accountA(), "SELECT count(*) FROM payments.order_events WHERE order_id = $1", uuid.UUID(id))
}

// paid writes the payment an order would have been given by a callback, so a test can start from a paid order.
func (w *world) paid(id OrderID) {
	w.t.Helper()
	w.exec(fmt.Sprintf(`UPDATE payments.orders SET state = 'paid', paid_at = transaction_timestamp(),
		paid_total = total, paid_fee = fee, credited = credits WHERE id = '%s'`, uuid.UUID(id)))
}

// onlyOrder is the order of account A, for the tests that placed exactly one.
func (w *world) onlyOrder() OrderID {
	w.t.Helper()
	var id uuid.UUID
	if err := w.tx(w.accountA(), func(tx pg.Tx) error {
		return tx.QueryRow(w.t.Context(), "SELECT id FROM payments.orders").Scan(&id)
	}); err != nil {
		w.t.Fatal(err)
	}
	return OrderID(id)
}

func isField(err error, field string) bool {
	var fe *FieldError
	return errors.As(err, &fe) && fe.Field == field
}

// TestOrderStatesFollowTheTable pins the one table of what an order may become (docs/domains/payments.md「订单」): every
// edge the design lists, and no other of the eighty-one pairs.
func TestOrderStatesFollowTheTable(t *testing.T) {
	documented := map[[2]State]bool{
		{Pending, Paid}: true, {Pending, Review}: true, {Pending, Expired}: true, {Pending, Cancelled}: true,
		{Pending, Failed}: true,
		{Expired, Paid}:   true, {Expired, Review}: true,
		{Cancelled, Paid}: true, {Cancelled, Review}: true,
		{Failed, Paid}: true, {Failed, Review}: true,
		{Review, Paid}: true, {Review, Refunded}: true,
		{Paid, PartiallyRefunded}: true, {Paid, Refunded}: true, {Paid, ChargedBack}: true,
		{PartiallyRefunded, PartiallyRefunded}: true, {PartiallyRefunded, Refunded}: true,
		{PartiallyRefunded, ChargedBack}: true,
		{Refunded, ChargedBack}:          true,
	}
	for _, from := range states {
		for _, to := range states {
			if got, want := from.canMoveTo(to), documented[[2]State{from, to}]; got != want {
				t.Errorf("%s → %s is %t, want %t", from, to, got, want)
			}
		}
	}
	for _, to := range states {
		var want []string
		for _, from := range states {
			if documented[[2]State{from, to}] {
				want = append(want, string(from))
			}
		}
		if got := predecessors(to); !slices.Equal(got, want) {
			t.Errorf("predecessors of %s = %v, want %v", to, got, want)
		}
	}
	if len(predecessors(Pending)) != 0 {
		t.Errorf("an order is born pending, not moved to it: %v", predecessors(Pending))
	}
}

// TestCreateOrderStoresWhatWasBought takes the first order of an account: the row its channel's answer is stored in, its
// events, what it reserved of the merchant's day, what another account sees, and the answers a repeated request gets.
func TestCreateOrderStoresWhatWasBought(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	want, err := quote(10*yuan, nil, currency(t, "CNY"), Fee{PPM: 30_000})
	if err != nil {
		t.Fatal(err)
	}
	o, c, err := w.place(10*yuan, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if o.State != Pending || o.Credits != 10*yuan || o.MerchantID != MerchantID(m.ID) || o.Total != want.Total ||
		o.Fee != want.Fee || o.Checkout.ClientSecret != "" || !o.ExpiresAt.After(o.CreatedAt) {
		t.Fatalf("order = %+v, want %+v", o, want)
	}
	if c.ClientSecret != "s3cret" || !strings.HasPrefix(c.PayURL, "https://pay.example.com/checkout/") {
		t.Fatalf("checkout = %+v", c)
	}
	state, tradeNo, stored := w.stored(o.ID)
	if state != "pending" || tradeNo != c.TradeNo || !strings.Contains(stored, `"pay_url"`) ||
		strings.Contains(stored, "client_secret") || strings.Contains(stored, "s3cret") {
		t.Fatalf("stored %s, %q, %s", state, tradeNo, stored)
	}
	if n := w.events(o.ID); n != 2 { // created, then its checkout
		t.Fatalf("%d events", n)
	}
	if used := w.used(uuid.UUID(m.ID)); used != want.Total.Minor {
		t.Fatalf("usage = %d, want %d", used, want.Total.Minor)
	}
	// Account data: another account of another site sees none of it.
	if n := w.count(w.accountB(), "SELECT count(*) FROM payments.orders"); n != 0 {
		t.Fatalf("another account sees %d orders", n)
	}
	if n := w.count(w.accountB(), "SELECT count(*) FROM payments.order_events"); n != 0 {
		t.Fatalf("another account sees %d events", n)
	}
	if _, err := w.p.Cancel(w.t.Context(), w.accountB(), o.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account cancelling: %v", err)
	}
	// The same request under the same key is the order it made, and its stored checkout carries no secret.
	if again, c2, err := w.place(10*yuan, "key-1"); err != nil || again.ID != o.ID || c2.TradeNo != c.TradeNo ||
		c2.ClientSecret != "" {
		t.Fatalf("replay = %+v, %+v, %v", again, c2, err)
	}
	if n := w.count(w.accountA(), "SELECT count(*) FROM payments.orders"); n != 1 {
		t.Fatalf("%d orders", n)
	}
	if _, _, err := w.place(11*yuan, "key-1"); !errors.Is(err, ErrKeyReused) {
		t.Fatalf("the same key for another order: %v", err)
	}
	if _, _, err := w.place(10*yuan, ""); !isField(err, "/idempotency_key") {
		t.Fatalf("without a key: %v", err)
	}
	// The return address comes from the site, never from the request: a plain-http one is refused before any order.
	r := CreateOrder{
		Site: w.site(w.siteA, "CNY"), Account: w.account, User: w.user, Method: "alipay", Credits: 10 * yuan,
		Key: "key-2", ReturnURL: "http://console.example.com/topup", ClientIP: netip.MustParseAddr("203.0.113.7"),
	}
	if _, _, err := w.placeAs(w.accountA(), r); !isField(err, "/return_url") {
		t.Fatalf("a plain-http return address: %v", err)
	}
}

// TestCreateOrderKeepsTheAccountToItsPlaces has ten orders arrive at once with three places free, then has the account's
// cancellations bar the next order.
func TestCreateOrderKeepsTheAccountToItsPlaces(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	start, done := make(chan struct{}), make(chan error, 10)
	var mu sync.Mutex
	var placed []Order
	for i := range 10 {
		go func() {
			<-start
			o, _, err := w.place(10*yuan, fmt.Sprintf("key-%d", i))
			if err == nil {
				mu.Lock()
				placed = append(placed, o)
				mu.Unlock()
			}
			done <- err
		}()
	}
	close(start)
	refused := 0
	for range 10 {
		switch err := <-done; {
		case err == nil:
		case errors.Is(err, ErrTooManyOrders):
			refused++
		default:
			t.Errorf("placing an order: %v", err)
		}
	}
	if len(placed) != defaultPending || refused != 10-defaultPending {
		t.Fatalf("%d placed, %d refused", len(placed), refused)
	}
	// A cancelled order gives its place back, and the cancellations themselves become the next limit.
	for i := range defaultCancels {
		id := placed[0].ID
		if i > 0 {
			id = w.mustPlace(10*yuan, fmt.Sprintf("cancel-%d", i)).ID
		}
		if _, err := w.p.Cancel(w.t.Context(), w.accountA(), id); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := w.place(10*yuan, "after-five-cancels"); !errors.Is(err, ErrTooManyCancels) {
		t.Fatalf("after five cancellations: %v", err)
	}
}

// TestCreateOrderHoldsTheAccountToItsDay buys against the site's daily limit until it refuses, cancels an order to show
// that a cancelled order stops counting, and then holds a first top-up to its own limit until a payment lifts it.
func TestCreateOrderHoldsTheAccountToItsDay(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	w.limits(fmt.Sprintf("daily_credits = %d", 20*yuan))
	first := w.mustPlace(15*yuan, "a")
	if _, _, err := w.place(6*yuan, "b"); !errors.Is(err, ErrDailyLimit) {
		t.Fatalf("past the day: %v", err)
	}
	w.mustPlace(5*yuan, "c")
	if _, _, err := w.place(yuan, "d"); !errors.Is(err, ErrDailyLimit) {
		t.Fatalf("the day exactly full: %v", err)
	}
	if _, err := w.p.Cancel(w.t.Context(), w.accountA(), first.ID); err != nil {
		t.Fatal(err)
	}
	w.mustPlace(yuan, "e")
	if _, _, err := w.place(20*yuan, "f"); !errors.Is(err, ErrDailyLimit) {
		t.Fatalf("past the day after a cancel: %v", err)
	}
	// A first top-up is limited until the account has paid for one.
	w.limits("daily_credits = NULL, first_credits = " + fmt.Sprint(15*yuan))
	if _, _, err := w.place(16*yuan, "g"); !errors.Is(err, ErrFirstLimit) {
		t.Fatalf("a first top-up over the limit: %v", err)
	}
	w.paid(w.mustPlace(15*yuan, "h").ID)
	if _, _, err := w.place(16*yuan, "i"); err != nil {
		t.Fatalf("after a payment: %v", err)
	}
	if _, _, err := w.place(16*yuan, "j"); !errors.Is(err, ErrTooManyOrders) {
		t.Fatalf("the place limit: %v", err)
	}
}

// TestCreateOrderRefusesALinkToFollow fails an order whose channel answers with a link the browser must not be sent to,
// and gives the merchant's day back with it.
func TestCreateOrderRefusesALinkToFollow(t *testing.T) {
	for name, create := range map[string]func(CreateRequest) (Checkout, error){
		"javascript": func(CreateRequest) (Checkout, error) {
			return Checkout{TradeNo: "T", PayURL: "javascript:alert(1)"}, nil
		},
		"protocol-relative": func(CreateRequest) (Checkout, error) {
			return Checkout{TradeNo: "T", QRCode: "//evil.example/pay"}, nil
		},
		"relative": func(CreateRequest) (Checkout, error) {
			return Checkout{TradeNo: "T", PayURL: "/pay/1"}, nil
		},
		"data": func(CreateRequest) (Checkout, error) {
			return Checkout{TradeNo: "T", PayURL: "data:text/html,<script>alert(1)</script>"}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newTestWorld(t, fakePlugin{create: create})
			m := w.waiting()
			o, _, err := w.place(10*yuan, "k")
			if !errors.Is(err, ErrUnsafeLink) {
				t.Fatalf("err = %v", err)
			}
			if state, _, _ := w.stored(o.ID); state != "failed" {
				t.Fatalf("state %s", state)
			}
			if used := w.used(uuid.UUID(m.ID)); used != 0 {
				t.Fatalf("usage %d, want nothing reserved", used)
			}
		})
	}
}

// TestCreateOrderCleansALink takes a link with a NUL byte in it: the database refuses those, and they mean nothing in a
// link, so the stored checkout is the link without them.
func TestCreateOrderCleansALink(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: func(CreateRequest) (Checkout, error) {
		return Checkout{TradeNo: "T\x001", PayURL: "https://pay.example.com/\x00pay"}, nil
	}})
	w.waiting()
	o := w.mustPlace(10*yuan, "k")
	if state, tradeNo, stored := w.stored(o.ID); state != "pending" || tradeNo != "T1" ||
		!strings.Contains(stored, "https://pay.example.com/pay") || strings.Contains(stored, `\u0000`) {
		t.Fatalf("stored %s, %q, %s", state, tradeNo, stored)
	}
}

// TestCreateOrderLeavesTheOrderWhenTheChannelIsNotHeardFrom checks the order is written before the channel is called, and
// that a channel whose answer is lost leaves the order waiting for payment instead of failing it: it may exist there.
func TestCreateOrderLeavesTheOrderWhenTheChannelIsNotHeardFrom(t *testing.T) {
	var during int
	var w *world
	w = newTestWorld(t, fakePlugin{create: func(CreateRequest) (Checkout, error) {
		during = w.count(pg.NewScope(true, nil, nil), "SELECT count(*) FROM payments.orders WHERE state = 'pending'")
		return Checkout{}, errors.New("the gateway timed out")
	}})
	m := w.waiting()
	if _, _, err := w.place(10*yuan, "k"); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("a lost answer: %v", err)
	}
	if during != 1 {
		t.Fatalf("%d orders existed while the channel was called", during)
	}
	id := w.onlyOrder()
	if state, tradeNo, _ := w.stored(id); state != "pending" || tradeNo != "" {
		t.Fatalf("stored %s, %q", state, tradeNo)
	}
	if used := w.used(uuid.UUID(m.ID)); used == 0 {
		t.Fatal("the merchant's day was given back for an order the gateway may have taken")
	}
	// The key still answers that order, so the caller may retry with it instead of paying twice.
	if again, _, err := w.place(10*yuan, "k"); err != nil || again.ID != id {
		t.Fatalf("replay after a lost answer: %+v, %v", again, err)
	}
}

// TestCreateOrderAnswersNoSuccessWithoutItsCheckout is the fault the card asks for: the transaction after the channel's
// answer fails, so the caller is told the outcome is unknown instead of being handed a checkout nothing stored.
func TestCreateOrderAnswersNoSuccessWithoutItsCheckout(t *testing.T) {
	var w *world
	w = newTestWorld(t, fakePlugin{create: func(r CreateRequest) (Checkout, error) {
		w.exec(fmt.Sprintf("UPDATE payments.orders SET state = 'cancelled' WHERE id = '%s'", uuid.UUID(r.Order)))
		return goodCheckout(r)
	}})
	w.waiting()
	o, c, err := w.place(10*yuan, "k")
	if !errors.Is(err, ErrUnconfirmed) || c.TradeNo != "" {
		t.Fatalf("%+v, %v", c, err)
	}
	if state, tradeNo, _ := w.stored(o.ID); state != "cancelled" || tradeNo != "" {
		t.Fatalf("stored %s, %q", state, tradeNo)
	}
}

// TestCancelGivesThePlaceBack cancels a waiting order: it stops being payable, its reservation goes back, the channel is
// asked to close it, and a second cancel is the same order.
func TestCancelGivesThePlaceBack(t *testing.T) {
	var mu sync.Mutex
	var closed []OrderID
	w := newTestWorld(t, fakePlugin{caps: CapClose, create: goodCheckout, close: func(id OrderID, _ string) error {
		mu.Lock()
		defer mu.Unlock()
		closed = append(closed, id)
		return nil
	}})
	m := w.waiting()
	o := w.mustPlace(10*yuan, "k")
	if _, err := w.p.Cancel(w.t.Context(), w.accountA(), o.ID); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := w.stored(o.ID); state != "cancelled" {
		t.Fatalf("state %s", state)
	}
	if used := w.used(uuid.UUID(m.ID)); used != 0 {
		t.Fatalf("usage %d, want nothing reserved", used)
	}
	mu.Lock()
	if !slices.Contains(closed, o.ID) {
		t.Error("the channel was not asked to close the order")
	}
	mu.Unlock()
	if n := w.events(o.ID); n != 3 { // created, its checkout, cancelled
		t.Fatalf("%d events", n)
	}
	if again, err := w.p.Cancel(w.t.Context(), w.accountA(), o.ID); err != nil || again.State != Cancelled {
		t.Fatalf("a second cancel: %+v, %v", again, err)
	}
	if n := w.events(o.ID); n != 3 {
		t.Fatalf("%d events after a second cancel", n)
	}
	// A paid order is not ours to cancel any more: its money is the query's (E1-2c).
	paid := w.mustPlace(10*yuan, "paid")
	w.paid(paid.ID)
	if _, err := w.p.Cancel(w.t.Context(), w.accountA(), paid.ID); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("cancelling a paid order: %v", err)
	}
	if _, err := w.p.Cancel(w.t.Context(), w.accountA(), OrderID(uuid.NewV7())); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelling an order that is not there: %v", err)
	}
}

// TestExpiryFreesThePlaceOnce expires an order past its deadline: once, however many runs of the job take it.
func TestExpiryFreesThePlaceOnce(t *testing.T) {
	var mu sync.Mutex
	var closed []OrderID
	w := newTestWorld(t, fakePlugin{caps: CapClose, create: goodCheckout, close: func(id OrderID, _ string) error {
		mu.Lock()
		defer mu.Unlock()
		closed = append(closed, id)
		return nil
	}})
	m := w.waiting()
	o := w.mustPlace(10*yuan, "k")
	w.exec(fmt.Sprintf("UPDATE payments.orders SET expires_at = transaction_timestamp() - interval '1 minute' WHERE id = '%s'",
		uuid.UUID(o.ID)))
	if err := w.p.poll(w.t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := w.stored(o.ID); state != "expired" {
		t.Fatalf("state %s", state)
	}
	if used := w.used(uuid.UUID(m.ID)); used != 0 {
		t.Fatalf("usage %d, want nothing reserved", used)
	}
	if n := w.events(o.ID); n != 3 {
		t.Fatalf("%d events", n)
	}
	mu.Lock()
	if !slices.Contains(closed, o.ID) {
		t.Error("the channel was not asked to close the expired order")
	}
	mu.Unlock()
	if err := w.p.poll(w.t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := w.events(o.ID); n != 3 {
		t.Fatalf("%d events after a second run", n)
	}
	mu.Lock()
	if len(closed) != 1 {
		t.Errorf("the channel was asked to close %d times", len(closed))
	}
	mu.Unlock()
}

// TestMerchantIdentityWaitsForOrdersInFlight refuses to change what a merchant account is while one of its orders is
// still in flight, and lets the change through once none is.
func TestMerchantIdentityWaitsForOrdersInFlight(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	o := w.mustPlace(10*yuan, "k")
	rev := m.Rev
	change := func(edit func(*MerchantInput)) (Merchant, error) {
		in := w.input(w.siteA)
		in.ID, in.Rev, in.Secrets = MerchantID(m.ID), rev, nil // the keys are kept unless the change is theirs
		edit(&in)
		saved, err := w.save(siteScope(w.siteA), in)
		if err == nil {
			rev = saved.Rev
		}
		return saved, err
	}
	for name, c := range map[string]struct {
		edit  func(*MerchantInput)
		field string
	}{
		"the currency": {func(in *MerchantInput) { in.Currency = currency(t, "USD") }, "/currency"},
		"the settings": {func(in *MerchantInput) {
			in.Settings = []byte(`{"methods": ["alipay"], "gateway": "https://pay.example.org"}`)
		}, "/settings"},
		"the keys": {func(in *MerchantInput) {
			in.Secrets = map[string]secret.Secret{"key": secret.New("another-key")}
		}, "/secrets"},
	} {
		if _, err := change(c.edit); !isField(err, c.field) {
			t.Errorf("%s with an order in flight: %v", name, err)
		}
	}
	// What the order does not rest on may still change while it waits.
	if _, err := change(func(in *MerchantInput) { in.Name = "Gateway 2" }); err != nil {
		t.Fatalf("renaming with an order in flight: %v", err)
	}
	if _, err := w.p.Cancel(w.t.Context(), w.accountA(), o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := change(func(in *MerchantInput) { in.Currency = currency(t, "USD") }); err != nil {
		t.Fatalf("changing the currency after the cancel: %v", err)
	}
}
