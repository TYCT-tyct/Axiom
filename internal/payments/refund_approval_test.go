package payments

import (
	"context"
	"sync/atomic"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func refundWorldAmount(t *testing.T, plugin fakePlugin, credits int64) (*world, Order) {
	t.Helper()
	plugin.create = goodCheckout
	w := newTestWorld(t, plugin)
	m := w.waiting()
	w.exec("UPDATE payments.settings SET first_credits = 1000000000000 WHERE site_id = '" + w.siteA.String() + "'")
	w.exec("UPDATE payments.merchants SET refund_enabled = true, user_refund = true WHERE id = '" + m.ID.String() + "'")
	o := w.mustPlace(credits, "paid")
	if err := w.p.receive(t.Context(), receipt(o)); err != nil {
		t.Fatal(err)
	}
	return w, o
}

func (w *world) setRefundLimit(limit int64) {
	w.t.Helper()
	if err := w.db.Tx(w.t.Context(), siteScope(w.siteA), func(tx pg.Tx) error {
		return SetSiteSettings(w.t.Context(), tx, admin, w.siteA, SiteSettings{Open: true, RefundSecondApproval: &limit}, "limit")
	}); err != nil {
		w.t.Fatal(err)
	}
}

func TestRefundAtTheApprovalLimitNeedsOneOperator(t *testing.T) {
	w, at := refundWorldAmount(t, fakePlugin{caps: CapRefund, refund: refundSucceeds}, 100*yuan)
	atLimit, err := w.p.Refund(t.Context(), w.accountA(), refundRequest(w, at, at.Credits, "at"))
	if err != nil {
		t.Fatal(err)
	}
	if atLimit.SecondRequired {
		t.Fatalf("exactly at the limit: %+v", atLimit)
	}
	o := w.mustPlace(100*yuan+1, "above")
	if err := w.p.receive(t.Context(), receipt(o)); err != nil {
		t.Fatal(err)
	}
	over, err := w.p.Refund(t.Context(), w.accountA(), refundRequest(w, o, o.Credits, "above"))
	if err != nil {
		t.Fatal(err)
	}
	if !over.SecondRequired {
		t.Fatalf("one credit above the limit: %+v", over)
	}
}

//nolint:cyclop // one refund crosses two approvals and each assertion is a step of that journey.
func TestRefundAboveLimitNeedsTwoDifferentOperators(t *testing.T) {
	var sends atomic.Int32
	w, o := refundWorldAmount(t, fakePlugin{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) {
		sends.Add(1)
		return RefundState{State: RefundSucceeded}, nil
	}}, 150*yuan)
	r := w.mustRefund(o, o.Credits, "two-people")
	if !r.SecondRequired || r.Approved {
		t.Fatal(r)
	}
	first := uuid.NewV7()
	got, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, first, "first check")
	if err != nil {
		t.Fatal(err)
	}
	if got.Approved || got.FirstApprovedBy == nil || *got.FirstApprovedBy != first || sends.Load() != 0 {
		t.Fatalf("first approval released the refund: %+v sends=%d", got, sends.Load())
	}
	// Repeating the first approval is idempotent: it stays at the first stage and sends nothing.
	got, err = w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, first, "same person")
	if err != nil || got.Approved || sends.Load() != 0 || !w.mustGetRefund(r.ID).SecondRequired {
		t.Fatalf("the first approver released it: %+v %v", got, err)
	}
	second := uuid.NewV7()
	got, err = w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, second, "second check")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Approved || got.ApprovedBy == nil || *got.ApprovedBy != second || got.State != "succeeded" || sends.Load() != 1 {
		t.Fatalf("second approval: %+v sends=%d", got, sends.Load())
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM platform.audit_log WHERE action='refund.approve' AND detail->>'stage'='first'"); got != 1 {
		t.Fatalf("first approvals = %d", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM platform.audit_log WHERE action='refund.approve' AND detail->>'stage'='final'"); got != 1 {
		t.Fatalf("final approvals = %d", got)
	}
}

func TestRefundApprovalRuleIsSnapshotted(t *testing.T) {
	t.Run("above stays above when the limit rises", func(t *testing.T) {
		w, o := refundWorldAmount(t, fakePlugin{caps: CapRefund, refund: refundSucceeds}, 150*yuan)
		r := w.mustRefund(o, o.Credits, "snapshot-high")
		if !r.SecondRequired {
			t.Fatal(r)
		}
		w.setRefundLimit(o.Credits * 10)
		got, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, uuid.NewV7(), "one check")
		if err != nil {
			t.Fatal(err)
		}
		if got.Approved || got.State != "frozen" {
			t.Fatalf("a raised limit released an existing request: %+v", got)
		}
	})
	t.Run("within stays within when the limit falls", func(t *testing.T) {
		w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: refundSucceeds})
		r := w.mustRefund(o, o.Credits, "snapshot-low")
		if r.SecondRequired {
			t.Fatal(r)
		}
		w.setRefundLimit(0)
		got, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, uuid.NewV7(), "one check")
		if err != nil {
			t.Fatal(err)
		}
		if !got.Approved || got.State != "succeeded" {
			t.Fatalf("a lowered limit added a second approval: %+v", got)
		}
	})
}

func TestSecondApprovalCannotBeBypassedInTheDatabase(t *testing.T) {
	w, o := refundWorldAmount(t, fakePlugin{caps: CapRefund, refund: refundSucceeds}, 150*yuan)
	r := w.mustRefund(o, o.Credits, "bypass")
	err := w.tx(w.p.platform, func(tx pg.Tx) error {
		_, err := tx.Exec(w.t.Context(), "UPDATE payments.refunds SET approved_by=$2 WHERE id=$1", uuid.UUID(r.ID), uuid.NewV7())
		return err
	})
	if pg.SQLState(err) != "23514" {
		t.Fatalf("approved a second-approval refund in SQL: %v", err)
	}
}
