package main

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"axiom/internal/observability"

	"axiom/internal/platform/adminapi"
)

func TestAdminSearchSeparatesSupportAndOperator(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	f.addUser("support@example.com", "support")
	support := f.verified("support@example.com")
	request := "0198c0a8-0000-7000-8000-000000000001"
	f.run(fmt.Sprintf(`INSERT INTO observability.call_log
 (id,at,site_id,account_id,workspace_id,key_id,ip,request_class,used_tools,has_image,has_file,model,endpoint_id,attempt_count,usage,service_tier,price_nano,cost_nano,currency,free_of_charge,latency_ms,first_token_ms,consent_version,training_allowed,route)
 SELECT '%s',now(),site_id,id,uuidv7(),uuidv7(),'203.0.113.7','chat',false,false,false,'test-model',uuidv7(),2,'{}','standard',9007199254740993,789,'USD',false,12,0,'v',false,'{"operator":"private-route"}' FROM accounts.accounts LIMIT 1`, request))
	f.run(fmt.Sprintf(`INSERT INTO observability.call_attempt(request_id,at,attempt_seq,site_id,account_id,endpoint_id,supplier_id,source_class,egress,outcome,started_at,finished_at,first_token_ms)
 SELECT id,at,n,site_id,account_id,uuidv7(),uuidv7(),'official','direct','committed',at,at,0 FROM observability.call_log CROSS JOIN generate_series(1,2) n WHERE id='%s'`, request))
	rec := f.do(http.MethodGet, "/calls?request_id="+request, "", as(support))
	expectAdmin(t, "support search", rec, 200, "")
	for _, hidden := range []string{"cost", "endpoint", "supplier", "route", "789"} {
		if strings.Contains(rec.Body.String(), hidden) {
			t.Fatalf("support leaked %s: %s", hidden, rec.Body.String())
		}
	}
	var list adminapi.AdminCallList
	decode(t, rec, &list)
	if list.Data == nil || len(*list.Data) != 1 || (*list.Data)[0].Price.Amount != "9007199254740993" {
		t.Fatal(rec.Body.String())
	}
	expectAdmin(t, "support cannot inspect", f.do(http.MethodGet, "/calls/"+request+"/trace", "", as(support)), 403, "")
	expectAdmin(t, "support cannot read audit", f.do(http.MethodGet, "/audit", "", as(support)), 403, "")
	checkOperatorSearch(t, f, owner, request)
}

func checkInvalidAdminSearch(t *testing.T, f adminFixture, owner session) {
	t.Helper()
	for _, query := range []string{"?since=2020-01-01T00:00:00Z", "?cursor=bogus", "?since=2026-10-30T00:00:00Z&until=2026-10-29T00:00:00Z"} {
		expectAdmin(t, "invalid call query", f.do(http.MethodGet, "/calls"+query, "", as(owner)), 400, "")
		expectAdmin(t, "invalid audit query", f.do(http.MethodGet, "/audit"+query, "", as(owner)), 400, "")
	}
}

func checkOperatorSearch(t *testing.T, f adminFixture, owner session, request string) {
	t.Helper()
	rec := f.do(http.MethodGet, "/calls/"+request+"/trace", "", as(owner))
	expectAdmin(t, "operator trace", rec, 200, "")
	var trace adminapi.AdminCallTrace
	decode(t, rec, &trace)
	if trace.Cost == nil || trace.Cost.Amount != "789" || trace.Attempts == nil || len(*trace.Attempts) != 2 || *(*trace.Attempts)[0].Seq != 1 || *(*trace.Attempts)[1].Seq != 2 {
		t.Fatal(rec.Body.String())
	}
	checkInvalidAdminSearch(t, f, owner)

	expectAdmin(t, "trace missing", f.do(http.MethodGet, "/calls/invalid/trace", "", as(owner)), 404, "")
	rec = f.do(http.MethodGet, "/audit?module=accounts&limit=1", "", as(owner))
	expectAdmin(t, "audit", rec, 200, "")
	var logs adminapi.AuditEntryList
	decode(t, rec, &logs)
	if logs.Data == nil || len(*logs.Data) != 1 {
		t.Fatal(rec.Body.String())
	}
	entry := (*logs.Data)[0]
	rec = f.do(http.MethodGet, "/audit/"+*entry.Id, "", as(owner))
	expectAdmin(t, "audit detail", rec, 200, "")
	expectAdmin(t, "audit missing", f.do(http.MethodGet, "/audit/invalid", "", as(owner)), 404, "")
}

func TestAdminSearchWindowBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, query string
		valid       bool
	}{
		{"defaults", "", true},
		{"historical end", "until=2020-02-01T00:00:00Z", true},
		{"31 days", "since=2020-01-01T00:00:00Z&until=2020-02-01T00:00:00Z", true},
		{"too wide", "since=2020-01-01T00:00:00Z&until=2020-02-02T00:00:00Z", false},
		{"empty", "since=2020-01-01T00:00:00Z&until=2020-01-01T00:00:00Z", false},
		{"bad time", "since=tomorrow", false},
		{"invalid cursor time", "cursor=" + (observability.Cursor{At: time.UnixMicro(math.MinInt64)}).String(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			since, until, _, err := adminSearchWindow(v)
			if (err == nil) != tc.valid {
				t.Fatalf("window: %v", err)
			}
			if tc.valid && v.Get("since") == "" && until.Sub(since) != 24*time.Hour {
				t.Fatalf("default window %s to %s", since, until)
			}
		})
	}
}
