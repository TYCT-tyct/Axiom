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

	testOrderPages(t, w, paid.ID, pending.ID, newer)
	testOrderFilters(t, w, newer)
}

func testOrderPages(t *testing.T, w *world, paid, pending OrderID, newer time.Time) {
	t.Helper()
	first, next, err := w.p.ListSiteOrders(t.Context(), siteScope(w.siteA), OrderQuery{Site: w.siteA}, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].ID != pending || next == nil || !next.At.Equal(newer) {
		t.Fatalf("first page: %+v, next %+v", first, next)
	}
	if first[0].MerchantName == "" {
		t.Fatal("the merchant account's name is missing")
	}
	second, last, err := w.p.ListSiteOrders(t.Context(), siteScope(w.siteA), OrderQuery{Site: w.siteA}, next, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != paid || last != nil {
		t.Fatalf("second page: %+v, next %+v", second, last)
	}
}

func testOrderFilters(t *testing.T, w *world, newer time.Time) {
	t.Helper()
	cases := map[string]struct {
		query OrderQuery
		want  int
	}{
		"state":    {OrderQuery{Site: w.siteA, State: Paid}, 1},
		"since":    {OrderQuery{Site: w.siteA, Since: newer}, 1},
		"until":    {OrderQuery{Site: w.siteA, Until: newer}, 1},
		"account":  {OrderQuery{Site: w.siteA, Account: new(w.acctB)}, 0},
		"merchant": {OrderQuery{Site: w.siteA, Merchant: new(uuid.NewV7())}, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			orders, _, err := w.p.ListSiteOrders(t.Context(), siteScope(w.siteA), tc.query, nil, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(orders) != tc.want {
				t.Fatalf("%s: %d orders, want %d", name, len(orders), tc.want)
			}
		})
	}
	mine, _, err := w.p.ListSiteOrders(t.Context(), siteScope(w.siteA), OrderQuery{Site: w.siteA, User: &w.user}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 {
		t.Fatalf("the user's orders: %+v", mine)
	}
	other, _, err := w.p.ListSiteOrders(t.Context(), siteScope(w.siteB), OrderQuery{Site: w.siteB}, nil, 10)
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
	assertFrozenRefund(t, w, r)
	approved := w.approve(r)
	if approved.State != "succeeded" {
		t.Fatalf("approved refund: %+v", approved)
	}
	assertDecidedRefund(t, w)
}

func assertFrozenRefund(t *testing.T, w *world, r Refund) {
	t.Helper()
	refunds, next, err := w.p.ListSiteRefunds(t.Context(), siteScope(w.siteA), RefundQuery{Site: w.siteA, State: "frozen"}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refunds) != 1 || next != nil {
		t.Fatalf("frozen refunds: %+v, next %+v", refunds, next)
	}
	assertRefundRow(t, refunds[0], w, r)
	for _, state := range []string{"sending", "pending", "succeeded", "failed"} {
		if list := siteRefunds(t, w, state); len(list) != 0 {
			t.Fatalf("%s: %+v", state, list)
		}
	}
}

func assertRefundRow(t *testing.T, got Refund, w *world, r Refund) {
	t.Helper()
	if got.ID != r.ID || got.SiteID != w.siteA || got.Reason != "unused credits" || got.RequestedBy != w.user ||
		got.ApprovedBy != nil || got.Approved || got.Amount.Currency.String() != "CNY" {
		t.Fatalf("the frozen refund: %+v", got)
	}
}

func assertDecidedRefund(t *testing.T, w *world) {
	t.Helper()
	succeeded := siteRefunds(t, w, "succeeded")
	if len(succeeded) != 1 || succeeded[0].ApprovedBy == nil || !succeeded[0].Approved {
		t.Fatalf("succeeded refunds: %+v", succeeded)
	}
	if frozen := siteRefunds(t, w, "frozen"); len(frozen) != 0 {
		t.Fatalf("a decided refund stayed frozen: %+v", frozen)
	}
}

// siteRefunds lists one site's refunds in a state, for the queue the page reads.
func siteRefunds(t *testing.T, w *world, state string) []Refund {
	t.Helper()
	refunds, _, err := w.p.ListSiteRefunds(t.Context(), siteScope(w.siteA), RefundQuery{Site: w.siteA, State: state}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	return refunds
}

func TestApproveRefundRequiresAndAuditsAReason(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: refundSucceeds})
	r := w.mustRefund(o, o.Credits, "audit-reason")
	approver := uuid.NewV7()
	_, err := w.p.ApproveRefund(t.Context(), siteScope(w.siteA), r.ID, approver, "  ")
	if err == nil {
		t.Fatal("a blank approval reason was accepted")
	}
	if field, ok := errors.AsType[*FieldError](err); !ok || field == nil {
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
