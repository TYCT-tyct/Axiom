package payments

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func refundWorld(t *testing.T, plugin fakePlugin) (*world, Order) {
	t.Helper()
	plugin.create = goodCheckout
	return refundWorldOn(t, newTestWorld(t, plugin))
}

// refundWorldWithPool is refundWorld on a world built with a particular pool and suffix.
func refundWorldWithPool(t *testing.T, plugin fakePlugin, suffix string, opts ...pg.Option) (*world, Order) {
	t.Helper()
	plugin.create = goodCheckout
	return refundWorldOn(t, newTestWorldWithPool(t, plugin, suffix, opts...))
}

// refundWorldOn prepares a paid order in a world the caller built with a particular pool.
func refundWorldOn(t *testing.T, w *world) (*world, Order) {
	t.Helper()
	m := w.waiting()
	w.exec(fmt.Sprintf("UPDATE payments.merchants SET refund_enabled = true, user_refund = true WHERE id = '%s'", m.ID))
	o := w.mustPlace(10*yuan, "paid")
	if err := w.p.receive(t.Context(), receipt(o)); err != nil {
		t.Fatal(err)
	}
	return w, o
}

func refundRequest(w *world, o Order, amount int64, key string) RefundRequest {
	return RefundRequest{Order: o.ID, Credits: amount, Key: key, User: w.user, Reason: "unused credits", Via: "channel"}
}

func (w *world) mustRefund(o Order, amount int64, key string) Refund {
	w.t.Helper()
	r, err := w.p.Refund(w.t.Context(), w.accountA(), refundRequest(w, o, amount, key))
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func (w *world) approve(r Refund) Refund {
	w.t.Helper()
	result, err := w.p.ApproveRefund(w.t.Context(), siteScope(w.siteA), r.ID, uuid.NewV7(), "approved")
	if err != nil {
		w.t.Fatal(err)
	}
	return result
}

func (w *world) dueRefunds() {
	w.t.Helper()
	w.exec("UPDATE payments.refunds SET next_query_at = clock_timestamp() - interval '1 second'")
	if err := w.p.pollRefunds(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
}

func TestRefundTimeoutQueriesAndAtomicFailure(t *testing.T) {
	for _, outcome := range []RefundOutcome{RefundSucceeded, RefundFailed} {
		t.Run(fmt.Sprint(outcome), func(t *testing.T) {
			testRefundQueryOutcome(t, outcome)
		})
	}
}

func TestRefundCrashBoundariesAndIdempotency(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		t.Run(fmt.Sprint(keyed), func(t *testing.T) {
			testRefundFrozenCrash(t, keyed)
		})
	}
}

func TestSendingCrashNeverResendsAnUnknownUnkeyedRefund(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		t.Run(fmt.Sprint(keyed), func(t *testing.T) {
			testRefundSendingCrash(t, keyed)
		})
	}
}

func TestRefundRequiresSeparateApprovalAndScopedAccess(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) {
		return RefundState{State: RefundSucceeded}, nil
	}})
	r := w.mustRefund(o, o.Credits, "approval")
	if _, err := w.p.GetRefund(t.Context(), w.accountB(), r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := w.p.ApproveRefund(t.Context(), w.accountA(), r.ID, uuid.NewV7(), "approved"); !errors.Is(err, ErrRefundForbidden) {
		t.Fatal(err)
	}
	if _, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, w.user, "approved"); !errors.Is(err, ErrSecondPerson) {
		t.Fatal(err)
	}
	w.dueRefunds()
	got := w.mustGetRefund(r.ID)
	if got.State != "frozen" {
		t.Fatal(got)
	}
	w.approve(r)
}

func testRefundQueryOutcome(t *testing.T, outcome RefundOutcome) {
	t.Helper()

	var sends, queries atomic.Int32
	w, o := refundWorld(t, fakePlugin{
		caps: CapRefund | CapQueryRefund,
		refund: func(context.Context, RefundCall) (RefundState, error) {
			sends.Add(1)
			return RefundState{}, context.DeadlineExceeded
		},
		queryRefund: func(context.Context, RefundCall) (RefundState, error) {
			queries.Add(1)
			return RefundState{State: outcome, ProviderRef: "proof"}, nil
		},
	})
	r := w.approve(w.mustRefund(o, o.Credits, "refund"))
	if r.State != "pending" {
		t.Fatal(r)
	}
	if w.count(w.p.platform, "SELECT frozen FROM billing.balances WHERE account_id=$1", w.account) != int(o.Credits) {
		t.Fatal("freeze lost")
	}
	w.dueRefunds()
	got, err := w.p.GetRefund(t.Context(), w.accountA(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := "succeeded"
	if outcome == RefundFailed {
		want = "failed"
	}
	assertRefundTradeNumber(w, o)

	if got.State != want || sends.Load() != 1 || queries.Load() != 1 {
		t.Fatalf("%+v sends=%d queries=%d", got, sends.Load(), queries.Load())
	}
	if n := w.count(w.p.platform, "SELECT frozen FROM billing.balances WHERE account_id=$1", w.account); n != 0 {
		t.Fatal(n)
	}
	balance := w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id=$1", w.account)
	balances := map[RefundOutcome]int{RefundSucceeded: 0, RefundFailed: int(o.Credits)}
	if balance != balances[outcome] {
		t.Fatal(balance)
	}
	w.dueRefunds()
	if sends.Load() != 1 || queries.Load() != 1 {
		t.Fatal("terminal replay called channel")
	}
}

func testRefundFrozenCrash(t *testing.T, keyed bool) {
	t.Helper()

	var calls []RefundID
	var mu sync.Mutex
	caps := CapRefund
	if keyed {
		caps |= CapRefundKey
	}
	w, o := refundWorld(t, fakePlugin{caps: caps, refund: func(_ context.Context, r RefundCall) (RefundState, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.Refund)
		return RefundState{State: RefundSucceeded}, nil
	}})
	r := w.mustRefund(o, o.Credits, "crash")
	again, err := w.p.Refund(t.Context(), w.accountA(), refundRequest(w, o, o.Credits, "crash"))
	if err != nil || again.ID != r.ID {
		t.Fatal(again, err)
	}
	// Stop after the first transaction and the approval; no sending state was committed.
	w.exec(fmt.Sprintf("UPDATE payments.refunds SET approved_by = '%s'", uuid.NewV7()))
	w.dueRefunds()
	if len(calls) != 1 || calls[0] != r.ID {
		t.Fatal(calls)
	}
	replay, err := w.p.Refund(t.Context(), w.accountA(), refundRequest(w, o, o.Credits, "crash"))
	if err != nil || replay.State != "succeeded" {
		t.Fatal(replay, err)
	}
}

func testRefundSendingCrash(t *testing.T, keyed bool) {
	t.Helper()

	var calls, executions atomic.Int32
	var original RefundID
	caps := CapRefund
	if keyed {
		caps |= CapRefundKey
	}
	w, o := refundWorld(t, fakePlugin{caps: caps, refund: func(_ context.Context, call RefundCall) (RefundState, error) {
		calls.Add(1)
		if !keyed || call.Refund != original {
			executions.Add(1)
		}
		return RefundState{State: RefundSucceeded}, nil
	}})
	r := w.mustRefund(o, o.Credits, "crash-sending")
	original = r.ID
	executions.Store(1) // the channel executed the first send before the process crashed

	w.exec(fmt.Sprintf("UPDATE payments.refunds SET approved_by='%s',state='sending',sent_at=clock_timestamp()-interval '25 hours',attempts=1", uuid.NewV7()))
	w.dueRefunds()
	w.dueRefunds()
	if executions.Load() != 1 {
		t.Fatal("recovery executed the refund twice", executions.Load())
	}
	got, err := w.p.GetRefund(t.Context(), w.accountA(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if keyed {
		if got.State != "succeeded" {
			t.Fatal(got, calls.Load())
		}
	} else {
		if calls.Load() != 0 || got.State != "pending" {
			t.Fatal(got, calls.Load())
		}
		if n := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE kind='refund_unknown'"); n != 1 {
			t.Fatal(n)
		}
		if _, err := w.p.Refund(t.Context(), w.accountA(), refundRequest(w, o, 1, "second")); !errors.Is(err, ErrRefundInFlight) {
			t.Fatal(err)
		}
	}
}

func (w *world) mustGetRefund(id RefundID) Refund {
	w.t.Helper()
	r, err := w.p.GetRefund(w.t.Context(), w.accountA(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func assertRefundTradeNumber(w *world, o Order) {
	w.t.Helper()
	t := w.t
	order, err := w.p.GetOrder(t.Context(), w.accountA(), o.ID)
	if err != nil || order.TradeNo != o.TradeNo {
		t.Fatal("refund replaced the payment trade number", order, err)
	}
}
