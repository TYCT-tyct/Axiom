package payments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

func TestPollBackoffDuringValidity(t *testing.T) {
	var calls atomic.Int32
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) {
		calls.Add(1)
		return Payment{State: PayPending}, nil
	}})
	w.waiting()
	o := w.mustPlace(yuan, "backoff")
	delays := []int{60, 120, 240, 480, 600, 600, 600}
	for i, want := range delays {
		w.exec("UPDATE payments.orders SET next_query_at=transaction_timestamp()-interval '1 second'")
		for range 2 {
			if err := w.p.queryPending(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if got := calls.Load(); got != int32(i+1) {
			t.Fatalf("backoff query %d called channel %d times", i, got)
		}
		var delay int
		err := w.tx(w.p.platform, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "SELECT query_delay_seconds FROM payments.orders WHERE id=$1", uuid.UUID(o.ID)).Scan(&delay)
		})
		if err != nil || delay != want {
			t.Fatalf("delay %d, want %d: %v", delay, want, err)
		}
	}
}

func TestPollSlowMerchantDoesNotBlockAnother(t *testing.T) {
	healthy := make(chan struct{})
	var slowOrder Order
	var fastOrder Order
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(ctx context.Context, order, _ string) (Payment, error) {
		if order == slowOrder.ID.TypeID() {
			select {
			case <-healthy:
				return Payment{}, context.DeadlineExceeded
			case <-ctx.Done():
				return Payment{}, ctx.Err()
			}
		}
		close(healthy)
		return receipt(fastOrder).payment, nil
	}})
	slow := w.waiting()
	slowOrder = w.mustPlace(yuan, "slow")
	in := w.input(w.siteA)
	in.ID = MerchantID(slow.ID)
	in.Rev = slow.Rev
	in.Enabled = false
	in.Secrets = nil
	w.mustSave(siteScope(w.siteA), in)
	w.mustSave(siteScope(w.siteA), w.input(w.siteA))
	fastOrder = w.mustPlace(yuan, "healthy")
	w.exec("UPDATE payments.orders SET next_query_at=transaction_timestamp()-interval '1 second'")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := w.p.queryPending(ctx); err != nil {
		t.Fatal(err)
	}
	w.assertCredit(fastOrder, fastOrder.Credits)
	w.assertCredit(slowOrder, 0)
}

func TestMerchantBreakerPersistsAndRecovers(t *testing.T) {
	var mode atomic.Int32
	var calls atomic.Int32
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) {
		calls.Add(1)
		switch mode.Load() {
		case 1:
			return Payment{}, &Rejected{Code: "missing"}
		case 2:
			return Payment{State: PayPending}, nil
		default:
			return Payment{}, context.DeadlineExceeded
		}
	}})
	w.waiting()
	o := w.mustPlace(yuan, "breaker")
	check := func() {
		t.Helper()
		if err := w.p.queryOrder(t.Context(), o); !errors.Is(err, ErrUnconfirmed) {
			t.Fatalf("unknown query: %v", err)
		}
	}
	for range 4 {
		check()
	}
	mode.Store(1)
	check() // A definite response breaks the unknown streak.
	mode.Store(0)
	for range 5 {
		check()
	}
	if err := w.p.queryOrder(t.Context(), o); !errors.Is(err, ErrQueryPaused) {
		t.Fatalf("open breaker allowed call: %v", err)
	}
	if calls.Load() != 10 || w.count(w.p.platform, "SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='payments.query_paused'") != 1 {
		t.Fatal("breaker did not persist one alert")
	}
	assertBreakerRecovery(w, o, &mode)
}

func assertBreakerRecovery(w *world, o Order, mode *atomic.Int32) {
	t := w.t
	t.Helper()
	// A restarted module shares the pause, and local expiry still releases the reservation.
	restarted, err := Open(Config{DB: w.db, Platform: w.p.platform, Keyring: w.p.keyring, Plugins: []Plugin{w.p.plugins["fake"]}})
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.queryOrder(t.Context(), o); !errors.Is(err, ErrQueryPaused) {
		t.Fatal("restart lost breaker", err)
	}
	w.exec("UPDATE payments.orders SET expires_at=transaction_timestamp()-interval '1 minute'")
	if err = w.p.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := w.stored(o.ID); state != "expired" {
		t.Fatal("breaker held local expiry", state)
	}
	w.exec("UPDATE payments.query_health SET paused_until=transaction_timestamp()-interval '1 minute'")
	mode.Store(2)
	if err = restarted.queryOrder(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='payments.query_paused' AND body->>'resolved'='true'") != 1 {
		t.Fatal("successful probe did not resolve pause alert")
	}
}

func TestExpiredUnclosablePaymentIsFoundInWindow(t *testing.T) {
	var paid atomic.Bool
	var o Order
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) {
		if paid.Load() {
			return receipt(o).payment, nil
		}
		return Payment{State: PayPending}, nil
	}})
	m := w.waiting()
	o = w.mustPlace(yuan, "late-unclosable")
	w.exec("UPDATE payments.orders SET expires_at=transaction_timestamp()-interval '1 minute'")
	if err := w.p.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := w.stored(o.ID); state != "expired" || w.used(m.ID) != 0 {
		t.Fatal("unpaid order did not expire")
	}
	paid.Store(true)
	w.exec("UPDATE payments.orders SET next_query_at=transaction_timestamp()-interval '1 second'")
	if err := w.p.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.assertCredit(o, o.Credits)
	if w.used(m.ID) != o.Total.Minor {
		t.Fatal("late receipt did not restore usage")
	}
}

func TestDailyReconcileDifferencesAreDurableAndDeduplicated(t *testing.T) {
	for _, kind := range []string{"ledger", "usage", "batch", "state"} {
		t.Run(kind, func(t *testing.T) { testDailyDifference(t, kind) })
	}
}

func testDailyDifference(t *testing.T, kind string) {
	t.Helper()
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	o := w.mustPlace(yuan, "daily")
	w.receive(receipt(o))
	day := time.Now().UTC().Truncate(24 * time.Hour)
	if err := w.p.reconcileMerchant(t.Context(), m.ID, day); err != nil {
		t.Fatal(err)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 0 {
		t.Fatal("consistent payment reported as mismatch")
	}
	mutations := map[string]string{"ledger": "UPDATE billing.entries SET amount=amount+1 WHERE book='cash'", "usage": "UPDATE payments.merchant_usage SET used=used+1", "batch": "UPDATE payments.orders SET paid_fee=paid_fee+1", "state": "UPDATE payments.orders SET state='pending'"}
	w.exec(mutations[kind])
	for range 2 {
		if err := w.p.reconcileMerchant(t.Context(), m.ID, day); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies"); n != 1 {
		t.Fatalf("same difference produced %d rows", n)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='payments.discrepancy'") != 1 {
		t.Fatal("difference missing alert or alerted twice")
	}
	d := w.pending(w.p.platform, uuid.UUID{}, "open")[0]
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		_, err := InvestigateDiscrepancy(t.Context(), tx, d.ID, "checked channel evidence", audit.User(w.user))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.p.reconcileMerchant(t.Context(), m.ID, day); err != nil {
		t.Fatal(err)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 1 {
		t.Fatal("investigated difference reopened as a second row")
	}
	w.assertCredit(o, o.Credits)
}

func TestDiscrepancyAlertAndInvestigationRollbackTogether(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	o := w.mustPlace(yuan, "atomic-alert")
	w.exec(`CREATE FUNCTION payments.refuse_alert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'alert unavailable'; END $$`,
		`CREATE TRIGGER refuse_alert BEFORE INSERT ON platform.alert_signals FOR EACH ROW EXECUTE FUNCTION payments.refuse_alert()`)
	c := receipt(o)
	c.payment.Paid.Minor++
	if err := w.p.receive(t.Context(), c); err == nil {
		t.Fatal("payment ignored signal persistence failure")
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 0 {
		t.Fatal("difference committed without its alert")
	}
	w.exec("DROP TRIGGER refuse_alert ON platform.alert_signals")
	w.receive(c)
	var d Discrepancy
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		rows, err := ListDiscrepancies(t.Context(), tx, uuid.UUID{}, "")
		if err != nil {
			return err
		}
		d = rows[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("rollback investigation")
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		_, err := InvestigateDiscrepancy(t.Context(), tx, d.ID, "checked", audit.User(w.user))
		if err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state='open' AND note IS NULL") != 1 {
		t.Fatal("rolled back investigation persisted")
	}
	if w.count(w.p.platform, "SELECT count(*) FROM platform.alert_signals WHERE body->>'resolved'='true'") != 0 {
		t.Fatal("rolled back investigation resolved alert")
	}
}

func TestDiscrepancyScopeAndPagination(t *testing.T) {
	w := newWorld(t)
	a := w.mustSave(siteScope(w.siteA), w.input(w.siteA))
	b := w.mustSave(siteScope(w.siteB), w.input(w.siteB))
	for _, m := range []Merchant{a, b} {
		w.seedDifferences(m.ID)
	}
	rows := w.pending(siteScope(w.siteA), uuid.UUID{}, "")
	if len(rows) != 100 {
		t.Fatalf("page=%d", len(rows))
	}
	for _, r := range rows {
		if r.Merchant != a.ID {
			t.Fatal("cross-site record leaked")
		}
	}
	if len(w.pending(siteScope(w.siteA), rows[99].ID, "")) != 1 {
		t.Fatal("cursor lost or repeated rows")
	}
	if len(w.pending(w.accountA(), uuid.UUID{}, "")) != 0 {
		t.Fatal("account read merchant-wide discrepancy")
	}
}

func (w *world) pending(scope pg.Scope, before uuid.UUID, state string) []Discrepancy {
	w.t.Helper()
	var rows []Discrepancy
	if err := w.tx(scope, func(tx pg.Tx) (err error) {
		rows, err = ListDiscrepancies(w.t.Context(), tx, before, state)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return rows
}

func (w *world) seedDifferences(merchant uuid.UUID) {
	w.t.Helper()
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		for i := range 101 {
			r, err := pendingDifference(merchant, "usage", fmt.Sprint(i), map[string]any{"day": "test"})
			if err != nil {
				return err
			}
			if _, err = recordPending(w.t.Context(), tx, r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		w.t.Fatal(err)
	}
}

func TestDailyQueryFindsChangedPaidAndUnfinishedOrders(t *testing.T) {
	var paid Order
	var queried atomic.Int32
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(_ context.Context, order, _ string) (Payment, error) {
		queried.Add(1)
		if order == paid.ID.TypeID() {
			return receipt(paid).payment, nil
		}
		return Payment{State: PayPending}, nil
	}})
	m := w.waiting()
	paid = w.mustPlace(yuan, "daily-lost")
	w.mustPlace(yuan, "daily-pending")
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	// Daily fallback does not consume the poll backoff: even a next_query_at tomorrow is checked.
	w.exec("UPDATE payments.orders SET created_at=transaction_timestamp()-interval '1 day', updated_at=transaction_timestamp()-interval '1 day', next_query_at=transaction_timestamp()+interval '1 day'")
	if next, err := w.p.reconcileQueries(t.Context(), m.ID, day, uuid.UUID{}); err != nil || next != (uuid.UUID{}) {
		t.Fatal("daily queries", next, err)
	}
	w.assertCredit(paid, paid.Credits)
	if queried.Load() != 2 {
		t.Fatal("daily fallback skipped an order because of poll backoff")
	}
}

func TestDailyCashUsesOneAccountingDayAcrossMidnight(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	o := w.mustPlace(yuan, "midnight")
	w.receive(receipt(o))
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	// A transaction starts its credit before midnight and updates the order after midnight.
	w.exec("UPDATE billing.credit_batches SET created_at=(date_trunc('day',transaction_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')-interval '1 microsecond'")
	for _, d := range []time.Time{day, day.Add(24 * time.Hour)} {
		if err := w.p.reconcileMerchant(t.Context(), m.ID, d); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.pending(w.p.platform, uuid.UUID{}, "")) != 0 {
		t.Fatal("a midnight receipt created a false daily cash discrepancy")
	}
}

func TestDailyQueryPagesContinueWithoutRepeatingOrders(t *testing.T) {
	var calls atomic.Int32
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) {
		calls.Add(1)
		return Payment{State: PayPending}, nil
	}})
	m := w.waiting()
	o := w.mustPlace(yuan, "daily-pages")
	w.exec(fmt.Sprintf(`INSERT INTO payments.orders
 SELECT (jsonb_populate_record(NULL::payments.orders,to_jsonb(o)||jsonb_build_object('id',gen_random_uuid(),'idem_key','page-'||n))).*
 FROM payments.orders o CROSS JOIN generate_series(1,204) n WHERE o.id='%s'`, uuid.UUID(o.ID)),
		"UPDATE payments.orders SET created_at=transaction_timestamp()-interval '1 day'")
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	var after uuid.UUID
	for _, want := range []int32{100, 200, 205} {
		next, err := w.p.reconcileQueries(t.Context(), m.ID, day, after)
		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != want {
			t.Fatalf("daily page called %d times, want %d", calls.Load(), want)
		}
		after = next
	}
	if after != (uuid.UUID{}) {
		t.Fatal("last page did not finish")
	}
}

func TestPollDiscoverySharesCapacityBetweenMerchants(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	a := w.waiting()
	o := w.mustPlace(yuan, "poll-backlog")
	w.exec(fmt.Sprintf(`INSERT INTO payments.orders
 SELECT (jsonb_populate_record(NULL::payments.orders,to_jsonb(o)||jsonb_build_object('id',gen_random_uuid(),'idem_key','backlog-'||n))).*
 FROM payments.orders o CROSS JOIN generate_series(1,104) n WHERE o.id='%s'`, uuid.UUID(o.ID)))
	in := w.input(w.siteA)
	in.ID, in.Rev, in.Enabled, in.Secrets = MerchantID(a.ID), a.Rev, false, nil
	w.mustSave(siteScope(w.siteA), in)
	b := w.mustSave(siteScope(w.siteA), w.input(w.siteA))
	w.limits("pending_limit=200")
	w.mustPlace(yuan, "poll-other")
	w.exec("UPDATE payments.orders SET next_query_at=transaction_timestamp()-interval '1 second'")
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		rows, err := queries.QueryOrders(t.Context(), tx, 100)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.MerchantID == b.ID {
				return nil
			}
		}
		return errors.New("one merchant backlog consumed the entire discovery page")
	}); err != nil {
		t.Fatal(err)
	}
}

func TestChangedDifferenceAfterInvestigationIsNotLost(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	w.mustPlace(yuan, "changed-difference")
	day := time.Now().UTC().Truncate(24 * time.Hour)
	w.exec("UPDATE payments.merchant_usage SET used=used+1")
	if err := w.p.reconcileMerchant(t.Context(), m.ID, day); err != nil {
		t.Fatal(err)
	}
	d := w.pending(w.p.platform, uuid.UUID{}, "open")[0]
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		_, err := InvestigateDiscrepancy(t.Context(), tx, d.ID, "checked first difference", audit.User(w.user))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w.exec("UPDATE payments.merchant_usage SET used=used+1")
	if err := w.p.reconcileMerchant(t.Context(), m.ID, day); err != nil {
		t.Fatal(err)
	}
	if len(w.pending(w.p.platform, uuid.UUID{}, "")) != 2 || len(w.pending(w.p.platform, uuid.UUID{}, "open")) != 1 {
		t.Fatal("changed difference was hidden by old investigation")
	}
}

func TestDiscrepancyAmountStaysExactAndDuplicateLogsLocateOneRecord(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	o := w.mustPlace(yuan, "exact-pending-amount")
	c := receipt(o)
	c.payment.Paid.Minor = 9007199254740993
	for range 2 {
		w.receive(c)
	}
	d := w.pending(w.p.platform, uuid.UUID{}, "open")[0]
	if d.Detail["amount"] != "9007199254740993" {
		t.Fatalf("pending amount lost digits: %v", d.Detail["amount"])
	}
	logs := w.logs.String()
	if strings.Count(logs, "confirmed payment needs attention") != 1 || !strings.Contains(logs, d.ID.String()) {
		t.Fatal("duplicate receipt logged an unpersisted discrepancy ID", logs)
	}
}

func TestDailyQueryRetainsUnknownPage(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprint(incomplete), func(t *testing.T) { testDailyUnknown(t, incomplete) })
	}
}

func testDailyUnknown(t *testing.T, incomplete bool) {
	t.Helper()
	var recovered atomic.Bool
	unit := currency(t, "CNY")
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) {
		if recovered.Load() {
			return Payment{State: PayPending}, nil
		}
		if incomplete {
			return Payment{State: PayPaid, TradeNo: "incomplete", Paid: Money{Currency: unit}}, nil
		}
		return Payment{}, context.DeadlineExceeded
	}})
	m := w.waiting()
	w.mustPlace(yuan, "retry-daily-page")
	w.exec("UPDATE payments.orders SET created_at=transaction_timestamp()-interval '1 day'")
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	if next, err := w.p.reconcileQueries(t.Context(), m.ID, day, uuid.UUID{}); !errors.Is(err, ErrUnconfirmed) || next != (uuid.UUID{}) {
		t.Fatalf("unknown page was completed: %s, %v", next, err)
	}
	recovered.Store(true)
	if _, err := w.p.reconcileQueries(t.Context(), m.ID, day, uuid.UUID{}); err != nil {
		t.Fatal("recovered page did not complete", err)
	}
}
