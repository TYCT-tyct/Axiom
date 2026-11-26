package main

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/observability"
	"axiom/internal/platform/adminapi"
	"axiom/internal/suppliers"
)

// TestAdminOperationsMonitorAndPluginStats: both reads answer the owner from the rollups, refuse a role without
// sites.manage, and refuse a window they do not have; the call list refuses a plugin it does not know.
func TestAdminOperationsMonitorAndPluginStats(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	f.addUser("member@example.com", "member")
	member := f.verified("member@example.com")
	endpoint := uuid.NewV7() // not a suppliers' endpoint: its calls are in the totals and on no line
	minute := `date_bin('60 seconds', now() - interval '3 minutes', '1970-01-01T00:00:00Z'::timestamptz)`
	hour := `date_bin('3600 seconds', now() - interval '3 minutes', '1970-01-01T00:00:00Z'::timestamptz)`
	for _, b := range []struct{ width, bucket string }{{"60", minute}, {"3600", hour}} {
		f.run(
			fmt.Sprintf(`INSERT INTO observability.monitor_counts VALUES
 (%[1]s, %[2]s, '%[3]s', '', 5, 1, 900), (%[1]s, %[2]s, '%[3]s', 'timeout', 2, 0, 0),
 (%[1]s, %[2]s, '00000000-0000-0000-0000-000000000000', 'invalid_request', 1, 0, 0)`, b.width, b.bucket, endpoint),
			fmt.Sprintf(`INSERT INTO observability.monitor_latency VALUES
 (%[1]s, %[2]s, 1, '%[3]s', 16, 5), (%[1]s, %[2]s, 2, '%[3]s', 21, 4), (%[1]s, %[2]s, 2, '%[3]s', 40, 1)`, b.width, b.bucket, endpoint),
		)
	}
	f.run(`INSERT INTO observability.plugin_hourly VALUES (date_trunc('hour', now()), 'web', 'USD', 3, 1, 2, 1500, 3, 5000000, 0),
 (date_trunc('hour', now()), 'web', 'EUR', 1, 0, 1, 500, 1, 7, 1),
 (date_trunc('hour', now()), 'file-parser', 'USD', 1, 0, 3, 1000, 1, 0, 1)`)

	checkMonitorAnswer(t, f, owner, "", 60, 60)
	checkMonitorAnswer(t, f, owner, "?window=24h", 24, 3600)

	// The EUR runs count in the totals and not in the USD site's revenue, so neither does the EUR run whose charge is
	// unknown; the parse's is unknown in USD, so the parser has no revenue rather than zero. Two days ago neither ran.
	day := time.Hour * 24
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"", []string{
			fmt.Sprint(adminapi.Web, 4, 1, 3, 500, true, adminapi.Money{Amount: "5000000", Currency: "USD"}, 24),
			fmt.Sprint(adminapi.FileParser, 1, 0, 3, 1000, false, adminapi.Money{}, 24),
		}},
		{"?since=" + url.QueryEscape(time.Now().Add(-2*day).Format(time.RFC3339)) + "&until=" + url.QueryEscape(time.Now().Add(-day-time.Hour).Format(time.RFC3339)), []string{
			fmt.Sprint(adminapi.Web, 0, 0, 0, 0, true, adminapi.Money{Amount: "0", Currency: "USD"}, 23),
			fmt.Sprint(adminapi.FileParser, 0, 0, 0, 0, true, adminapi.Money{Amount: "0", Currency: "USD"}, 23),
		}},
	} {
		rec := f.do(http.MethodGet, "/reports/plugins"+c.query, "", as(owner))
		expectAdmin(t, "plugins", rec, 200, "")
		var stats adminapi.PluginStats
		decode(t, rec, &stats)
		plugins := make([]string, len(stats.Plugins))
		for i, p := range stats.Plugins {
			plugins[i] = fmt.Sprint(p.Plugin, p.Calls, p.Failures, p.Uses, valueOf(p.MeanMs), p.Revenue != nil, valueOf(p.Revenue), len(p.Hours))
		}
		if !slices.Equal(plugins, c.want) {
			t.Errorf("plugins%s = %v, want %v: %s", c.query, plugins, c.want, rec.Body)
		}
	}

	for _, c := range []struct {
		name, path string
		session    session
		status     int
	}{
		{"monitor without sites.manage", "/reports/monitor", member, 403},
		{"plugins without sites.manage", "/reports/plugins", member, 403},
		{"a window the monitor has not", "/reports/monitor?window=7d", owner, 400},
		{"plugins over more than 31 days", "/reports/plugins?since=2026-01-01T00:00:00Z&until=2026-03-01T00:00:00Z", owner, 400},
		{"a plugin the list does not know", "/calls?plugin=" + url.QueryEscape("code"), owner, 400},
	} {
		expectAdmin(t, c.name, f.do(http.MethodGet, c.path, "", as(c.session)), c.status, "")
	}
}

// checkMonitorAnswer checks one window of the rows TestAdminOperationsMonitorAndPluginStats wrote.
func checkMonitorAnswer(t *testing.T, f adminFixture, owner session, window string, buckets, seconds int) {
	t.Helper()
	rec := f.do(http.MethodGet, "/reports/monitor"+window, "", as(owner))
	expectAdmin(t, "monitor"+window, rec, 200, "")
	var m adminapi.OperationsMonitor
	decode(t, rec, &m)
	durations := make([]string, len(m.Durations))
	for i, d := range m.Durations {
		durations[i] = fmt.Sprint(valueOf(d.BelowMs), ":", d.Calls)
	}
	got := fmt.Sprint(len(m.Buckets), m.BucketSeconds, m.Calls, m.Failures, m.SwitchedCalls, m.Tokens,
		valueOf(m.FirstTokenMs.P50), valueOf(m.TotalMs.P99), durations, m.FailureCauses, len(m.Lines))
	// Four completions in [1 s, 2 s) and one in [60 s, 120 s); the endpoint is no supplier's, so there is no line.
	want := fmt.Sprint(buckets, seconds, 8, 3, 1, 900, 350, 90000,
		[]string{"1000:0", "2000:4", "5000:0", "10000:0", "20000:0", "30000:0", "60000:0", "120000:1", "0:0"},
		[]adminapi.MonitorFailureCause{{ErrorKind: "timeout", Upstream: true, Calls: 2}, {ErrorKind: "invalid_request", Calls: 1}}, 0)
	if got != want {
		t.Errorf("monitor%s = %s, want %s: %s", window, got, want, rec.Body)
	}
}

// TestMonitorLinesAddEndpointsUpByGroup: two endpoints of one group make one line, most calls first.
func TestMonitorLinesAddEndpointsUpByGroup(t *testing.T) {
	t.Parallel()
	supplier, big, small := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	a, b, c, gone := uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	var fast, slow observability.Histogram
	fast[16], slow[21] = 1, 1
	lines := monitorLines([]observability.MonitorEndpoint{
		{Endpoint: c, Calls: 1},
		{Endpoint: a, Calls: 2, Failures: 1, FirstToken: fast},
		{Endpoint: b, Calls: 2, FirstToken: slow},
		{Endpoint: gone, Calls: 9},
	}, map[uuid.UUID]suppliers.Line{a: {Supplier: supplier, Group: big}, b: {Supplier: supplier, Group: big}, c: {Supplier: supplier, Group: small}})
	want := fmt.Sprint([]map[string]any{
		{"supplier_id": supplier.String(), "group_id": big.String(), "calls": int64(4), "failures": int64(1), "first_token_ms_p50": int64(350)},
		{"supplier_id": supplier.String(), "group_id": small.String(), "calls": int64(1), "failures": int64(0)},
	})
	if got := fmt.Sprint(lines); got != want {
		t.Errorf("lines = %s, want %s", got, want)
	}
}

// TestAdminOperationsOverviewDashboard: the dashboard's rollup figures, each UTC day the window touches, and the
// ledger's money by day beside them.
func TestAdminOperationsOverviewDashboard(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	main, _, err := f.svc.SiteForHost(adminHost)
	if err != nil {
		t.Fatal(err)
	}
	site := uuid.UUID(main.ID).String()
	big, small := uuid.NewV7(), uuid.NewV7()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	since, until := today.Add(-12*time.Hour), today.Add(12*time.Hour)
	at := func(d time.Duration) string { return today.Add(d).Format(time.RFC3339) }
	f.run(
		fmt.Sprintf(`INSERT INTO observability.call_log
 (id,at,site_id,account_id,workspace_id,key_id,ip,request_class,used_tools,has_image,has_file,model,endpoint_id,attempt_count,usage,service_tier,price_nano,cost_nano,currency,free_of_charge,latency_ms,first_token_ms,consent_version,training_allowed,error_kind)
 VALUES (uuidv7(), '%[2]s', '%[1]s', '%[3]s', uuidv7(), uuidv7(), '203.0.113.7', 'chat', false, false, false, 'model-a', uuidv7(), 1, '{}', 'standard', 1, 1, 'USD', false, 1, 0, 'v', false, '')`,
			site, at(time.Hour), big),
		// The last row's hour starts before the window, so the rollup figures leave it out.
		fmt.Sprintf(`INSERT INTO observability.usage_hourly VALUES
 ('%[1]s', '%[2]s', '%[4]s', uuidv7(), uuidv7(), 'model-a', 'USD', 3, 2, 100, 10, 20, 5, 0, 0, 0, 9000),
 ('%[1]s', '%[3]s', '%[5]s', uuidv7(), uuidv7(), 'model-b', 'USD', 1, 1, 50, 5, 0, 0, 0, 0, 0, 1000),
 ('%[1]s', '%[3]s', '%[6]s', uuidv7(), uuidv7(), 'model-a', 'USD', 7, 7, 1, 1, 1, 1, 0, 0, 0, 1)`,
			site, big, small, at(time.Hour), at(-2*time.Hour), at(-13*time.Hour)),
		fmt.Sprintf(`INSERT INTO observability.first_token_hourly VALUES ('%s', '%s', 16, 3)`, site, at(time.Hour)),
		// The ledger after a correction: big's 9000 of the rollup is now 600, so small spent the most.
		`INSERT INTO billing.holders (id, expires_at) VALUES (uuidv7(), now() + interval '1 hour')`,
		fmt.Sprintf(`INSERT INTO billing.reservations (request_id, site_id, account_id, workspace_id, key_id, holder_id,
 workspace_window, key_window, segment, amount, state, deadline, charged, profit, created_at)
 SELECT uuidv7(), '%s', v.account::uuid, uuidv7(), uuidv7(), (SELECT id FROM billing.holders ORDER BY id LIMIT 1), now(), now(),
  1, 5, 'settled', now() + interval '1 hour', v.charged, 0, now()
 FROM (VALUES ('%s', 600), ('%s', 1000)) v(account, charged)`, site, big, small),
	)
	rec := f.do(http.MethodGet, "/reports/overview?since="+url.QueryEscape(since.Format(time.RFC3339))+"&until="+url.QueryEscape(until.Format(time.RFC3339)), "", as(owner))
	expectAdmin(t, "overview", rec, 200, "")
	var o adminapi.OperationsOverview
	decode(t, rec, &o)
	wantPtr(t, rec, "tokens", o.Tokens, adminapi.OverviewTokens{Input: 150, CacheRead: 20, CacheWrite: 5, Output: 15})
	wantPtr(t, rec, "active accounts", o.ActiveAccounts, int64(2))
	wantPtr(t, rec, "first token", o.FirstTokenMsP50, 350)
	newUsers := f.text(fmt.Sprintf("SELECT count(*) FROM accounts.users WHERE site_id = '%s'", site)) // all signed up during the test
	if got := fmt.Sprint(valueOf(o.NewUsers)); got != newUsers {
		t.Errorf("new users = %s, want %s", got, newUsers)
	}
	// The spenders are the ledger's, like the revenue; the model's revenue is what its calls were charged when recorded.
	wantPtr(t, rec, "revenue", o.Revenue, adminapi.Money{Amount: "1600", Currency: "USD"})
	spenders := wantList(t, rec, "top spenders", o.TopSpenders, 2)
	if got := fmt.Sprintf("%v %s %s", spenders[0].AccountId == accounts.AccountID(small).TypeID(), spenders[0].Spend.Amount, spenders[1].Spend.Amount); got != "true 1000 600" {
		t.Errorf("spenders = %s: %s", got, rec.Body)
	}
	models := wantList(t, rec, "top models", o.TopModels, 1)
	wantPtr(t, rec, "model tokens", models[0].Tokens, int64(135))
	wantPtr(t, rec, "model revenue", models[0].Revenue, adminapi.Money{Amount: "9000", Currency: "USD"})
	days := wantList(t, rec, "days", o.Days, 2)
	got := make([]string, len(days))
	for i, d := range days {
		got[i] = fmt.Sprint(d.Date.String(), d.Calls, d.Errors, valueOf(d.FirstTokenMsP50), d.Revenue.Amount)
	}
	// The calls settled today, so yesterday's ledger day is zero.
	if want := []string{fmt.Sprint(today.Add(-24*time.Hour).Format(time.DateOnly), 1, 0, 0, "0"), fmt.Sprint(today.Format(time.DateOnly), 3, 1, 350, "1600")}; !slices.Equal(got, want) {
		t.Errorf("days = %v, want %v", got, want)
	}
}
