package main

import (
	"net/http"
	"testing"

	"axiom/internal/platform/adminapi"
)

// TestAdminOperationsTargets: the owner sets all four targets of a UTC month, reads them back, and the overview
// prorates them to its window and states each gap as actual minus target. A later write replaces the whole set, and a
// target left out is cleared rather than kept.
func TestAdminOperationsTargets(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	path := "/reports/targets/2026-11"

	rec := f.do(http.MethodGet, path, "", as(owner))
	expectAdmin(t, "empty targets", rec, 200, "")
	var empty adminapi.OperationsTargets
	decode(t, rec, &empty)
	if empty.Month != "2026-11" || empty.SiteId == "" || empty.Calls != nil || empty.Errors != nil || empty.Revenue != nil || empty.Cost != nil {
		t.Fatalf("a month with no targets: %s", rec.Body.String())
	}

	key := newKey()
	rec = f.change(owner, http.MethodPut, path,
		`{"calls":3100,"errors":12,"revenue":{"amount":"5000000000000","currency":"USD"},`+
			`"cost":{"amount":"3000000000000","currency":"USD"},"reason":"set November targets"}`, key)
	expectAdmin(t, "set targets", rec, 200, "")
	var set adminapi.OperationsTargets
	decode(t, rec, &set)
	wantPtr(t, rec, "calls", set.Calls, int64(3100))
	wantPtr(t, rec, "errors", set.Errors, int64(12))
	wantPtr(t, rec, "revenue", set.Revenue, adminapi.Money{Amount: "5000000000000", Currency: "USD"})
	wantPtr(t, rec, "cost", set.Cost, adminapi.Money{Amount: "3000000000000", Currency: "USD"})
	f.expectAudit("targets.update", map[string]string{
		"detail->>'month'":                      "2026-11",
		"detail->'after'->>'calls'":             "3100",
		"detail->'after'->'revenue'->>'amount'": "5000000000000",
		"detail->'before'":                      "{}",
	})

	// Two days of a 30-day month: 3100 calls and 12 errors prorate to 207 and 1; the money targets keep their exact
	// billionths and the gaps are the actual (zero, no calls in this fixture) minus those targets.
	rec = f.do(http.MethodGet, "/reports/overview?since=2026-11-01T00:00:00Z&until=2026-11-03T00:00:00Z", "", as(owner))
	expectAdmin(t, "overview with targets", rec, 200, "")
	var overview adminapi.OperationsOverview
	decode(t, rec, &overview)
	if overview.Targets == nil {
		t.Fatalf("the overview has no targets object: %s", rec.Body.String())
	}
	wantPtr(t, rec, "calls target", &overview.Targets.Calls.Target, int64(207))
	wantPtr(t, rec, "calls gap", overview.Targets.Calls.Gap, int64(-207))
	wantPtr(t, rec, "errors target", &overview.Targets.Errors.Target, int64(1))
	wantPtr(t, rec, "errors gap", overview.Targets.Errors.Gap, int64(-1))
	wantPtr(t, rec, "revenue target", &overview.Targets.Revenue.Target, adminapi.Money{Amount: "333333333333", Currency: "USD"})
	wantPtr(t, rec, "revenue gap", overview.Targets.Revenue.Gap, adminapi.Money{Amount: "-333333333333", Currency: "USD"})
	wantPtr(t, rec, "cost target", &overview.Targets.Cost.Target, adminapi.Money{Amount: "200000000000", Currency: "USD"})
	wantPtr(t, rec, "cost gap", overview.Targets.Cost.Gap, adminapi.Money{Amount: "-200000000000", Currency: "USD"})

	// Replaying the first request returns its response without writing or auditing again.
	replay := f.change(owner, http.MethodPut, path,
		`{"calls":3100,"errors":12,"revenue":{"amount":"5000000000000","currency":"USD"},`+
			`"cost":{"amount":"3000000000000","currency":"USD"},"reason":"set November targets"}`, key)
	expectAdmin(t, "replayed targets", replay, 200, "")
	if got := replay.Header().Get("Idempotency-Replayed"); got != "true" {
		t.Fatalf("replayed header: %q", got)
	}
	if n := f.auditCount("targets.update"); n != "1" {
		t.Fatalf("a replay wrote %s audit entries, want 1", n)
	}

	// The next write is the whole set: the errors, revenue and cost targets are gone, not carried over.
	rec = f.change(owner, http.MethodPut, path, `{"calls":4000,"reason":"raise calls"}`, newKey())
	expectAdmin(t, "replace targets", rec, 200, "")
	var replaced adminapi.OperationsTargets
	decode(t, rec, &replaced)
	wantPtr(t, rec, "replaced calls", replaced.Calls, int64(4000))
	if replaced.Errors != nil || replaced.Revenue != nil || replaced.Cost != nil {
		t.Fatalf("targets left out were kept: %s", rec.Body.String())
	}
	if got := f.text("SELECT detail->'before'->>'errors' FROM platform.audit_log WHERE action = 'targets.update' ORDER BY at DESC LIMIT 1"); got != "12" {
		t.Errorf("the replaced errors target in the audit entry: %q, want 12", got)
	}
	if got := f.text("SELECT coalesce(detail->'after'->>'errors', '') FROM platform.audit_log WHERE action = 'targets.update' ORDER BY at DESC LIMIT 1"); got != "" {
		t.Errorf("the cleared errors target in the audit entry: %q, want none", got)
	}
	if n := f.auditCount("targets.update"); n != "2" {
		t.Fatalf("%s audit entries after the second write, want 2", n)
	}

	// Clearing the set is a write too: the month stays answerable, with every target absent.
	rec = f.change(owner, http.MethodPut, path, `{"reason":"clear the month"}`, newKey())
	expectAdmin(t, "clear targets", rec, 200, "")
	var cleared adminapi.OperationsTargets
	decode(t, rec, &cleared)
	if cleared.Calls != nil || cleared.Errors != nil || cleared.Revenue != nil || cleared.Cost != nil {
		t.Fatalf("a cleared month still has targets: %s", rec.Body.String())
	}
	if got := f.text("SELECT detail->'before'->>'calls' FROM platform.audit_log WHERE action = 'targets.update' ORDER BY at DESC LIMIT 1"); got != "4000" {
		t.Errorf("the cleared calls target in the audit entry: %q, want 4000", got)
	}
	if n := f.auditCount("targets.update"); n != "3" {
		t.Fatalf("%s audit entries after clearing, want 3", n)
	}

	// Zero is a set target, not an absent one.
	rec = f.change(owner, http.MethodPut, "/reports/targets/2026-12", `{"calls":0,"reason":"zero calls"}`, newKey())
	expectAdmin(t, "zero target", rec, 200, "")
	rec = f.do(http.MethodGet, "/reports/targets/2026-12", "", as(owner))
	expectAdmin(t, "zero target read", rec, 200, "")
	var zero adminapi.OperationsTargets
	decode(t, rec, &zero)
	wantPtr(t, rec, "zero calls", zero.Calls, int64(0))
}

// TestAdminOperationsTargetsNeedSitesManage: the same permission that shows the dashboard sets its targets; a role
// without it cannot read or write them, whether or not the month has targets.
func TestAdminOperationsTargetsNeedSitesManage(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	f.addUser("viewer@example.com", "viewer")
	viewer := f.verified("viewer@example.com")
	for _, tc := range []struct {
		method, body string
	}{
		{http.MethodGet, ""},
		{http.MethodPut, `{"calls":1,"reason":"r"}`},
	} {
		rec := f.do(tc.method, "/reports/targets/2026-11", tc.body, as(viewer), idempotent(newKey()))
		expectAdmin(t, "viewer "+tc.method, rec, http.StatusForbidden, codeForbidden)
	}
}

// TestAdminOperationsTargetsRefuseAnotherCurrency: a money target is in the site's accounting currency, so a target
// in another currency is a field error before anything is written.
func TestAdminOperationsTargetsRefuseAnotherCurrency(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	rec := f.change(f.owner(), http.MethodPut, "/reports/targets/2026-11",
		`{"revenue":{"amount":"1","currency":"CNY"},"reason":"r"}`, newKey())
	expectAdmin(t, "another currency", rec, http.StatusBadRequest, codeInvalidRequest)
	detail := errorDetailOf(t, rec)
	if detail.Fields == nil || len(*detail.Fields) != 1 || (*detail.Fields)[0].Pointer != "/revenue/currency" {
		t.Fatalf("the field error: %s", rec.Body.String())
	}
}
