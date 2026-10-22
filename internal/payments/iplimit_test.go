package payments

import (
	"errors"
	"fmt"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/clientip"
	"axiom/internal/platform/pg"
)

// accountIn adds an account to the world's first site, with the balance row an account has, and returns its scope.
func (w *world) accountIn() (pg.Scope, uuid.UUID) {
	w.t.Helper()
	id := uuid.NewV7()
	w.exec(fmt.Sprintf("INSERT INTO platform.tenants VALUES ('%s','%s')", w.siteA, id),
		fmt.Sprintf("INSERT INTO billing.balances (site_id, account_id) VALUES ('%s','%s')", w.siteA, id))
	return pg.AccountScopeIn(w.siteA, id), id
}

// placeFrom places a 10-credit order for account as the request of ip.
func (w *world) placeFrom(s pg.Scope, account uuid.UUID, ip, key string) (Order, Checkout, error) {
	return w.placeAs(s, CreateOrder{
		Site: w.site(w.siteA, "CNY"), Account: account, User: w.user, Method: "alipay", Credits: 10 * yuan, Key: key,
		ReturnURL: "https://console.example.com/topup", ClientIP: netip.MustParseAddr(ip),
	})
}

// ipUsage is the counters of the IP group ip falls in, on the world's first site.
func (w *world) ipUsage(ip string) (orders, failures int) {
	w.t.Helper()
	err := w.tx(pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		return tx.QueryRow(w.t.Context(), "SELECT orders, failures FROM payments.ip_usage WHERE site_id = $1 AND ip = $2::inet",
			w.siteA, clientip.Group(netip.MustParseAddr(ip))).Scan(&orders, &failures)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return orders, failures
}

// fillGroup places the orders of the site's limit of three from the addresses of one IP group, the second by another
// account, and returns the refusal of the fourth, which is the first account's from the first address.
func (w *world) fillGroup(other pg.Scope, otherID uuid.UUID, addrs ...string) error {
	w.t.Helper()
	for i, ip := range addrs {
		s, id := w.accountA(), w.account
		if i == 1 {
			s, id = other, otherID
		}
		if _, _, err := w.placeFrom(s, id, ip, fmt.Sprintf("%s-%d", addrs[0], i)); err != nil {
			w.t.Fatalf("order %d from %s: %v", i+1, ip, err)
		}
	}
	_, _, err := w.placeFrom(w.accountA(), w.account, addrs[0], "over-"+addrs[0])
	return err
}

// wantWait checks that err is a refusal for cause that tells how long is left of the hour.
func wantWait(t *testing.T, err, cause error) {
	t.Helper()
	var wait *Wait
	if !errors.Is(err, cause) || !errors.As(err, &wait) || wait.After <= 59*time.Minute || wait.After > time.Hour {
		t.Fatalf("the refusal is %v (wait %v), want %v with most of an hour to wait", err, wait, cause)
	}
}

// TestIPOrdersAreLimitedPerGroup places orders up to a site's limit of three from one IPv4 group, across accounts and
// across the ways to write its address, and shows the next one refused with a wait.
func TestIPOrdersAreLimitedPerGroup(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	w.limits("ip_order_limit = 3, pending_limit = 50")
	other, otherID := w.accountIn()
	wantWait(t, w.fillGroup(other, otherID, "203.0.113.7", "::ffff:203.0.113.7", "203.0.113.7"), ErrTooManyIPOrders)
	if orders, _ := w.ipUsage("203.0.113.7"); orders != 3 {
		t.Errorf("the group counts %d orders after a refusal, want 3", orders)
	}
	// The replay of an order that was placed returns it; a refused request took nothing and left no order.
	if o, _, err := w.placeFrom(w.accountA(), w.account, "203.0.113.7", "203.0.113.7-0"); err != nil || o.State != Pending {
		t.Fatalf("a replay at the limit: %+v, %v", o, err)
	}
	if n := w.count(w.accountA(), "SELECT count(*) FROM payments.orders WHERE idem_key LIKE 'over-%'"); n != 0 {
		t.Fatalf("%d orders of refused requests", n)
	}
	// The next IPv4 address is another group.
	if _, _, err := w.placeFrom(other, otherID, "203.0.113.8", "neighbour"); err != nil {
		t.Fatalf("an order from the next address: %v", err)
	}
}

// TestIPv6OrdersAreLimitedPerPrefix: an IPv6 allocation shares its /64, so another address of it buys no new place, and
// the next /64 is another group.
func TestIPv6OrdersAreLimitedPerPrefix(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	w.limits("ip_order_limit = 3, pending_limit = 50")
	other, otherID := w.accountIn()
	wantWait(t, w.fillGroup(other, otherID, "2001:db8:1:2::1", "2001:db8:1:2:ffff::9", "2001:db8:1:2::1"), ErrTooManyIPOrders)
	if _, _, err := w.placeFrom(other, otherID, "2001:db8:1:3::1", "neighbour"); err != nil {
		t.Fatalf("an order from the next /64: %v", err)
	}
}

// TestIPOrderLimitHoldsAtOnce has ten accounts of one IP order at once with four places in the window.
func TestIPOrderLimitHoldsAtOnce(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	w.limits("ip_order_limit = 4")
	type account struct {
		s  pg.Scope
		id uuid.UUID
	}
	var accounts []account
	for range 10 {
		s, id := w.accountIn()
		accounts = append(accounts, account{s, id})
	}
	start, done := make(chan struct{}), make(chan error, len(accounts))
	for i, a := range accounts {
		go func() {
			<-start
			_, _, err := w.placeFrom(a.s, a.id, "198.51.100.20", fmt.Sprintf("race-%d", i))
			done <- err
		}()
	}
	close(start)
	placed, refused := 0, 0
	for range accounts {
		switch err := <-done; {
		case err == nil:
			placed++
		case errors.Is(err, ErrTooManyIPOrders):
			refused++
		default:
			t.Errorf("placing an order: %v", err)
		}
	}
	if placed != 4 || refused != 6 {
		t.Fatalf("%d placed, %d refused, want 4 and 6", placed, refused)
	}
}

// TestIPLimitsDefaultToTwentyAndTen: a site that sets none lets 20 orders in an hour through and refuses the 21st; and,
// from another IP, lets 10 failed orders through and refuses the 11th for their failures, not their number.
func TestIPLimitsDefaultToTwentyAndTen(t *testing.T) {
	var reject atomic.Bool
	w := newTestWorld(t, fakePlugin{create: func(r CreateRequest) (Checkout, error) {
		if reject.Load() {
			return Checkout{}, &Rejected{"declined", "card declined"}
		}
		return goodCheckout(r)
	}})
	w.waiting()
	w.limits("pending_limit = 100")
	for i := range defaultIPOrders {
		if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.50", fmt.Sprintf("n-%d", i)); err != nil {
			t.Fatalf("order %d: %v", i+1, err)
		}
	}
	if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.50", "n-over"); !errors.Is(err, ErrTooManyIPOrders) {
		t.Fatalf("the 21st order: %v", err)
	}
	reject.Store(true)
	for i := range defaultIPFailures {
		if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.51", fmt.Sprintf("f-%d", i)); !errors.Is(err, ErrRefused) {
			t.Fatalf("failing order %d: %v", i+1, err)
		}
	}
	reject.Store(false)
	var wait *Wait
	if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.51", "f-over"); !errors.Is(err, ErrTooManyIPFailures) || !errors.As(err, &wait) {
		t.Fatalf("an order after 10 failures: %v", err)
	}
	if orders, failures := w.ipUsage("192.0.2.51"); orders != defaultIPFailures || failures != defaultIPFailures {
		t.Fatalf("counters %d orders, %d failures", orders, failures)
	}
}

// TestIPWindowStartsAgain: an hour after the window opened, the counters begin again from the next order.
func TestIPWindowStartsAgain(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	w.limits("ip_order_limit = 1")
	if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.60", "first"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.60", "second"); !errors.Is(err, ErrTooManyIPOrders) {
		t.Fatalf("a second order in the window: %v", err)
	}
	w.exec("UPDATE payments.ip_usage SET window_start = window_start - interval '59 minutes', failures = 5")
	if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.60", "second"); !errors.Is(err, ErrTooManyIPOrders) {
		t.Fatalf("a minute before the hour is over: %v", err)
	}
	w.exec("UPDATE payments.ip_usage SET window_start = window_start - interval '2 minutes'")
	if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.60", "second"); err != nil {
		t.Fatalf("an hour after the window opened: %v", err)
	}
	if orders, failures := w.ipUsage("192.0.2.60"); orders != 1 || failures != 0 {
		t.Fatalf("counters %d orders, %d failures after the window began again", orders, failures)
	}
}

// TestRefusedByAnotherLimitTakesNoIPPlace: an order the account's own limits refuse leaves the IP's counters as they
// were, because the place is taken in the order's transaction.
func TestRefusedByAnotherLimitTakesNoIPPlace(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	w.limits("pending_limit = 1")
	if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.70", "one"); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.70", fmt.Sprintf("more-%d", i)); !errors.Is(err, ErrTooManyOrders) {
			t.Fatalf("a second waiting order: %v", err)
		}
	}
	if orders, _ := w.ipUsage("192.0.2.70"); orders != 1 {
		t.Fatalf("the IP counts %d orders, want 1", orders)
	}
}

// TestIPCountingFaultRefuses: when the counter cannot be written, the order is not placed and the channel is not
// asked: the limit does not open when its counting breaks.
func TestIPCountingFaultRefuses(t *testing.T) {
	var asked atomic.Int64
	w := newTestWorld(t, fakePlugin{create: func(r CreateRequest) (Checkout, error) { asked.Add(1); return goodCheckout(r) }})
	m := w.waiting()
	w.exec(`CREATE FUNCTION payments.refuse_ip_usage() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected counting fault'; END $$;
		CREATE TRIGGER refuse_ip_usage BEFORE INSERT OR UPDATE ON payments.ip_usage
		FOR EACH ROW EXECUTE FUNCTION payments.refuse_ip_usage()`)
	_, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.80", "faulty")
	var wait *Wait
	if err == nil || errors.As(err, &wait) {
		t.Fatalf("an order with the counter broken: %v", err)
	}
	if n := w.count(w.accountA(), "SELECT count(*) FROM payments.orders"); n != 0 || asked.Load() != 0 || w.used(m.ID) != 0 {
		t.Fatalf("%d orders, %d asks of the channel, %d reserved after a refused order", n, asked.Load(), w.used(m.ID))
	}
}

// TestFailureAndItsCountAreOne: an order is failed in the transaction that counts its failure, so a failure that cannot
// be counted leaves the order waiting, with its reservation, for the expiry to settle.
func TestFailureAndItsCountAreOne(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: func(CreateRequest) (Checkout, error) { return Checkout{}, &Rejected{"declined", "no"} }})
	m := w.waiting()
	w.exec(`CREATE FUNCTION payments.refuse_failures() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected counting fault'; END $$;
		CREATE TRIGGER refuse_failures BEFORE INSERT OR UPDATE ON payments.ip_usage
		FOR EACH ROW WHEN (NEW.failures > 0) EXECUTE FUNCTION payments.refuse_failures()`)
	o, _, err := w.placeFrom(w.accountA(), w.account, "192.0.2.90", "faulty-failure")
	if err == nil {
		t.Fatal("a refused order with the failure count broken answered success")
	}
	if state, _, _ := w.stored(o.ID); state != "pending" || w.used(m.ID) != o.Total.Minor {
		t.Fatalf("state %s, %d reserved: the order failed without its failure counted", state, w.used(m.ID))
	}
}
