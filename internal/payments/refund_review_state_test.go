package payments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func (w *world) swapRefundPlugin(plugin fakePlugin) {
	w.t.Helper()
	w.p.plugins["fake"] = plugin
	w.p.mu.Lock()
	clear(w.p.channels)
	w.p.mu.Unlock()
}

func TestRefundReviewDisabledCapabilityStaysUnsent(t *testing.T) {
	var sends atomic.Int32
	plugin := fakePlugin{create: goodCheckout, caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) {
		sends.Add(1)
		return RefundState{State: RefundSucceeded}, nil
	}}
	w, o := refundWorld(t, plugin)
	r := w.mustRefund(o, o.Credits, "disabled")
	plugin.caps = 0
	w.swapRefundPlugin(plugin)
	if _, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, uuid.NewV7(), "approved"); !errors.Is(err, ErrRefundForbidden) {
		t.Fatal(err)
	}
	if w.mustGetRefund(r.ID).State != "frozen" || sends.Load() != 0 {
		t.Fatal("disabled channel was claimed")
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies"); n != 0 {
		t.Fatal(n)
	}
	plugin.caps = CapRefund
	w.swapRefundPlugin(plugin)
	if err := w.p.driveRefund(t.Context(), uuid.UUID(r.ID), true); err != nil {
		t.Fatal(err)
	}
	if w.mustGetRefund(r.ID).State != "succeeded" || sends.Load() != 1 {
		t.Fatal("unsent refund did not resume")
	}
}

func TestRefundReviewUnrecoverableWorkLeavesAutomaticQueue(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) { return RefundState{State: RefundPending}, nil }})
	r := w.approve(w.mustRefund(o, o.Credits, "park"))
	before := w.count(w.p.platform, "SELECT attempts FROM payments.refunds WHERE id=$1", uuid.UUID(r.ID))
	for range 3 {
		w.dueRefunds()
	}
	if after := w.count(w.p.platform, "SELECT attempts FROM payments.refunds WHERE id=$1", uuid.UUID(r.ID)); after != before {
		t.Fatal("manual-only work was reclaimed", before, after)
	}
	proposal := Resolution{Order: o.ID, Refund: r.ID, Outcome: "succeeded", User: w.user, Key: "manual", Reason: "verified at gateway", Evidence: "refund receipt"}
	if err := w.p.Resolve(t.Context(), siteScope(w.siteA), proposal); err != nil {
		t.Fatal(err)
	}
	if err := w.p.ApproveResolution(t.Context(), siteScope(w.siteA), o.ID, proposal.Key, uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	if w.mustGetRefund(r.ID).State != "succeeded" {
		t.Fatal("parked refund cannot be resolved")
	}
}

func TestRefundReviewRejectedRequestCannotBeApproved(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund})
	r := w.mustRefund(o, o.Credits, "decline")
	if _, err := w.p.RejectRefund(t.Context(), siteScope(w.siteA), r.ID, uuid.NewV7(), "declined"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, uuid.NewV7(), "approved"); !errors.Is(err, ErrRefundForbidden) {
		t.Fatal(err)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.refunds WHERE approved_by IS NOT NULL"); n != 0 {
		t.Fatal(n)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM platform.audit_log WHERE action='refund.approve'"); n != 0 {
		t.Fatal(n)
	}
}

func TestRefundReviewResolutionForeignKeyBindsOrder(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund})
	other := w.mustPlace(yuan, "other")
	w.receive(receipt(other))
	foreign := w.mustRefund(other, other.Credits, "foreign")
	foreignID := uuid.UUID(foreign.ID)
	err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return queries.InsertResolution(t.Context(), tx, InsertResolutionParams{
			ID: uuid.NewV7(), OrderID: uuid.UUID(o.ID), SiteID: o.SiteID, AccountID: o.AccountID,
			RefundID: &foreignID, Outcome: "failed", Evidence: "receipt", Reason: "wrong order", RequestedBy: w.user, IdemKey: "mismatched",
		})
	})
	if pg.SQLState(err) != "23503" {
		t.Fatalf("mismatched refund/order foreign key accepted: %v", err)
	}
}

func TestRefundReviewInvalidReferenceKeepsDefinitiveOutcome(t *testing.T) {
	for _, ref := range []string{"invalid\x00reference", strings.Repeat("x", 1025)} {
		for _, outcome := range []RefundOutcome{RefundSucceeded, RefundFailed} {
			t.Run(fmt.Sprintf("%d/%d", len(ref), outcome), func(t *testing.T) {
				w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) {
					return RefundState{State: outcome, ProviderRef: ref}, nil
				}})
				r := w.approve(w.mustRefund(o, o.Credits, "invalid-ref"))
				want := map[RefundOutcome]string{RefundSucceeded: "succeeded", RefundFailed: "failed"}[outcome]
				if r.State != want || r.ProviderRef != "" {
					t.Fatal(r)
				}
				if n := w.count(w.p.platform, "SELECT frozen FROM billing.balances WHERE account_id=$1", w.account); n != 0 {
					t.Fatal(n)
				}
			})
		}
	}
}

func TestRefundReviewBindingFailureDoesNotClaimPendingWork(t *testing.T) {
	var unavailable atomic.Bool
	plugin := fakePlugin{
		create: goodCheckout, caps: CapRefund | CapQueryRefund,
		open: func() error {
			if unavailable.Load() {
				return errors.New("temporarily unavailable")
			}
			return nil
		},
		refund: func(context.Context, RefundCall) (RefundState, error) { return RefundState{State: RefundPending}, nil },
	}
	w, o := refundWorld(t, plugin)
	r := w.approve(w.mustRefund(o, o.Credits, "binding"))
	before := w.count(w.p.platform, "SELECT attempts FROM payments.refunds WHERE id=$1", uuid.UUID(r.ID))
	unavailable.Store(true)
	w.swapRefundPlugin(plugin)
	w.dueRefunds()
	if after := w.count(w.p.platform, "SELECT attempts FROM payments.refunds WHERE id=$1", uuid.UUID(r.ID)); after != before {
		t.Fatal(before, after)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies"); n != 0 {
		t.Fatal("binding error reported as gateway unknown", n)
	}
}

func TestRefundReviewResolutionReplayCannotChangeCurrency(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	o := w.mustPlace(10*yuan, "currency-replay")
	c := receipt(o)
	c.payment.Paid.Minor--
	w.receive(c)
	r := Resolution{Order: o.ID, Outcome: "credit", Payment: c.payment, User: w.user, Key: "credit", Reason: "actual payment", Evidence: "gateway receipt"}
	if err := w.p.Resolve(t.Context(), siteScope(w.siteA), r); err != nil {
		t.Fatal(err)
	}
	r.Payment.Paid.Currency = currency(t, "USD")
	if err := w.p.Resolve(t.Context(), siteScope(w.siteA), r); !errors.Is(err, ErrRefundForbidden) {
		t.Fatal("same-key currency change accepted", err)
	}
}
