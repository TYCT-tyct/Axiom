package payments

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
	"uuid"
)

func TestRefundReviewCallerDeadlinePreservesDefinitiveOutcome(t *testing.T) {
	for _, poll := range []bool{false, true} {
		for _, rejected := range []bool{false, true} {
			t.Run(fmt.Sprintf("poll=%t/rejected=%t", poll, rejected), func(t *testing.T) { testRefundCallerDeadline(t, poll, rejected) })
		}
	}
}

func testRefundCallerDeadline(t *testing.T, poll, rejected bool) {
	t.Helper()
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	var interrupted bool
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: func(callCtx context.Context, _ RefundCall) (RefundState, error) {
		cancel(context.DeadlineExceeded)
		interrupted = callCtx.Err() != nil
		if rejected {
			return RefundState{}, &Rejected{Code: "refused"}
		}
		return RefundState{State: RefundSucceeded}, nil
	}})
	r := w.mustRefund(o, o.Credits, "caller-deadline")
	var err error
	if poll {
		w.exec(fmt.Sprintf("UPDATE payments.refunds SET approved_by='%s',next_query_at=clock_timestamp()-interval '1 second'", uuid.NewV7()))
		err = w.p.pollRefunds(ctx)
	} else {
		_, err = w.p.ApproveRefund(ctx, siteScope(w.siteA), r.ID, uuid.NewV7(), "approved")
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	want := "succeeded"
	if rejected {
		want = "failed"
	}
	if got := w.mustGetRefund(r.ID); got.State != want {
		t.Fatalf("definitive %s lost after caller deadline: %+v", want, got)
	}
	if interrupted {
		t.Fatal("claimed channel call inherited caller cancellation")
	}
	if n := w.count(w.p.platform, "SELECT frozen FROM billing.balances WHERE account_id=$1", w.account); n != 0 {
		t.Fatal(n)
	}
}

func TestRefundReviewQueryHealthClassifiesFailures(t *testing.T) {
	for _, tc := range []struct {
		name              string
		cause, errorValue error
		unknown           bool
	}{
		{"rejected", nil, &Rejected{Code: "not_found"}, false},
		{"cancelled", context.Canceled, context.Canceled, false},
		{"round deadline", context.DeadlineExceeded, context.DeadlineExceeded, false},
		{"gateway deadline", errQueryDeadline, context.DeadlineExceeded, true},
	} {
		t.Run(tc.name, func(t *testing.T) { testRefundQueryHealth(t, tc.cause, tc.errorValue, tc.unknown) })
	}
}

func testRefundQueryHealth(t *testing.T, cause, reply error, unknown bool) {
	t.Helper()
	w, o := refundWorld(t, fakePlugin{caps: CapRefund})
	r := w.mustRefund(o, o.Credits, "query-health")
	row, _, err := w.p.refundAccess(t.Context(), w.p.platform, r.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	initial := 2
	if unknown {
		initial = 0
	}
	w.exec(fmt.Sprintf("INSERT INTO payments.query_health(merchant_id,site_id,failures) VALUES ('%s','%s',%d)", uuid.UUID(o.MerchantID), w.siteA, initial))
	for range 5 {
		ctx, cancel := context.WithCancelCause(t.Context())
		ch := fakeChannel{caps: CapQueryRefund, queryRefund: func(context.Context, RefundCall) (RefundState, error) {
			if cause != nil {
				cancel(cause)
			}
			return RefundState{}, reply
		}}
		_, callErr := w.p.callRefund(ctx, ch, row, o, false, true, false)
		cancel(nil)
		if !errors.Is(callErr, reply) {
			t.Fatal(callErr)
		}
	}
	paused := w.count(w.p.platform, "SELECT count(*) FROM payments.query_health WHERE paused_until>clock_timestamp()")
	want := 0
	if unknown {
		want = 1
	}
	failures := 0
	if cause != nil && !unknown {
		failures = initial
	}
	if n := w.count(w.p.platform, "SELECT failures FROM payments.query_health"); n != failures {
		t.Fatal("wrong failure counter", n, failures)
	}
	if paused != want {
		t.Fatalf("gateway health count=%d want=%d", paused, want)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='payments.query_paused'"); n != want {
		t.Fatal(n)
	}
}

func TestRefundReviewBudgetReservesWholeCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), refundCallTimeout)
		defer cancel()
		if !refundHasBudget(ctx) {
			t.Fatal("a full call budget was refused")
		}
		time.Sleep(time.Nanosecond)
		if refundHasBudget(ctx) {
			t.Fatal("a shortened call budget was claimed")
		}
		cancel()
		if refundHasBudget(ctx) {
			t.Fatal("cancelled work was claimed")
		}
		if !refundHasBudget(t.Context()) {
			t.Fatal("unbounded work was refused")
		}
	})
}
