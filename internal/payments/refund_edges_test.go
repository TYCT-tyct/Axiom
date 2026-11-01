package payments

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestUnsentRefundCanBeRejectedWithoutSending(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund})
	r := w.mustRefund(o, o.Credits, "reject")
	r, err := w.p.RejectRefund(t.Context(), siteScope(w.siteA), r.ID, uuid.NewV7(), "request declined")
	if err != nil || r.State != "failed" {
		t.Fatal(r, err)
	}
	if n := w.count(w.p.platform, "SELECT frozen FROM billing.balances WHERE account_id=$1", w.account); n != 0 {
		t.Fatal(n)
	}
}

func TestConcurrentRefundRequestsSerializeAtTheOrder(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprint(sameKey), func(t *testing.T) {
			testConcurrentRefundRequests(t, sameKey)
		})
	}
}

func TestRefundCapabilityUpgradeCannotResendAnEarlierUnknown(t *testing.T) {
	var sends atomic.Int32
	plugin := fakePlugin{caps: CapRefund | CapQueryRefund, create: goodCheckout, refund: func(context.Context, RefundCall) (RefundState, error) {
		sends.Add(1)
		return RefundState{}, context.DeadlineExceeded
	}}
	w, o := refundWorld(t, plugin)
	r := w.approve(w.mustRefund(o, o.Credits, "upgrade"))
	plugin.caps = CapRefund | CapRefundKey
	w.p.plugins["fake"] = plugin
	w.p.mu.Lock()
	clear(w.p.channels)
	w.p.mu.Unlock()
	w.dueRefunds()
	if sends.Load() != 1 || w.mustGetRefund(r.ID).State != "pending" {
		t.Fatal("unknown refund was resent")
	}
	if _, err := w.p.RejectRefund(t.Context(), siteScope(w.siteA), r.ID, uuid.NewV7(), "not known"); !errors.Is(err, ErrRefundForbidden) {
		t.Fatal(err)
	}
}

func TestRefundStillUnknownAfter24HoursIsReported(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{
		caps:   CapRefund | CapQueryRefund,
		refund: func(context.Context, RefundCall) (RefundState, error) { return RefundState{State: RefundPending}, nil },
		queryRefund: func(context.Context, RefundCall) (RefundState, error) {
			return RefundState{}, errors.New("HTTP 503 / unparseable response")
		},
	})
	r := w.approve(w.mustRefund(o, o.Credits, "24h"))
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE kind='refund_unknown'"); n != 0 {
		t.Fatal(n)
	}
	w.exec("UPDATE payments.refunds SET sent_at=clock_timestamp()-interval '25 hours'")
	w.dueRefunds()
	w.dueRefunds()
	if w.mustGetRefund(r.ID).State != "pending" {
		t.Fatal("unknown became final")
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE kind='refund_unknown'"); n != 1 {
		t.Fatal(n)
	}
}

func TestRefundFinishedAtUsesTheLedgerClock(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) {
		return RefundState{State: RefundSucceeded}, nil
	}})
	w.exec(`CREATE FUNCTION billing.previous_refund_day() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='refund' THEN NEW.created_at := date_trunc('day',clock_timestamp())-interval '1 microsecond'; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER previous_refund_day BEFORE INSERT ON billing.transactions FOR EACH ROW EXECUTE FUNCTION billing.previous_refund_day()`)
	r := w.approve(w.mustRefund(o, o.Credits, "midnight"))
	if r.State != "succeeded" {
		t.Fatal(r)
	}
	if n := w.count(w.p.platform, `SELECT count(*) FROM payments.refunds r JOIN billing.transactions t ON t.request_id=r.order_id AND t.kind='refund' WHERE r.finished_at=t.created_at`); n != 1 {
		t.Fatal(n)
	}
	var differences []RecordDiscrepancyParams
	err := w.tx(w.p.platform, func(tx pg.Tx) (err error) {
		now, err := queries.RefundClock(t.Context(), tx)
		if err != nil {
			return err
		}
		differences, err = checkMerchant(t.Context(), tx, uuid.UUID(o.MerchantID), now.UTC().Truncate(24*time.Hour))
		return err
	})
	if err != nil || len(differences) != 0 {
		t.Fatal(differences, err)
	}
}

func TestStaleRefundResponseCannotReleaseOrSettleAgain(t *testing.T) {
	var w *world
	var observed PaymentsRefund
	plugin := fakePlugin{caps: CapRefund, refund: func(ctx context.Context, call RefundCall) (RefundState, error) {
		err := w.db.ReadTx(ctx, w.p.platform, func(tx pg.Tx) (err error) {
			rows, err := queries.ReadRefund(ctx, tx, uuid.UUID(call.Refund))
			if err == nil {
				observed = rows[0]
			}
			return err
		})
		return RefundState{}, errors.Join(context.DeadlineExceeded, err)
	}}
	var o Order
	w, o = refundWorld(t, plugin)
	r := w.approve(w.mustRefund(o, o.Credits, "late"))
	w.exec("UPDATE payments.refunds SET attempts=attempts+1")
	if err := w.p.saveRefundOutcome(t.Context(), observed, "succeeded", "late", false); err != nil {
		t.Fatal(err)
	}
	if w.mustGetRefund(r.ID).State != "pending" {
		t.Fatal("stale response moved money")
	}
}

func testConcurrentRefundRequests(t *testing.T, sameKey bool) {
	t.Helper()

	w, o := refundWorld(t, fakePlugin{caps: CapRefund})
	start := make(chan struct{})
	results := make(chan error, 10)
	for i := range 10 {
		go func() {
			<-start
			key := fmt.Sprint(i)
			if sameKey {
				key = "same"
			}
			_, err := w.p.Refund(t.Context(), w.accountA(), refundRequest(w, o, o.Credits/3, key))
			results <- err
		}()
	}
	close(start)
	accepted := 0
	for range 10 {
		err := <-results
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrRefundInFlight) {
			t.Fatal(err)
		}
	}
	want := 1
	if sameKey {
		want = 10
	}
	if accepted != want {
		t.Fatal(accepted)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.refunds"); n != 1 {
		t.Fatal(n)
	}
	if n := w.count(w.p.platform, "SELECT frozen FROM billing.balances WHERE account_id=$1", w.account); n != int(o.Credits/3) {
		t.Fatal(n)
	}
}

func TestExpiredRefundSenderDoesNotReachChannel(t *testing.T) {
	var calls atomic.Int32
	ch := fakeChannel{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) {
		calls.Add(1)
		return RefundState{State: RefundSucceeded}, nil
	}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := &Payments{}
	_, err := p.callRefund(ctx, ch, PaymentsRefund{}, Order{}, true, false, false)
	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatal(err, calls.Load())
	}
}

func TestRefundZeroCashAndExcessCreditsLeaveNoFreeze(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund})
	for i, amount := range []int64{1, o.Credits + 1} {
		_, err := w.p.Refund(t.Context(), w.accountA(), refundRequest(w, o, amount, fmt.Sprint(i)))
		if !errors.Is(err, ErrRefundAmount) {
			t.Fatal(err)
		}
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.refunds"); n != 0 {
		t.Fatal(n)
	}
	if n := w.count(w.p.platform, "SELECT frozen FROM billing.balances WHERE account_id=$1", w.account); n != 0 {
		t.Fatal(n)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM billing.transactions WHERE kind='freeze'"); n != 0 {
		t.Fatal(n)
	}
}

func TestRefundFrozenQuoteSurvivesPolicyChanges(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) {
		return RefundState{State: RefundSucceeded}, nil
	}})
	r := w.mustRefund(o, o.Credits, "frozen-policy")
	amount := r.Amount
	w.limits("refund_with_fee=true")
	r = w.approve(r)
	if r.State != "succeeded" || r.Amount != amount || r.Amount.Minor != o.Total.Minor-o.Fee {
		t.Fatal(r)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.orders WHERE refund_with_fee=false"); n != 1 {
		t.Fatal(n)
	}
}

func TestRefundNotFoundIsResentWithinTheKeyWindow(t *testing.T) {
	var sends, queries atomic.Int32
	plugin := fakePlugin{
		create: goodCheckout, caps: CapRefund | CapQueryRefund | CapRefundKey,
		refund: func(context.Context, RefundCall) (RefundState, error) {
			sends.Add(1)
			return RefundState{State: RefundSucceeded, ProviderRef: "re_resent"}, nil
		},
		queryRefund: func(context.Context, RefundCall) (RefundState, error) {
			queries.Add(1)
			return RefundState{}, ErrRefundNotFound
		},
	}
	w, o := refundWorld(t, plugin)
	r := w.mustRefund(o, o.Credits, "missing")
	// The process died after committing "sending" and before the request reached Stripe.
	w.exec("UPDATE payments.refunds SET approved_by='" + uuid.NewV7().String() + "',state='sending',sent_at=clock_timestamp(),attempts=1 WHERE id='" + uuid.UUID(r.ID).String() + "'")
	w.dueRefunds()
	if sends.Load() != 1 || queries.Load() != 1 {
		t.Fatalf("sends=%d queries=%d, want one of each", sends.Load(), queries.Load())
	}
	if got := w.mustGetRefund(r.ID); got.State != "succeeded" || got.ProviderRef != "re_resent" {
		t.Fatalf("refund = %+v", got)
	}
}

func TestRefundNotFoundAfterTheKeyWindowIsNotResent(t *testing.T) {
	var sends, queries atomic.Int32
	plugin := fakePlugin{
		create: goodCheckout, caps: CapRefund | CapQueryRefund | CapRefundKey,
		refund: func(context.Context, RefundCall) (RefundState, error) {
			sends.Add(1)
			return RefundState{State: RefundSucceeded}, nil
		},
		queryRefund: func(context.Context, RefundCall) (RefundState, error) {
			queries.Add(1)
			return RefundState{}, ErrRefundNotFound
		},
	}
	w, o := refundWorld(t, plugin)
	r := w.mustRefund(o, o.Credits, "old")
	w.exec("UPDATE payments.refunds SET approved_by='" + uuid.NewV7().String() + "',state='sending',sent_at=clock_timestamp()-interval '25 hours',attempts=1 WHERE id='" + uuid.UUID(r.ID).String() + "'")
	w.dueRefunds()
	if sends.Load() != 0 || queries.Load() != 1 {
		t.Fatalf("sends=%d queries=%d, want no resend after the key window", sends.Load(), queries.Load())
	}
	if got := w.mustGetRefund(r.ID); got.State != "pending" {
		t.Fatalf("refund = %+v, want pending", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state='open' AND kind='refund_unknown'"); got != 1 {
		t.Fatalf("unknown refund discrepancies = %d, want 1", got)
	}
}
