package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/observability"
	"axiom/internal/platform/consoleapi"
)

func TestUsageStatisticsWindowHTTP(t *testing.T) {
	t.Parallel()
	c := newCallsFixture(t)
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"since=2026-01-01T00:00:00Z&until=2026-04-02T00:00:00Z", 400},
		{"since=2026-01-01T00:00:00Z&until=2026-04-01T00:00:00Z", 200},
		{"granularity=hour&since=2026-01-01T00:00:00Z&until=2026-01-09T00:00:00Z", 400},
		{"granularity=hour&since=2026-01-01T00:00:00Z&until=2026-01-08T00:00:00Z", 200},
		{"since=2026-01-01T00:01:00Z&until=2026-01-02T00:00:00Z", 400},
		{"granularity=minute", 400},
		{"group_by=member", 400},
		{"compare=unknown", 400},
		{"workspace_id=bad", 400},
		{"key_id=bad", 400},
		{"granularity=hour", 200},
		{"group_by=workspace&compare=previous", 200},
	} {
		t.Run(tc.query, func(t *testing.T) {
			rec := c.f.do(http.MethodGet, "/usage/statistics?"+tc.query, "", as(c.s))
			if rec.Code != tc.status {
				t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
			}
			if tc.status == 400 && !strings.Contains(rec.Body.String(), "invalid_request") {
				t.Fatalf("error code: %s", rec.Body)
			}
		})
	}
	rec := c.f.do(http.MethodGet, "/usage/statistics", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated %d", rec.Code)
	}
}

func TestUsageStatisticsResponse(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	id := uuid.NewV7()
	metrics := observability.UsageMetrics{Currency: "USD", Requests: "2", Successes: "1", Spend: "18446744073709551614", Input: "0", CacheRateRequests: "0"}
	for _, group := range []string{"key", "workspace", "model", ""} {
		t.Run(group, func(t *testing.T) {
			name := id.String()
			if group == "model" {
				name = "public/model"
			}
			if group == "" {
				name = ""
			}
			body := usageBody(observability.UsageStatistics{
				Buckets: []observability.UsageBucket{{At: at, Group: name, UsageMetrics: metrics}},
				Totals:  []observability.UsageMetrics{metrics}, Previous: []observability.UsageMetrics{metrics}, BackfillComplete: true,
			}, observability.UsageQuery{Since: at, Until: at.Add(time.Hour), Granularity: "hour", GroupBy: group, Previous: true})
			bucket := (*body.Buckets)[0]
			checkUsageResponseIDs(t, group, bucket)
			if body.Previous == nil || *(*body.Totals)[0].Spend.Amount != metrics.Spend || *bucket.Metrics.Tokens.Input != "0" || bucket.Metrics.Tokens.Output != nil || bucket.Metrics.CacheHitRate != nil {
				t.Fatalf("response %+v", body)
			}
		})
	}
}

func checkUsageResponseIDs(t *testing.T, group string, b consoleapi.UsageBucket) {
	t.Helper()
	switch group {
	case "key":
		if b.Group == nil || !strings.HasPrefix(*b.Group, "key_") {
			t.Fatal(b.Group)
		}
	case "workspace":
		if b.Group == nil || !strings.HasPrefix(*b.Group, "ws_") {
			t.Fatal(b.Group)
		}
	case "model":
		if b.Group == nil || *b.Group != "public/model" {
			t.Fatal(b.Group)
		}
	case "":
		if b.Group != nil {
			t.Fatal(b.Group)
		}
	}
}

func TestUsageIgnoresUndeclaredCallParameters(t *testing.T) {
	t.Parallel()
	c := newCallsFixture(t)
	path := "/usage/statistics?since=2026-01-01T00:00:00Z&until=2026-01-02T00:00:00Z"
	base := c.f.do(http.MethodGet, path, "", as(c.s))
	if base.Code != http.StatusOK {
		t.Fatal(base.Body)
	}
	for _, extra := range []string{"cursor=x", "request_id=bad", "status=error", "client_app=foo"} {
		t.Run(extra, func(t *testing.T) {
			rec := c.f.do(http.MethodGet, path+"&"+extra, "", as(c.s))
			if rec.Code != base.Code || rec.Body.String() != base.Body.String() {
				t.Fatalf("unsupported parameter affected response: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestUsageQueryUsesDeclaredFilters(t *testing.T) {
	t.Parallel()
	account, key, workspace := accounts.AccountID(uuid.NewV7()), uuid.NewV7(), uuid.NewV7()
	model, keyID, workspaceID := "public/model", accounts.KeyID(key).TypeID(), accounts.WorkspaceID(workspace).TypeID()
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	until := since.Add(time.Hour)
	got, err := usageQuery(account, consoleapi.GetUsageStatisticsParams{Model: &model, KeyId: &keyID, WorkspaceId: &workspaceID, Since: &since, Until: &until})
	if err != nil {
		t.Fatal(err)
	}
	want := observability.UsageQuery{Account: uuid.UUID(account), Model: model, Key: &key, Workspace: &workspace, Since: since, Until: until, Granularity: "day"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}
