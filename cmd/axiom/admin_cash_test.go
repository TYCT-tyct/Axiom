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

// TestAdminCashMovements: the dashboard's cash list follows the ledger's transaction time, so a refund of an order
// created before the window and an offline top-up are both in it, with the channel that received the money.
func TestAdminCashMovements(t *testing.T) {
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
	now := time.Now().UTC()
	offlineBatch, onlineBatch := uuid.NewV7(), uuid.NewV7()
	f.run(
		fmt.Sprintf(`INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, currency, paid, fee,
			offline_channel, created_at) VALUES ('%s', '%s', '%s', 'paid', 2000, 'USD', 2000, 0, 'bank', '%s')`,
			offlineBatch, site, account, now.Add(-2*time.Hour).Format(time.RFC3339)),
		fmt.Sprintf(`INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, currency, paid, fee,
			merchant_id, created_at) VALUES ('%s', '%s', '%s', 'paid', 3000, 'USD', 3000, 0, '%s', '%s')`,
			onlineBatch, site, account, uuid.NewV7(), now.Add(-3*time.Hour).Format(time.RFC3339)),
		fmt.Sprintf(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id, created_at) VALUES
			('%s', '%s', '%s', 'credit', '%s', '%s'),
			('%s', '%s', '%s', 'refund', '%s', '%s')`,
			uuid.NewV7(), site, account, offlineBatch, now.Add(-20*time.Minute).Format(time.RFC3339),
			uuid.NewV7(), site, account, onlineBatch, now.Add(-30*time.Minute).Format(time.RFC3339)),
		fmt.Sprintf(`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount, currency)
			SELECT t.id, t.site_id, t.account_id, 'cash', CASE t.kind WHEN 'refund' THEN -500 ELSE 2000 END, 'USD'
			FROM billing.transactions t WHERE t.site_id = '%s'`, site),
	)
	since, until := now.Add(-time.Hour).Format(time.RFC3339), now.Format(time.RFC3339)
	rec := f.do(http.MethodGet, "/payments/cash?site_id="+url.QueryEscape(siteID)+"&since="+url.QueryEscape(since)+"&until="+url.QueryEscape(until), "", as(owner))
	expectAdmin(t, "cash movements", rec, 200, "")
	var list adminapi.CashMovementList
	decode(t, rec, &list)
	if len(list.Data) != 2 || list.NextCursor != nil {
		t.Fatalf("cash: %s", rec.Body.String())
	}
	first, second := list.Data[0], list.Data[1]
	if first.Kind == nil || *first.Kind != adminapi.Credit || first.OfflineChannel == nil || *first.OfflineChannel != "bank" {
		t.Fatalf("offline credit: %s", rec.Body.String())
	}
	if second.Kind == nil || *second.Kind != adminapi.Refund || second.OrderId == nil {
		t.Fatalf("refund of the older order: %s", rec.Body.String())
	}
	if first.Amount == nil || first.Amount.Amount != "2000" || second.Amount == nil || second.Amount.Amount != "-500" {
		t.Fatalf("amounts: %s", rec.Body.String())
	}

	expectAdmin(t, "cash cursor", f.do(http.MethodGet, "/payments/cash?site_id="+url.QueryEscape(siteID)+"&cursor=not-a-cursor", "", as(owner)), 400, "")
}
