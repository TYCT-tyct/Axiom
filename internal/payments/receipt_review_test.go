package payments

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

func TestIncompletePaymentRetriesAndRecovers(t *testing.T) {
	for _, field := range []string{"amount", "trade_no"} {
		for _, failures := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/%d", field, failures), func(t *testing.T) {
				testIncompletePayment(t, field, failures)
			})
		}
	}
}

func testIncompletePayment(t *testing.T, field string, failures int) {
	t.Helper()
	var o Order
	calls := 0
	w := newTestWorld(t, fakePlugin{
		create: goodCheckout, verify: signedNotice,
		query: func(context.Context, string, string) (Payment, error) {
			calls++
			p := receipt(o).payment
			if calls <= failures {
				if field == "amount" {
					p.Paid.Minor = 0
				} else {
					p.TradeNo = ""
				}
			}
			return p, nil
		},
	})
	w.waiting()
	o = w.mustPlace(yuan, "incomplete")
	if r := w.callback(o.MerchantID, http.MethodGet, callbackValues(o)); r.Code != 200 || calls != 2 {
		t.Fatalf("incomplete query did not retry once: status=%d calls=%d", r.Code, calls)
	}
	if failures == 2 {
		w.assertIncompleteReceipt(o)
		if _, err := w.p.Verify(t.Context(), w.accountA(), o.ID); err != nil {
			t.Fatal(err)
		}
	}
	w.assertCredit(o, o.Credits)
}

func (w *world) assertIncompleteReceipt(o Order) {
	w.t.Helper()
	if state, _, _ := w.stored(o.ID); state != string(Pending) {
		w.t.Fatal("transient empty receipt permanently held the order", state)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.order_payments") != 0 || w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 0 {
		w.t.Fatal("transient empty receipt became a permanent money fact")
	}
	if !strings.Contains(w.logs.String(), "paid receipt is incomplete") {
		w.t.Fatal("incomplete receipt was not warned")
	}
	w.assertCredit(o, 0)
}

func TestIncompleteNoticeUsesCompleteQuery(t *testing.T) {
	for _, field := range []string{"money", "trade_no"} {
		t.Run(field, func(t *testing.T) {
			var o Order
			w := newTestWorld(t, fakePlugin{
				create: goodCheckout, verify: signedNotice,
				query: func(context.Context, string, string) (Payment, error) { return receipt(o).payment, nil },
			})
			w.waiting()
			o = w.mustPlace(yuan, "empty-notice")
			v := callbackValues(o)
			v.Set(field, "0")
			if field == "trade_no" {
				v.Set(field, "")
			}
			v.Set("sign", epaySignature(v, theKey))
			if r := w.callback(o.MerchantID, http.MethodGet, v); r.Code != 200 {
				t.Fatal(r.Code, r.Body)
			}
			w.assertCredit(o, o.Credits)
		})
	}
}

func TestInvalidReceiptTextIsRecordedSafely(t *testing.T) {
	for _, bad := range []string{"trade\xff", "\xff", "trade\x00"} {
		for _, field := range []string{"order", "paid trade", "unpaid trade"} {
			t.Run(fmt.Sprintf("%x/%s", bad, field), func(t *testing.T) {
				testInvalidReceiptText(t, bad, field)
			})
		}
	}
}

func testInvalidReceiptText(t *testing.T, bad, field string) {
	t.Helper()
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	o := w.mustPlace(yuan, "malformed")
	c := receipt(o)
	wantState, wantDiscrepancy := string(Review), 1
	switch field {
	case "order":
		c.order = bad
		wantState = string(Pending)
	case "paid trade":
		c.payment.TradeNo = bad
	case "unpaid trade":
		c.payment.TradeNo, c.payment.State = bad, PayPending
		wantState, wantDiscrepancy = string(Pending), 0
	}
	w.receive(c)
	w.assertCredit(o, 0)
	if state, _, _ := w.stored(o.ID); state != wantState {
		t.Fatalf("malformed receipt state=%s, want %s", state, wantState)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != wantDiscrepancy || w.count(w.p.platform, "SELECT count(*) FROM payments.order_payments") != 0 {
		t.Fatal("malformed text lost the discrepancy or became a paid receipt")
	}
	if field == "unpaid trade" {
		if w.count(w.p.platform, "SELECT count(*) FROM payments.order_events WHERE kind = 'payment'") != 1 {
			t.Fatal("malformed unpaid notice lost its event")
		}
	} else {
		log := w.logs.String()
		if !strings.Contains(log, "order=") || !strings.Contains(log, "trade_no=") || !strings.Contains(log, "discrepancy=") {
			t.Fatal("discrepancy warning cannot locate its record", log)
		}
	}
}

func TestOrderWindowsCoverLongestPolicyAndScope(t *testing.T) {
	for _, longest := range []string{"grace_seconds", "auto_credit_seconds", "post_expiry_query_seconds"} {
		t.Run(longest, func(t *testing.T) {
			testLongestWindow(t, longest)
		})
	}
}

func testLongestWindow(t *testing.T, longest string) {
	t.Helper()
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	o := w.mustPlace(yuan, "longest-window")
	w.endOrder(o, Expired)
	w.limits("grace_seconds = 0, auto_credit_seconds = 0, post_expiry_query_seconds = 0")
	w.limits(longest + " = 120")
	w.exec("UPDATE payments.orders SET expires_at = transaction_timestamp() - interval '1 minute', next_query_at = transaction_timestamp() - interval '1 minute'")
	for i, scope := range []pg.Scope{w.p.platform, w.accountA(), w.accountB()} {
		if err := w.tx(scope, func(tx pg.Tx) error {
			allowed, err := queries.QueryAllowed(t.Context(), tx, uuid.UUID(o.ID))
			if err != nil {
				return err
			}
			due, err := queries.QueryOrders(t.Context(), tx, expireBatch)
			if err == nil && (allowed != (i < 2) || allowed != slices.ContainsFunc(due, func(r QueryOrdersRow) bool { return r.ID == uuid.UUID(o.ID) })) {
				t.Error("manual and scheduled query eligibility or tenant visibility differs")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	in := w.input(w.siteA)
	in.ID, in.Rev, in.Secrets = o.MerchantID, m.Rev, map[string]secret.Secret{"key": secret.New("replacement")}
	if _, err := w.save(siteScope(w.siteA), in); !isField(err, "/secrets") {
		t.Fatal("identity released before the longest policy elapsed", err)
	}
	if err := w.p.queryScheduled(t.Context(), o.ID); err != nil {
		t.Fatal(err)
	}
}

func TestQueryOrdersPrioritizeUnexpiredThenJustExpiredPending(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	late := w.mustPlace(yuan, "old-terminal")
	w.endOrder(late, Expired)
	just := w.mustPlace(yuan, "just-expired")
	fresh := w.mustPlace(yuan, "fresh")
	w.exec("UPDATE payments.orders SET next_query_at = transaction_timestamp() - interval '1 day' WHERE state = 'expired'",
		fmt.Sprintf("UPDATE payments.orders SET expires_at = transaction_timestamp() - interval '1 minute' WHERE id = '%s'", uuid.UUID(just.ID)),
		"UPDATE payments.orders SET next_query_at = transaction_timestamp() - interval '1 minute' WHERE state = 'pending'")
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		ids, err := queries.QueryOrders(t.Context(), tx, 2)
		if err == nil && (len(ids) != 2 || ids[0].ID != uuid.UUID(fresh.ID) || ids[1].ID != uuid.UUID(just.ID)) {
			t.Error("late backlog outranked a pending order", ids)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
