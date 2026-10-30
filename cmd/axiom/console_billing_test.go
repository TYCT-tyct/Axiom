package main

import (
	"net/http"
	"net/url"
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

	rec := f.do(http.MethodGet, "/orders", "", as(s))
	expect(t, "listOrders", rec, http.StatusOK, "")
	var list consoleapi.OrderList
	decode(t, rec, &list)
	if len(list.Data) != 2 || list.NextCursor != nil {
		t.Fatalf("listOrders: %d orders, next %v, want 2 and none", len(list.Data), list.NextCursor)
	}
	if list.Data[0].Id != second.Id || list.Data[1].Id != first.Id {
		t.Fatalf("listOrders: %s, %s, want newest first", list.Data[0].Id, list.Data[1].Id)
	}
	if list.Data[0].Refunds == nil || len(*list.Data[0].Refunds) != 0 {
		t.Fatalf("listOrders: refunds %v, want an empty list", list.Data[0].Refunds)
	}
	if list.Data[0].RefundAllowed == nil || !*list.Data[0].RefundAllowed {
		t.Fatalf("listOrders: refund_allowed %v, want true", list.Data[0].RefundAllowed)
	}
	// The channel's client secret is shown once, when the order is made; a page read does not repeat it.
	if list.Data[0].Checkout != nil && list.Data[0].Checkout.ClientSecret != nil {
		t.Fatalf("listOrders: checkout carries a client secret")
	}

	rec = f.do(http.MethodGet, "/orders?limit=1", "", as(s))
	expect(t, "listOrders", rec, http.StatusOK, "")
	list = consoleapi.OrderList{}
	decode(t, rec, &list)
	if len(list.Data) != 1 || list.Data[0].Id != second.Id || list.NextCursor == nil {
		t.Fatalf("listOrders: one order %v, next %v, want %s and a cursor", list.Data, list.NextCursor, second.Id)
	}
	rec = f.do(http.MethodGet, "/orders?limit=1&cursor="+url.QueryEscape(*list.NextCursor), "", as(s))
	expect(t, "listOrders", rec, http.StatusOK, "")
	list = consoleapi.OrderList{}
	decode(t, rec, &list)
	if len(list.Data) != 1 || list.Data[0].Id != first.Id || list.NextCursor != nil {
		t.Fatalf("listOrders: second page %v, next %v, want %s and none", list.Data, list.NextCursor, first.Id)
	}

	expect(t, "listOrders", f.do(http.MethodGet, "/orders?cursor=nonsense", "", as(s)), http.StatusBadRequest, "invalid_request")
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
	b := f.balance(s)
	if b.Batches == nil || len(*b.Batches) != 2 {
		t.Fatalf("balance: batches %v, want two", b.Batches)
	}
	byID := map[string]consoleapi.CreditBatch{}
	for _, batch := range *b.Batches {
		byID[batch.Id] = batch
	}
	expiring, ok := byID[first.String()]
	if !ok || expiring.Source != consoleapi.CreditBatchGift || expiring.Remaining.Amount != "5000000000" || expiring.ExpiresAt == nil {
		t.Fatalf("balance: expiring batch %+v, want a gift of 5 credits expiring at %v", expiring, expires)
	}
	if !expiring.ExpiresAt.Equal(expires) {
		t.Fatalf("balance: expiry %v, want %v", expiring.ExpiresAt, expires)
	}
	if lasting := byID[second.String()]; lasting.ExpiresAt != nil || lasting.Amount.Amount != "3000000000" {
		t.Fatalf("balance: lasting batch %+v, want 3 credits without expiry", lasting)
	}
}
