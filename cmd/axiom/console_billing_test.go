package main

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

// TestListOrders: the page lists the account's orders newest first, its refunds are an empty list until one exists, and
// the cursor continues exactly where the page stopped.
func TestListOrders(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	first := f.order(s, "list-first", http.StatusCreated)
	second := f.order(s, "list-second", http.StatusCreated)

	list := listOrders(t, f, s, "")
	requireOrderPage(t, list, []string{second.Id, first.Id}, false)
	top := list.Data[0]
	if top.Refunds == nil || len(*top.Refunds) != 0 {
		t.Fatalf("listOrders: refunds %v, want an empty list", top.Refunds)
	}
	if top.RefundAllowed == nil || !*top.RefundAllowed {
		t.Fatalf("listOrders: refund_allowed %v, want true", top.RefundAllowed)
	}
	// The channel's client secret is shown once, when the order is made; a page read does not repeat it.
	if top.Checkout != nil && top.Checkout.ClientSecret != nil {
		t.Fatalf("listOrders: checkout carries a client secret")
	}

	one := listOrders(t, f, s, "?limit=1")
	requireOrderPage(t, one, []string{second.Id}, true)
	requireOrderPage(t, listOrders(t, f, s, "?limit=1&cursor="+url.QueryEscape(*one.NextCursor)), []string{first.Id}, false)

	expect(t, "listOrders", f.do(http.MethodGet, "/orders?cursor=nonsense", "", as(s)), http.StatusBadRequest, "invalid_request")
}

// listOrders reads one page of the account's orders as the query asks for it.
func listOrders(t *testing.T, f consoleFixture, s session, query string) consoleapi.OrderList {
	t.Helper()
	rec := f.do(http.MethodGet, "/orders"+query, "", as(s))
	expect(t, "listOrders", rec, http.StatusOK, "")
	var list consoleapi.OrderList
	decode(t, rec, &list)
	return list
}

// requireOrderPage checks the page's orders in the order it returned them and whether a cursor says more follow.
func requireOrderPage(t *testing.T, list consoleapi.OrderList, ids []string, more bool) {
	t.Helper()
	got := make([]string, len(list.Data))
	for i, order := range list.Data {
		got[i] = order.Id
	}
	if !slices.Equal(got, ids) || (list.NextCursor != nil) != more {
		t.Fatalf("listOrders: orders %v, next %v, want %v and cursor %t", got, list.NextCursor, ids, more)
	}
}

// TestRequestRefund: a paid order's unused credits can be frozen for a refund; the request waits for approval, replays
// under its key, cannot be doubled while in flight, and is refused for an unpaid order or a merchant that does not let
// users ask.
func TestRequestRefund(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	o := f.order(s, "refund-order", http.StatusCreated)
	paid, err := strconv.ParseInt(o.Total.Amount, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	f.gateway.pays(o.Id, payments.Money{Currency: usd, Minor: paid})
	expect(t, "verify", f.do(http.MethodPost, "/orders/"+o.Id+"/verify", "", as(s)), http.StatusOK, "")

	body := `{"credits":{"amount":"` + tenCredits + `","currency":"USD"},"reason":"changed my mind"}`
	rec := f.do(http.MethodPost, "/orders/"+o.Id+"/refunds", body, as(s), header("Idempotency-Key", "refund-key"))
	expect(t, "requestRefund", rec, http.StatusCreated, "")
	var refund consoleapi.OrderRefund
	decode(t, rec, &refund)
	if refund.State != consoleapi.RefundPendingReview || refund.Credits.Amount != tenCredits || refund.Amount.Amount == "0" {
		t.Fatalf("requestRefund: %+v, want review for ten credits and positive cash", refund)
	}

	rec = f.do(http.MethodPost, "/orders/"+o.Id+"/refunds", body, as(s), header("Idempotency-Key", "refund-key"))
	expect(t, "requestRefund", rec, http.StatusCreated, "")
	var replay consoleapi.OrderRefund
	decode(t, rec, &replay)
	if replay.Id != refund.Id {
		t.Fatalf("requestRefund: replay %s, want %s", replay.Id, refund.Id)
	}

	expect(t, "requestRefund", f.do(http.MethodPost, "/orders/"+o.Id+"/refunds", body, as(s),
		header("Idempotency-Key", "another-key")), http.StatusConflict, "conflict")

	rec = f.do(http.MethodGet, "/orders", "", as(s))
	expect(t, "listOrders", rec, http.StatusOK, "")
	var list consoleapi.OrderList
	decode(t, rec, &list)
	if len(list.Data) != 1 || list.Data[0].Refunds == nil || len(*list.Data[0].Refunds) != 1 {
		t.Fatalf("listOrders: refunds %v, want one on the order", list.Data)
	}

	unpaid := f.order(s, "refund-unpaid", http.StatusCreated)
	expect(t, "requestRefund", f.do(http.MethodPost, "/orders/"+unpaid.Id+"/refunds", body, as(s),
		header("Idempotency-Key", "unpaid-key")), http.StatusUnprocessableEntity, "unprocessable")

	f.run("UPDATE payments.merchants SET user_refund = false")
	refused := f.order(s, "refund-refused", http.StatusCreated)
	f.gateway.pays(refused.Id, payments.Money{Currency: usd, Minor: paid})
	expect(t, "verify", f.do(http.MethodPost, "/orders/"+refused.Id+"/verify", "", as(s)), http.StatusOK, "")
	expect(t, "requestRefund", f.do(http.MethodPost, "/orders/"+refused.Id+"/refunds", body, as(s),
		header("Idempotency-Key", "refused-key")), http.StatusForbidden, "forbidden")
}

// TestBalanceBatches: the balance shows each credit batch with what is left and when it expires, so the page can warn
// before credit stops counting.
func TestBalanceBatches(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	account, site := f.account(s)
	expires := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Microsecond)
	first, second := uuid.NewV7(), uuid.NewV7()
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		if err := billing.AddCredit(t.Context(), tx, billing.Credit{
			ID: first, Site: site, Account: account, Amount: 5_000_000_000, ExpiresAt: &expires,
			Actor: audit.System("test"), Reason: "expiring gift",
		}); err != nil {
			return err
		}
		return billing.AddCredit(t.Context(), tx, billing.Credit{
			ID: second, Site: site, Account: account, Amount: 3_000_000_000,
			Actor: audit.System("test"), Reason: "lasting gift",
		})
	}); err != nil {
		t.Fatal(err)
	}
	requireBatches(t, f.balance(s).Batches, map[string]batchWant{
		first.String():  {source: consoleapi.CreditBatchGift, amount: "5000000000", remaining: "5000000000", expires: &expires},
		second.String(): {source: consoleapi.CreditBatchGift, amount: "3000000000", remaining: "3000000000"},
	})
}

// batchWant is what one batch of the balance should say: what it came from, what it was, what is left and, if it
// expires, when.
type batchWant struct {
	source    consoleapi.CreditBatchSource
	amount    string
	remaining string
	expires   *time.Time
}

// requireBatches checks the balance's batches by ID: each one's source, remaining credits and expiry, and no others.
func requireBatches(t *testing.T, batches *[]consoleapi.CreditBatch, want map[string]batchWant) {
	t.Helper()
	got := make(map[string]consoleapi.CreditBatch, len(want))
	if batches != nil {
		for _, batch := range *batches {
			got[batch.Id] = batch
		}
	}
	if len(got) != len(want) {
		t.Fatalf("balance: %d batches, want %d", len(got), len(want))
	}
	for id, w := range want {
		batch, ok := got[id]
		if !ok {
			t.Fatalf("balance: no batch %s", id)
		}
		if batch.Source != w.source || batch.Amount.Amount != w.amount || batch.Remaining.Amount != w.remaining {
			t.Fatalf("balance: batch %s is %s/%s/%s, want %s/%s/%s", id,
				batch.Source, batch.Amount.Amount, batch.Remaining.Amount, w.source, w.amount, w.remaining)
		}
		if !sameInstant(batch.ExpiresAt, w.expires) {
			t.Fatalf("balance: batch %s expires at %v, want %v", id, batch.ExpiresAt, w.expires)
		}
	}
}

// sameInstant compares two optional instants, either of which may be absent.
func sameInstant(got, want *time.Time) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got.Equal(*want)
}
