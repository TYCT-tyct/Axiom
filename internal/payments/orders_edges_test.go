package payments

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

func TestOrderReplaySurvivesClosedTopUps(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	o := w.mustPlace(yuan, "same")
	w.limits("open = false")
	again, _, err := w.place(yuan, "same")
	if err != nil || again.ID != o.ID {
		t.Fatalf("replay after closing top-ups: %+v, %v", again, err)
	}
	if _, _, err = w.place(2*yuan, "same"); !errors.Is(err, ErrKeyReused) {
		t.Fatalf("changed request: %v", err)
	}
}

func TestOrderPersistsTheExactBufferedRate(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA)
	in.Currency = currency(t, "USD")
	w.mustSave(siteScope(w.siteA), in)
	w.setRate("CNY", "USD", "5", "36")
	o := w.mustPlace(10*yuan, "fraction")
	// 10 CNY at (5/36)*(51/50) USD/CNY, then a 3% fee, rounds once to 147 cents.
	if o.Total != (Money{currency(t, "USD"), 147}) || o.Fee != 5 {
		t.Fatalf("total %+v, fee %d", o.Total, o.Fee)
	}
	if err := w.tx(pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		_, err := billing.SetExchangeRate(t.Context(), tx, admin, billing.ExchangeInput{
			Base: "CNY", Quote: "USD", Numerator: "1", Denominator: "6", Kind: "adjustment",
			ExpectedVersion: 1, Reason: "verified new reference rate",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var refNum, refDen, appliedNum, appliedDen string
	err := w.tx(w.accountA(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT ref_num::text, ref_den::text, applied_num::text, applied_den::text
			FROM payments.orders WHERE id = $1`, uuid.UUID(o.ID)).Scan(&refNum, &refDen, &appliedNum, &appliedDen)
	})
	if err != nil {
		t.Fatal(err)
	}
	if [4]string{refNum, refDen, appliedNum, appliedDen} != [4]string{"5", "36", "17", "120"} {
		t.Fatalf("locked rate %s/%s applied %s/%s", refNum, refDen, appliedNum, appliedDen)
	}
}

func TestCancellationReleasesTheCreationDay(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	o := w.mustPlace(yuan, "yesterday")
	w.exec(fmt.Sprintf(`UPDATE payments.orders SET created_at = created_at - interval '1 day' WHERE id = '%s';
		UPDATE payments.merchant_usage SET day = day - 1 WHERE merchant_id = '%s'`, uuid.UUID(o.ID), m.ID))
	if _, err := w.p.Cancel(t.Context(), w.accountA(), o.ID); err != nil {
		t.Fatal(err)
	}
	if n := w.count(w.accountA(), "SELECT count(*) FROM payments.merchant_usage WHERE used <> 0"); n != 0 {
		t.Fatalf("%d reservations survived cancellation across midnight", n)
	}
}

func TestChannelRejectionFreesReservation(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: func(CreateRequest) (Checkout, error) {
		return Checkout{}, &Rejected{"declined", "bad\x00" + strings.Repeat("中", maxRaw)}
	}})
	m := w.waiting()
	o, _, err := w.place(yuan, "rejected")
	if !errors.Is(err, errRefused) || errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("definite rejection became unknown: %v", err)
	}
	if state, _, _ := w.stored(o.ID); state != "failed" {
		t.Fatalf("state %s", state)
	}
	if w.used(m.ID) != 0 || w.events(o.ID) != 2 {
		t.Fatalf("usage %d, events %d", w.used(m.ID), w.events(o.ID))
	}
	var raw string
	err = w.tx(w.accountA(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT raw FROM payments.order_events WHERE order_id = $1 AND kind = 'reject'",
			uuid.UUID(o.ID)).Scan(&raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxRaw || strings.ContainsRune(raw, 0) {
		t.Fatalf("unsafe rejection event: %d bytes", len(raw))
	}
}

func TestConcurrentOrderReplayCreatesOneCheckout(t *testing.T) {
	var calls atomic.Int32
	w := newTestWorld(t, fakePlugin{create: func(r CreateRequest) (Checkout, error) {
		calls.Add(1)
		return goodCheckout(r)
	}})
	m := w.waiting()
	start := make(chan struct{})
	type result struct {
		o   Order
		err error
	}
	done := make(chan result, 10)
	for range 10 {
		go func() {
			<-start
			o, _, err := w.place(yuan, "same")
			done <- result{o, err}
		}()
	}
	close(start)
	var id OrderID
	for range 10 {
		r := <-done
		if r.err != nil {
			t.Fatal(r.err)
		}
		if id != (OrderID{}) && id != r.o.ID {
			t.Fatalf("different orders: %s, %s", id.TypeID(), r.o.ID.TypeID())
		}
		id = r.o.ID
	}
	if calls.Load() != 1 || w.events(id) != 2 {
		t.Fatalf("%d channel creates, %d events", calls.Load(), w.events(id))
	}
	q, err := quote(yuan, nil, currency(t, "CNY"), Fee{PPM: 30_000})
	if err != nil || w.used(m.ID) != q.Total.Minor {
		t.Fatalf("usage %d, quote %+v, %v", w.used(m.ID), q, err)
	}
}

func TestCheckoutCommitFailureReturnsUnknown(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	// The UPDATE and event both succeed; only COMMIT fails. This catches acknowledgements sent before commit.
	w.exec(`CREATE FUNCTION payments.reject_checkout_commit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected checkout commit failure'; END $$;
		CREATE CONSTRAINT TRIGGER reject_checkout_commit AFTER INSERT ON payments.order_events
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.kind = 'checkout')
		EXECUTE FUNCTION payments.reject_checkout_commit()`)
	o, c, err := w.place(yuan, "commit-failure")
	if !errors.Is(err, ErrUnconfirmed) || c != (Checkout{}) {
		t.Fatalf("checkout %+v, error %v", c, err)
	}
	state, trade, checkout := w.stored(o.ID)
	if state != "pending" || trade != "" || checkout != "{}" || w.events(o.ID) != 1 {
		t.Fatalf("partial write: %s %q %s, %d events", state, trade, checkout, w.events(o.ID))
	}
	if w.used(m.ID) != o.Total.Minor {
		t.Fatal("unknown gateway outcome released its reservation")
	}
}

func TestOrderRLSWithinOneSite(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	o := w.mustPlace(yuan, "rls")
	other := pg.AccountScopeIn(w.siteA, uuid.NewV7())
	for _, table := range []string{"orders", "order_events"} {
		if n := w.count(other, "SELECT count(*) FROM payments."+table); n != 0 {
			t.Fatalf("another account in the same site sees %d %s", n, table)
		}
	}
	if _, err := w.p.Cancel(t.Context(), other, o.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account cancelled: %v", err)
	}
}

func TestStaleMenuCannotPlaceAnOrder(t *testing.T) {
	w := newWorld(t)
	m := w.waiting()
	method := "alipay"
	menu, err := w.p.menu(t.Context(), w.accountA(), w.site(w.siteA, "CNY"), &method)
	if err != nil {
		t.Fatal(err)
	}
	in := w.input(w.siteA)
	in.ID, in.Rev, in.Secrets = MerchantID(m.ID), m.Rev, nil
	in.Currency = currency(t, "USD")
	w.mustSave(siteScope(w.siteA), in)
	err = w.tx(w.accountA(), func(tx pg.Tx) error {
		_, err := w.p.choose(t.Context(), tx, menu, "alipay", yuan)
		return err
	})
	if !errors.Is(err, ErrStale) || w.used(m.ID) != 0 {
		t.Fatalf("stale menu: %v, usage %d", err, w.used(m.ID))
	}
}

func TestMerchantIdentityIsHeldUntilOrderCommit(t *testing.T) {
	w := newWorld(t)
	m := w.waiting()
	method := "alipay"
	menu, err := w.p.menu(t.Context(), w.accountA(), w.site(w.siteA, "CNY"), &method)
	if err != nil {
		t.Fatal(err)
	}
	err = w.tx(w.accountA(), func(tx pg.Tx) error {
		if _, err := w.p.choose(t.Context(), tx, menu, "alipay", yuan); err != nil {
			return err
		}
		return w.tx(siteScope(w.siteA), func(save pg.Tx) error {
			var locked bool
			err := save.QueryRow(t.Context(), "SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))",
				"payments.merchant:"+MerchantID(m.ID).TypeID()).Scan(&locked)
			if err == nil && locked {
				return errors.New("merchant identity can change before the order commits")
			}
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCancelAndExpiryReleaseOnce(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	o := w.mustPlace(yuan, "race")
	w.exec(fmt.Sprintf("UPDATE payments.orders SET expires_at = transaction_timestamp() - interval '1 minute' WHERE id = '%s'", uuid.UUID(o.ID)))
	start, done := make(chan struct{}), make(chan error, 10)
	for i := range 10 {
		go func() {
			<-start
			if i%2 == 0 {
				done <- w.p.poll(t.Context())
				return
			}
			_, err := w.p.Cancel(t.Context(), w.accountA(), o.ID)
			if errors.Is(err, ErrNotWaiting) {
				err = nil
			}
			done <- err
		}()
	}
	close(start)
	for range 10 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if w.used(m.ID) != 0 || w.events(o.ID) != 3 {
		t.Fatalf("usage %d, events %d", w.used(m.ID), w.events(o.ID))
	}
}

func TestOrderRequestRequiresAccountUserAndIP(t *testing.T) {
	w := newWorld(t)
	r := CreateOrder{
		Account: w.account, User: w.user, Key: "valid", ReturnURL: "https://console.example/topup",
		ClientIP: netip.MustParseAddr("203.0.113.7"),
	}
	for _, mutate := range []func(*CreateOrder){
		func(r *CreateOrder) { r.Account = uuid.UUID{} },
		func(r *CreateOrder) { r.User = uuid.UUID{} },
		func(r *CreateOrder) { r.ClientIP = netip.Addr{} },
	} {
		bad := r
		mutate(&bad)
		if _, _, err := w.placeAs(w.accountA(), bad); err == nil {
			t.Fatal("invalid boundary request accepted")
		}
	}
}
