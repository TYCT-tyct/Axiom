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

// TestAdminOperationsOverview: the dashboard reads calls, revenue, cost and cash from the site's window, keeps the
// calls whose cost is unknown out of the cost total, and lets the page drill into the call list with the model and
// result filters the log has.
func TestAdminOperationsOverview(t *testing.T) {
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
	since, until := now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)

	f.run(
		fmt.Sprintf(`INSERT INTO observability.call_log
 (id,at,site_id,account_id,workspace_id,key_id,ip,request_class,used_tools,has_image,has_file,model,endpoint_id,attempt_count,usage,service_tier,price_nano,cost_nano,currency,free_of_charge,latency_ms,first_token_ms,consent_version,training_allowed,error_kind)
 SELECT uuidv7(), v.at, v.site::uuid, '%s', uuidv7(), uuidv7(), '203.0.113.7', 'chat', false, false, false, v.model, uuidv7(), 1, '{}', 'standard', 1, 1, 'USD', false, 1, 0, 'v', false, v.error_kind
 FROM (VALUES ('model-a','upstream',now(),'%s'), ('model-a','',now(),'%s'), ('model-b','',now(),'%s'), ('model-a','',now() - interval '2 hours','%s'), ('model-a','',now(),'%s')) v(model,error_kind,at,site)`, account, site, site, site, site, uuid.NewV7().String()),
		`INSERT INTO billing.holders (id, expires_at) VALUES (uuidv7(), now() + interval '1 hour')`,
		fmt.Sprintf(`INSERT INTO billing.reservations (request_id, site_id, account_id, workspace_id, key_id, holder_id,
 workspace_window, key_window, segment, amount, state, deadline, charged, profit, created_at)
 SELECT uuidv7(), '%s', '%s', uuidv7(), uuidv7(), (SELECT id FROM billing.holders ORDER BY id LIMIT 1), now(), now(),
  1, 5, v.state, now() + interval '1 hour', v.charged, v.profit, now()
 FROM (VALUES ('settled',1000,400), ('settled',1000,600), ('cleanup_settled',500,NULL), ('pending',0,NULL)) v(state,charged,profit)`, site, account),
		fmt.Sprintf(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id, created_at) VALUES
 ('0198c0a8-0000-7000-8000-0000000000c1', '%s', '%s', 'credit', '0198c0a8-0000-7000-8000-0000000000f1', now()),
 ('0198c0a8-0000-7000-8000-0000000000c2', '%s', '%s', 'refund', '0198c0a8-0000-7000-8000-0000000000f1', now()),
 ('0198c0a8-0000-7000-8000-0000000000c3', '%s', '%s', 'credit', '0198c0a8-0000-7000-8000-0000000000f2', now() - interval '2 hours')`, site, account, site, account, site, account),
		fmt.Sprintf(`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount)
 SELECT id, site_id, account_id, 'cash', CASE id WHEN '0198c0a8-0000-7000-8000-0000000000c1' THEN 2000
  WHEN '0198c0a8-0000-7000-8000-0000000000c2' THEN -500 ELSE 999 END
 FROM billing.transactions WHERE request_id IN ('0198c0a8-0000-7000-8000-0000000000f1', '0198c0a8-0000-7000-8000-0000000000f2')`),
	)

	rec := f.do(http.MethodGet, "/reports/overview?since="+url.QueryEscape(since)+"&until="+url.QueryEscape(until), "", as(owner))
	expectAdmin(t, "overview", rec, 200, "")
	var overview adminapi.OperationsOverview
	decode(t, rec, &overview)
	if overview.SiteId == nil || *overview.SiteId != siteID {
		t.Fatalf("site: %s", rec.Body.String())
	}
	if overview.Calls == nil || *overview.Calls != 3 || overview.Errors == nil || *overview.Errors != 1 {
		t.Fatalf("calls and errors: %s", rec.Body.String())
	}
	if overview.Revenue == nil || overview.Revenue.Amount != "2500" || overview.Cost == nil || overview.Cost.Amount != "1000" {
		t.Fatalf("revenue and cost: %s", rec.Body.String())
	}
	if overview.UnknownCostCalls == nil || *overview.UnknownCostCalls != 1 {
		t.Fatalf("unknown cost calls: %s", rec.Body.String())
	}
	if overview.Cash == nil || len(*overview.Cash) != 1 || (*overview.Cash)[0].Amount != "1500" || (*overview.Cash)[0].Currency != "USD" {
		t.Fatalf("cash: %s", rec.Body.String())
	}
	if overview.TopAccounts == nil || len(*overview.TopAccounts) != 1 || *(*overview.TopAccounts)[0].Calls != 3 {
		t.Fatalf("top accounts: %s", rec.Body.String())
	}
	if overview.TopModels == nil || len(*overview.TopModels) != 2 || *(*overview.TopModels)[0].Model != "model-a" {
		t.Fatalf("top models: %s", rec.Body.String())
	}

	for _, tc := range []struct {
		name, query string
		calls       int
	}{
		{"model", "model=model-a", 2},
		{"errors", "status=error", 1},
		{"ok", "status=ok", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(http.MethodGet, "/calls?since="+url.QueryEscape(since)+"&until="+url.QueryEscape(until)+"&site_id="+url.QueryEscape(siteID)+"&"+tc.query, "", as(owner))
			expectAdmin(t, "filtered calls", rec, 200, "")
			var list adminapi.AdminCallList
			decode(t, rec, &list)
			if list.Data == nil || len(*list.Data) != tc.calls {
				t.Fatalf("%s: %s", tc.name, rec.Body.String())
			}
			if tc.name == "model" && (list.ModelFilter == nil || *list.ModelFilter != "model-a") {
				t.Fatalf("model filter echo: %s", rec.Body.String())
			}
			if tc.name == "errors" && (list.StatusFilter == nil || *list.StatusFilter != adminapi.AdminCallStatusError) {
				t.Fatalf("status filter echo: %s", rec.Body.String())
			}
			if list.SiteFilter == nil || *list.SiteFilter != siteID {
				t.Fatalf("site filter echo: %s", rec.Body.String())
			}
		})
	}

	// A platform-wide search keeps the other site's call; the dashboard's own figure does not include it.
	for query, want := range map[string]int{"": 4, "&site_id=" + url.QueryEscape(siteID): 3} {
		rec := f.do(http.MethodGet, "/calls?since="+url.QueryEscape(since)+"&until="+url.QueryEscape(until)+query, "", as(owner))
		expectAdmin(t, "site calls", rec, 200, "")
		var list adminapi.AdminCallList
		decode(t, rec, &list)
		if list.Data == nil || len(*list.Data) != want {
			t.Fatalf("calls with %q: %s", query, rec.Body.String())
		}
	}

	expectAdmin(t, "overview window too wide", f.do(http.MethodGet, "/reports/overview?since=2020-01-01T00:00:00Z&until=2020-03-01T00:00:00Z", "", as(owner)), 400, "")
}

// TestAdminOperationsOverviewSumsWiderThanInt64: two settled calls that each fit int64 add up past it; the dashboard
// keeps the digits in decimal text instead of failing the page or wrapping the number.
func TestAdminOperationsOverviewSumsWiderThanInt64(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	main, _, err := f.svc.SiteForHost(adminHost)
	if err != nil {
		t.Fatal(err)
	}
	site := uuid.UUID(main.ID).String()
	account := f.text("SELECT id::text FROM accounts.accounts ORDER BY id LIMIT 1")
	now := time.Now().UTC()
	since, until := now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)
	const half = int64(5_000_000_000_000_000_000)
	f.run(
		`INSERT INTO billing.holders (id, expires_at) VALUES (uuidv7(), now() + interval '1 hour')`,
		fmt.Sprintf(`INSERT INTO billing.reservations (request_id, site_id, account_id, workspace_id, key_id, holder_id,
 workspace_window, key_window, segment, amount, state, deadline, charged, profit, created_at)
 SELECT uuidv7(), '%s', '%s', uuidv7(), uuidv7(), (SELECT id FROM billing.holders ORDER BY id LIMIT 1), now(), now(),
  1, %d, 'settled', now() + interval '1 hour', %d, 0, now()
 FROM generate_series(1, 2)`, site, account, half, half),
	)
	rec := f.do(http.MethodGet, "/reports/overview?since="+url.QueryEscape(since)+"&until="+url.QueryEscape(until), "", as(owner))
	expectAdmin(t, "wide overview", rec, 200, "")
	var overview adminapi.OperationsOverview
	decode(t, rec, &overview)
	const sum = "10000000000000000000"
	if overview.Revenue == nil || overview.Revenue.Amount != sum || overview.Cost == nil || overview.Cost.Amount != sum {
		t.Fatalf("wide revenue and cost: %s", rec.Body.String())
	}
}
