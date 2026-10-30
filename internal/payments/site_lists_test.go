package payments

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

func refundSucceeds(context.Context, RefundCall) (RefundState, error) {
	return RefundState{State: RefundSucceeded}, nil
}

func TestListSiteOrdersFiltersAndPages(t *testing.T) {
	w, paid := refundWorld(t, fakePlugin{caps: CapRefund})
	pending := w.mustPlace(yuan, "admin-pending")
	// Two fixed times make the window and the cursor independent of how fast the test runs.
	older := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	w.exec(`UPDATE payments.orders SET created_at = '`+older.Format(time.RFC3339)+`' WHERE id = '`+uuid.UUID(paid.ID).String()+`'`,
		`UPDATE payments.orders SET created_at = '`+newer.Format(time.RFC3339)+`' WHERE id = '`+uuid.UUID(pending.ID).String()+`'`)

	site := uuid.UUID(w.siteA)
	first, next, err := w.p.ListSiteOrders(t.Context(), siteScope(w.siteA), OrderQuery{Site: site}, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].ID != pending.ID || next == nil || !next.At.Equal(newer) {
		t.Fatalf("first page: %+v, next %+v", first, next)
	}
	second, last, err := w.p.ListSiteOrders(t.Context(), siteScope(w.siteA), OrderQuery{Site: site}, next, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != paid.ID || last != nil {
		t.Fatalf("second page: %+v, next %+v", second, last)
	}
	if first[0].MerchantName == "" {
		t.Fatal("the merchant account's name is missing")
	}

	for name, query := range map[string]OrderQuery{
		"state":    {Site: site, State: Paid},
		"since":    {Site: site, Since: newer},
		"until":    {Site: site, Until: newer},
		"account":  {Site: site, Account: new(uuid.UUID(w.acctB))},
		"merchant": {Site: site, Merchant: new(uuid.NewV7())},
	} {
		t.Run(name, func(t *testing.T) {
			orders, _, err := w.p.ListSiteOrders(t.Context(), siteScope(w.siteA), query, nil, 10)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if name == "account" || name == "merchant" {
				want = 0
			}
			if len(orders) != want {
				t.Fatalf("%s: %d orders, want %d", name, len(orders), want)
			}
		})
	}
	mine, _, err := w.p.ListSiteOrders(t.Context(), siteScope(w.siteA), OrderQuery{Site: site, User: &w.user}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 {
		t.Fatalf("the user's orders: %+v", mine)
	}
	other, _, err := w.p.ListSiteOrders(t.Context(), siteScope(w.siteB), OrderQuery{Site: uuid.UUID(w.siteB)}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("another site's orders: %+v", other)
	}
}

func TestListSiteRefundsFiltersAndDecisionState(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: refundSucceeds})
	r := w.mustRefund(o, o.Credits, "admin-refund")
	site := uuid.UUID(w.siteA)
	refunds, next, err := w.p.ListSiteRefunds(t.Context(), siteScope(w.siteA), RefundQuery{Site: site, State: "frozen"}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refunds) != 1 || next != nil {
		t.Fatalf("frozen refunds: %+v, next %+v", refunds, next)
	}
	got := refunds[0]
	if got.ID != r.ID || got.SiteID != site || got.Reason != "unused credits" || got.RequestedBy != w.user ||
		got.ApprovedBy != nil || got.Approved || got.Amount.Currency.String() != "CNY" {
		t.Fatalf("the frozen refund: %+v", got)
	}
	for _, state := range []string{"sending", "pending", "succeeded", "failed"} {
		list, _, err := w.p.ListSiteRefunds(t.Context(), siteScope(w.siteA), RefundQuery{Site: site, State: state}, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 0 {
			t.Fatalf("%s: %+v", state, list)
		}
	}

	approved := w.approve(r)
	if approved.State != "succeeded" {
		t.Fatalf("approved refund: %+v", approved)
	}
	succeeded, _, err := w.p.ListSiteRefunds(t.Context(), siteScope(w.siteA), RefundQuery{Site: site, State: "succeeded"}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(succeeded) != 1 || succeeded[0].ApprovedBy == nil || !succeeded[0].Approved {
		t.Fatalf("succeeded refunds: %+v", succeeded)
	}
	frozen, _, err := w.p.ListSiteRefunds(t.Context(), siteScope(w.siteA), RefundQuery{Site: site, State: "frozen"}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(frozen) != 0 {
		t.Fatalf("a decided refund stayed frozen: %+v", frozen)
	}
}

func TestApproveRefundRequiresAndAuditsAReason(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: refundSucceeds})
	r := w.mustRefund(o, o.Credits, "audit-reason")
	approver := uuid.NewV7()
	if _, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, approver, "  "); err == nil {
		t.Fatal("a blank approval reason was accepted")
	} else if _, ok := errors.AsType[*FieldError](err); !ok {
		t.Fatalf("blank reason: %v", err)
	}
	if _, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, approver, "checked the receipt"); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT reason FROM platform.audit_log WHERE action = 'refund.approve'").Scan(&reason)
	}); err != nil {
		t.Fatal(err)
	}
	if reason != "checked the receipt" {
		t.Fatalf("audited reason %q", reason)
	}
}
