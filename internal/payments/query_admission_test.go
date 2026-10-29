package payments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/riverqueue/river"
)

func TestCallerCancellationCannotPauseMerchant(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			testCallerCancellation(t, cause)
		})
	}
}

func testCallerCancellation(t *testing.T, cause error) {
	t.Helper()
	entered := make(chan struct{})
	var recovering atomic.Bool
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(ctx context.Context, _, _ string) (Payment, error) {
		if recovering.Load() {
			return Payment{State: PayPending}, nil
		}
		entered <- struct{}{}
		<-ctx.Done()
		return Payment{}, ctx.Err()
	}})
	w.waiting()
	w.limits("pending_limit=10")
	for i := range 5 {
		o := w.mustPlace(yuan, fmt.Sprintf("cancel-%d", i))
		ctx, cancel := context.WithCancelCause(t.Context())
		done := make(chan error, 1)
		go func() { _, err := w.p.Verify(ctx, w.accountA(), o.ID); done <- err }()
		select {
		case <-entered:
		case err := <-done:
			cancel(cause)
			t.Fatal("query failed before caller could cancel", err)
		}
		cancel(cause)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal("caller cancellation lost", err)
		}
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.query_health WHERE failures<>0 OR paused_until>transaction_timestamp()") != 0 {
		t.Fatal("caller aborted queries changed gateway health")
	}
	if w.count(w.p.platform, "SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='payments.query_paused'") != 0 {
		t.Fatal("caller cancellation raised breaker alert")
	}
	recovering.Store(true)
	o := w.mustPlace(yuan, "after-aborts")
	if _, err := w.p.Verify(t.Context(), w.accountA(), o.ID); err != nil {
		t.Fatal("caller aborted merchant cannot query", err)
	}
}

func TestVerifyBusyDoesNotSpendAllowance(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	o := w.mustPlace(yuan, "verify-busy")
	var tokens []uuid.UUID
	for range 2 {
		token, err := w.p.acquireQuery(t.Context(), MerchantID(m.ID), false)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	if _, err := w.p.Verify(t.Context(), w.accountA(), o.ID); !errors.Is(err, ErrQueryPaused) {
		t.Fatal("busy query accepted", err)
	}
	for _, token := range tokens {
		if err := w.p.finishQuery(t.Context(), o.MerchantID, token, queryUnused); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.p.Verify(t.Context(), w.accountA(), o.ID); err != nil {
		t.Fatal("busy admission consumed verification allowance", err)
	}
}

func TestDailyQueryLeavesCapacityForCallback(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var background, foreground Order
	var calls atomic.Int32
	w := newTestWorld(t, fakePlugin{create: goodCheckout, verify: signedNotice, query: func(ctx context.Context, order, _ string) (Payment, error) {
		if order == background.ID.TypeID() {
			if calls.Add(1) > 1 {
				return Payment{State: PayPending}, nil
			}
			close(entered)
			select {
			case <-release:
				return Payment{State: PayPending}, nil
			case <-ctx.Done():
				return Payment{}, ctx.Err()
			}
		}
		return receipt(foreground).payment, nil
	}})
	m := w.waiting()
	background = w.mustPlace(yuan, "background")
	foreground = w.mustPlace(yuan, "foreground")
	w.exec("UPDATE payments.orders SET created_at='2000-01-01 12:00:00+00'")
	day := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	done := make(chan error, 1)
	var cursor uuid.UUID
	go func() {
		var err error
		cursor, err = w.p.reconcileQueries(t.Context(), m.ID, day, uuid.UUID{})
		done <- err
	}()
	<-entered
	// A second node/day cannot consume the foreground slot either.
	_, err := w.p.reconcileQueries(t.Context(), m.ID, day, uuid.UUID{})
	snooze, _ := errors.AsType[*river.JobSnoozeError](err)
	if !errors.Is(err, ErrQueryBusy) || snooze != nil {
		close(release)
		<-done
		t.Fatal("daily slot contention must spend a retry", err)
	}
	r := w.callback(foreground.MerchantID, http.MethodGet, callbackValues(foreground))
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r.Code != http.StatusOK {
		t.Fatal("daily query blocked callback confirmation", r.Code, r.Body)
	}
	w.assertCredit(foreground, foreground.Credits)
	if cursor != uuid.UUID(background.ID) {
		t.Fatal("completed daily query lost its continuation", cursor)
	}
	// Retrying the continuation cannot repeat the successful prefix.
	if _, err := w.p.reconcileQueries(t.Context(), m.ID, day, cursor); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("continuation repeated a completed order")
	}
}

func TestDailyContinuationAfterContention(t *testing.T) {
	var calls int
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) {
		calls++
		return Payment{State: PayPending}, nil
	}})
	m := w.waiting()
	w.mustPlace(yuan, "cursor-first")
	w.mustPlace(yuan, "cursor-second")
	w.exec("UPDATE payments.orders SET created_at='2000-01-01 12:00:00+00'")
	day := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	after, err := w.p.reconcileQueries(t.Context(), m.ID, day, uuid.UUID{})
	if err != nil || after == (uuid.UUID{}) {
		t.Fatal("first query lost its cursor", after, err)
	}
	token, err := w.p.acquireQuery(t.Context(), MerchantID(m.ID), true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.p.reconcileQueries(t.Context(), m.ID, day, after)
	snooze, _ := errors.AsType[*river.JobSnoozeError](err)
	if !errors.Is(err, ErrQueryBusy) || snooze != nil {
		t.Fatal("busy continuation avoided bounded retries", err)
	}
	if err := w.p.finishQuery(t.Context(), MerchantID(m.ID), token, queryUnused); err != nil {
		t.Fatal(err)
	}
	next, err := w.p.reconcileQueries(t.Context(), m.ID, day, after)
	if err != nil || next == after || next == (uuid.UUID{}) || calls != 2 {
		t.Fatal("contention replayed the completed prefix", next, calls, err)
	}
	w.exec("UPDATE payments.query_health SET paused_until=transaction_timestamp()+interval '5 minutes'")
	_, err = w.p.reconcileQueries(t.Context(), m.ID, day, uuid.UUID{})
	snooze, _ = errors.AsType[*river.JobSnoozeError](err)
	if snooze == nil {
		t.Fatal("open circuit should snooze without consuming a retry", err)
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
	w.exec("UPDATE payments.orders SET created_at='2000-01-01 12:00:00+00'")
	day := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if next, err := w.p.reconcileQueries(t.Context(), m.ID, day, uuid.UUID{}); !errors.Is(err, ErrUnconfirmed) || next != (uuid.UUID{}) {
		t.Fatalf("unknown page was completed: %s, %v", next, err)
	}
	recovered.Store(true)
	if _, err := w.p.reconcileQueries(t.Context(), m.ID, day, uuid.UUID{}); err != nil {
		t.Fatal("recovered page did not complete", err)
	}
}

func TestCoreQueryDeadlineCountsAsUnknown(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) { return Payment{}, context.DeadlineExceeded }})
	m := w.waiting()
	o := w.mustPlace(yuan, "core-deadline")
	ch, err := w.p.channelOf(t.Context(), o.MerchantID)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		token, err := w.p.acquireQuery(t.Context(), o.MerchantID, false)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(errQueryDeadline)
		_, err = w.p.completeQuery(ctx, ch, o.MerchantID, token, o.ID.TypeID(), o.TradeNo)
		if !errors.Is(err, ErrUnconfirmed) {
			t.Fatal("core query timeout lost its unknown outcome", err)
		}
	}
	if _, err := w.p.acquireQuery(t.Context(), MerchantID(m.ID), false); !errors.Is(err, ErrQueryPaused) {
		t.Fatal("core gateway timeouts no longer trip breaker", err)
	}
}
