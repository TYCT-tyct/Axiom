package main

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/adminapi"
)

// TestAdminPaidBatchesUseBatchTime: the drill-down list reads paid credit batches by the batch's created_at, the
// same time paid users and monthly revenue count. An order created before the window and credited inside it is
// represented by its batch, not by an order created_at filter; gift batches are not paid top-ups.
func TestAdminPaidBatchesUseBatchTime(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	main, _, err := f.svc.SiteForHost(adminHost)
	if err != nil {
		t.Fatal(err)
	}
	site := uuid.UUID(main.ID).String()
	siteID := main.ID.TypeID()
	account := f.text("SELECT id::text FROM accounts.accounts ORDER BY id LIMIT 1")
	// The order was created before the window; its credit batch is created inside it. The list must follow the
	// batch, not the order, or this payment would disappear from the dashboard's drill-down.
	merchant := f.seedMerchant()
	online := f.seedOrder(merchant, ownerEmail, "paid", time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC))
	f.run(fmt.Sprintf(`UPDATE payments.orders SET paid_at = '2026-11-01T00:01:00Z', updated_at = '2026-11-01T00:01:00Z' WHERE id = '%s'`, online))
	offline, gift, early := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	f.run(
		fmt.Sprintf(`INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, currency, paid, fee,
			offline_channel, created_at) VALUES ('%s', '%s', '%s', 'paid', 2500, 'USD', 2500, 0, 'bank', '2026-11-01T00:02:00Z')`,
			offline, site, account),
		fmt.Sprintf(`INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, currency, paid, fee,
			merchant_id, created_at) VALUES ('%s', '%s', '%s', 'paid', 1500, 'USD', 1500, 0, '%s', '2026-11-01T00:01:00Z')`,
			online, site, account, merchant),
		fmt.Sprintf(`INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, created_at)
			VALUES ('%s', '%s', '%s', 'gift', 999, '2026-11-01T00:03:00Z')`, gift, site, account),
		fmt.Sprintf(`INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, currency, paid, fee,
			offline_channel, created_at) VALUES ('%s', '%s', '%s', 'paid', 700, 'USD', 700, 0, 'bank', '2026-10-31T23:59:00Z')`,
			early, site, account),
	)
	since, until := "2026-11-01T00:00:00Z", "2026-11-03T00:00:00Z"
	path := "/payments/paid-batches?site_id=" + url.QueryEscape(siteID) + "&since=" + url.QueryEscape(since) + "&until=" + url.QueryEscape(until)
	rec := f.do(http.MethodGet, path, "", as(owner))
	expectAdmin(t, "paid batches", rec, 200, "")
	var list adminapi.PaidBatchList
	decode(t, rec, &list)
	if len(list.Data) != 2 || list.NextCursor != nil {
		t.Fatalf("paid batches: %s", rec.Body.String())
	}
	first, second := list.Data[0], list.Data[1]
	checkPaidBatch(t, first, offline.String(), "2500", false, "bank", time.Date(2026, 11, 1, 0, 2, 0, 0, time.UTC))
	checkPaidBatch(t, second, online.String(), "1500", true, "", time.Date(2026, 11, 1, 0, 1, 0, 0, time.UTC))

	// The cursor is the batch position, so the next page continues after the first row.
	firstPage := f.do(http.MethodGet, path+"&limit=1", "", as(owner))
	expectAdmin(t, "paid batches first page", firstPage, 200, "")
	var page adminapi.PaidBatchList
	decode(t, firstPage, &page)
	if len(page.Data) != 1 || page.NextCursor == nil {
		t.Fatalf("paid batches first page: %s", firstPage.Body.String())
	}
	next := f.do(http.MethodGet, path+"&limit=1&cursor="+url.QueryEscape(*page.NextCursor), "", as(owner))
	expectAdmin(t, "paid batches second page", next, 200, "")
	var secondPage adminapi.PaidBatchList
	decode(t, next, &secondPage)
	if len(secondPage.Data) != 1 || secondPage.Data[0].Id != online.String() || secondPage.NextCursor != nil {
		t.Fatalf("paid batches second page: %s", next.Body.String())
	}

	expectAdmin(t, "paid batches cursor", f.do(http.MethodGet, "/payments/paid-batches?site_id="+url.QueryEscape(siteID)+"&cursor=not-a-cursor", "", as(owner)), 400, "")
}

func checkPaidBatch(t *testing.T, got adminapi.PaidBatch, id, amount string, online bool, channel string, at time.Time) {
	t.Helper()
	if got.Id != id || got.Amount.Amount != amount || got.Online != online || !got.CreatedAt.Equal(at) {
		t.Fatalf("paid batch: %+v", got)
	}
	if channel == "" {
		if got.OfflineChannel != nil {
			t.Fatalf("paid batch channel: %+v", got)
		}
		return
	}
	if got.OfflineChannel == nil || *got.OfflineChannel != channel {
		t.Fatalf("paid batch channel: %+v", got)
	}
}
