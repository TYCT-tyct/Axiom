package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/adminapi"
)

// TestAdminOperationsOverview: the dashboard reads calls, revenue, cost and cash from the site's window, keeps the
// calls whose cost is unknown out of the cost total, and lets the page drill into the call list with the model and
// result filters the log has.
//
//nolint:cyclop,gocognit // one dashboard journey: unknown cost, its target, then the same cost after it is filled in.
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
		`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount)
 SELECT id, site_id, account_id, 'cash', CASE id WHEN '0198c0a8-0000-7000-8000-0000000000c1' THEN 2000
  WHEN '0198c0a8-0000-7000-8000-0000000000c2' THEN -500 ELSE 999 END
 FROM billing.transactions WHERE request_id IN ('0198c0a8-0000-7000-8000-0000000000f1', '0198c0a8-0000-7000-8000-0000000000f2')`,
	)

	// A cost target is present while one call's supplier cost is unknown, but the gap is withheld: the cost total
	// does not include that call, so it cannot support a definite comparison.
	targetPath := "/reports/targets/" + now.Format("2006-01")
	rec := f.change(owner, http.MethodPut, targetPath,
		`{"cost":{"amount":"1000","currency":"USD"},"rev":0,"reason":"cost target"}`, newKey())
	expectAdmin(t, "cost target", rec, 200, "")

	rec = f.do(http.MethodGet, "/reports/overview?since="+url.QueryEscape(since)+"&until="+url.QueryEscape(until), "", as(owner))
	expectAdmin(t, "overview", rec, 200, "")
	var overview adminapi.OperationsOverview
	decode(t, rec, &overview)
	wantPtr(t, rec, "site", overview.SiteId, siteID)
	wantPtr(t, rec, "calls", overview.Calls, int64(3))
	wantPtr(t, rec, "errors", overview.Errors, int64(1))
	wantPtr(t, rec, "revenue", overview.Revenue, adminapi.Money{Amount: "2500", Currency: "USD"})
	wantPtr(t, rec, "cost", overview.Cost, adminapi.Money{Amount: "1000", Currency: "USD"})
	wantPtr(t, rec, "unknown cost calls", overview.UnknownCostCalls, int64(1))
	if overview.Targets == nil || overview.Targets.Cost == nil {
		t.Fatalf("the cost target is missing: %s", rec.Body.String())
	}
	if overview.Targets.Cost.Gap != nil {
		t.Fatalf("a cost gap was shown while the cost total is incomplete: %s", rec.Body.String())
	}
	if overview.CallsIncomplete == nil || *overview.CallsIncomplete {
		t.Errorf("a window inside the retained log is complete: %s", rec.Body.String())
	}
	if overview.CallsAvailableSince == nil || overview.CallsAvailableSince.After(now.Add(-2*time.Hour)) {
		t.Errorf("call coverage starts after the oldest call: %s", rec.Body.String())
	}
	cash := wantList(t, rec, "cash", overview.Cash, 1)
	wantPtr(t, rec, "cash amount", &cash[0].Amount, "1500")
	wantPtr(t, rec, "cash currency", &cash[0].Currency, "USD")
	accounts := wantList(t, rec, "top accounts", overview.TopAccounts, 1)
	wantPtr(t, rec, "top account calls", accounts[0].Calls, int64(3))
	models := wantList(t, rec, "top models", overview.TopModels, 2)
	wantPtr(t, rec, "top model", models[0].Model, "model-a")

	// Once the unknown cost is filled in, the same target gains its definite gap.
	f.run("UPDATE billing.reservations SET profit = 0 WHERE state = 'cleanup_settled'")
	rec = f.do(http.MethodGet, "/reports/overview?since="+url.QueryEscape(since)+"&until="+url.QueryEscape(until), "", as(owner))
	expectAdmin(t, "overview with complete cost", rec, 200, "")
	var complete adminapi.OperationsOverview
	decode(t, rec, &complete)
	wantPtr(t, rec, "complete unknown cost calls", complete.UnknownCostCalls, int64(0))
	if complete.Targets == nil || complete.Targets.Cost == nil || complete.Targets.Cost.Gap == nil {
		t.Fatalf("a complete cost should show the target's gap: %s", rec.Body.String())
	}

	for _, tc := range []struct {
		name, query string
		calls       int
		model       string
		status      adminapi.AdminCallStatus
	}{
		{"model", "model=model-a", 2, "model-a", ""},
		{"errors", "status=error", 1, "", adminapi.AdminCallStatusError},
		{"ok", "status=ok", 2, "", adminapi.AdminCallStatusOk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(http.MethodGet, "/calls?since="+url.QueryEscape(since)+"&until="+url.QueryEscape(until)+"&site_id="+url.QueryEscape(siteID)+"&"+tc.query, "", as(owner))
			expectAdmin(t, "filtered calls", rec, 200, "")
			var list adminapi.AdminCallList
			decode(t, rec, &list)
			wantList(t, rec, tc.name+" calls", list.Data, tc.calls)
			if tc.model != "" {
				wantPtr(t, rec, "model filter echo", list.ModelFilter, tc.model)
			}
			if tc.status != "" {
				wantPtr(t, rec, "status filter echo", list.StatusFilter, tc.status)
			}
			wantPtr(t, rec, "site filter echo", list.SiteFilter, siteID)
		})
	}

	// A platform-wide search keeps the other site's call; the dashboard's own figure does not include it.
	for query, want := range map[string]int{"": 4, "&site_id=" + url.QueryEscape(siteID): 3} {
		rec := f.do(http.MethodGet, "/calls?since="+url.QueryEscape(since)+"&until="+url.QueryEscape(until)+query, "", as(owner))
		expectAdmin(t, "site calls", rec, 200, "")
		var list adminapi.AdminCallList
		decode(t, rec, &list)
		wantList(t, rec, fmt.Sprintf("calls with %q", query), list.Data, want)
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

// TestAdminCoverageMarksAnArchivedWindowIncomplete: once October's partition is dropped, a window that starts in
// October is incomplete on both the dashboard and the call list, and the boundary is November; only November's call is
// counted, and a window wholly inside November is complete.
func TestAdminCoverageMarksAnArchivedWindowIncomplete(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	main, _, err := f.svc.SiteForHost(adminHost)
	if err != nil {
		t.Fatal(err)
	}
	site := uuid.UUID(main.ID).String()
	account := f.text("SELECT id::text FROM accounts.accounts ORDER BY id LIMIT 1")
	f.run(
		fmt.Sprintf(`INSERT INTO observability.call_log
 (id,at,site_id,account_id,workspace_id,key_id,ip,request_class,used_tools,has_image,has_file,model,endpoint_id,attempt_count,usage,service_tier,price_nano,cost_nano,currency,free_of_charge,latency_ms,first_token_ms,consent_version,training_allowed,error_kind)
 VALUES (uuidv7(),'2026-10-15T12:00:00Z','%s','%s',uuidv7(),uuidv7(),'203.0.113.7','chat',false,false,false,'model-a',uuidv7(),1,'{}','standard',1,1,'USD',false,1,0,'v',false,''),
        (uuidv7(),'2026-11-02T12:00:00Z','%s','%s',uuidv7(),uuidv7(),'203.0.113.7','chat',false,false,false,'model-b',uuidv7(),1,'{}','standard',1,1,'USD',false,1,0,'v',false,'')`, site, account, site, account),
		"DROP TABLE observability.call_log_2026_10",
		"DROP TABLE observability.call_attempt_2026_10",
	)
	firstOfNovember := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	const since, until = "2026-10-15T00:00:00Z", "2026-11-15T00:00:00Z"
	rec := f.do(http.MethodGet, "/reports/overview?since="+since+"&until="+until, "", as(owner))
	expectAdmin(t, "archived overview", rec, 200, "")
	var overview adminapi.OperationsOverview
	decode(t, rec, &overview)
	wantPtr(t, rec, "calls", overview.Calls, int64(1))
	wantPtr(t, rec, "calls incomplete", overview.CallsIncomplete, true)
	if overview.CallsAvailableSince == nil || !overview.CallsAvailableSince.Equal(firstOfNovember) {
		t.Fatalf("call coverage boundary: %s", rec.Body.String())
	}

	rec = f.do(http.MethodGet, "/calls?since="+since+"&until="+until, "", as(owner))
	expectAdmin(t, "archived calls", rec, 200, "")
	var list adminapi.AdminCallList
	decode(t, rec, &list)
	wantList(t, rec, "archived calls", list.Data, 1)
	wantPtr(t, rec, "list incomplete", list.Incomplete, true)
	if list.AvailableSince == nil || !list.AvailableSince.Equal(firstOfNovember) {
		t.Fatalf("call list coverage boundary: %s", rec.Body.String())
	}

	rec = f.do(http.MethodGet, "/reports/overview?since=2026-11-01T00:00:00Z&until=2026-11-15T00:00:00Z", "", as(owner))
	expectAdmin(t, "complete overview", rec, 200, "")
	overview = adminapi.OperationsOverview{}
	decode(t, rec, &overview)
	if overview.CallsIncomplete == nil || *overview.CallsIncomplete {
		t.Fatalf("a window inside the retained month is complete: %s", rec.Body.String())
	}
}

// wantPtr checks an optional response field: a missing field is a read the page shows as unavailable, not a zero.
func wantPtr[T comparable](t *testing.T, rec *httptest.ResponseRecorder, what string, got *T, want T) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: missing: %s", what, rec.Body.String())
	}
	if *got != want {
		t.Fatalf("%s: got %v, want %v: %s", what, *got, want, rec.Body.String())
	}
}

// wantList checks an optional response list and returns it, so the caller can check its items.
func wantList[T any](t *testing.T, rec *httptest.ResponseRecorder, what string, got *[]T, want int) []T {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: missing: %s", what, rec.Body.String())
	}
	if len(*got) != want {
		t.Fatalf("%s: got %d items, want %d: %s", what, len(*got), want, rec.Body.String())
	}
	return *got
}
