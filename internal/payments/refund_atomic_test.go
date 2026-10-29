package payments

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"uuid"
)

func TestRefundConcurrentApprovalSendsOnce(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return RefundState{State: RefundSucceeded}, nil
	}})
	r := w.mustRefund(o, o.Credits, "concurrent")
	done := make(chan error, 1)
	go func() { _, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, uuid.NewV7()); done <- err }()
	<-entered
	_, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, uuid.NewV7())
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestRefundOutcomeAndLedgerRollBackTogether(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			testRefundOutcomeRollback(t, fail)
		})
	}
}

func TestRefundFailedFirstAttemptDoesNotFixFeePolicy(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) {
		return RefundState{}, &Rejected{Code: "refused"}
	}})
	r := w.approve(w.mustRefund(o, o.Credits, "failure"))
	if r.State != "failed" {
		t.Fatal(r)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.orders WHERE refund_with_fee IS NOT NULL"); n != 0 {
		t.Fatal(n)
	}
	w.limits("refund_with_fee=true")
	next := w.mustRefund(o, o.Credits, "retry")
	if next.Amount.Minor != o.Total.Minor {
		t.Fatal(next)
	}
	changed := refundRequest(w, o, o.Credits-1, "retry")
	if _, err := w.p.Refund(t.Context(), w.accountA(), changed); !errors.Is(err, ErrKeyReused) {
		t.Fatal(err)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.refunds WHERE id=$1", uuid.UUID(next.ID)); n != 1 {
		t.Fatal(n)
	}
}

func testRefundOutcomeRollback(t *testing.T, fail bool) {
	t.Helper()

	var calls atomic.Int32
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) {
		calls.Add(1)
		if fail {
			return RefundState{}, &Rejected{Code: "refused"}
		}
		return RefundState{State: RefundSucceeded}, nil
	}})
	r := w.mustRefund(o, o.Credits, "atomic")
	// Reject at COMMIT, after both ledger and refund state have been written.
	w.exec(`CREATE FUNCTION payments.reject_refund_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected refund commit failure'; END $$`,
		`CREATE CONSTRAINT TRIGGER reject_refund_commit AFTER UPDATE ON payments.refunds DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.state IN ('succeeded','failed')) EXECUTE FUNCTION payments.reject_refund_commit()`)
	_, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, uuid.NewV7())
	if err == nil {
		t.Fatal("commit did not fail")
	}
	got, err := w.p.GetRefund(t.Context(), w.accountA(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "sending" || calls.Load() != 1 {
		t.Fatal(got, calls.Load())
	}
	if n := w.count(w.p.platform, "SELECT frozen FROM billing.balances WHERE account_id=$1", w.account); n != int(o.Credits) {
		t.Fatal(n)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM billing.transactions WHERE kind IN ('refund','unfreeze')"); n != 0 {
		t.Fatal(n)
	}
	w.exec("DROP TRIGGER reject_refund_commit ON payments.refunds")
	w.dueRefunds()
	got = w.mustGetRefund(r.ID)
	if got.State != "pending" || calls.Load() != 1 {
		t.Fatal(got, calls.Load())
	}
}
