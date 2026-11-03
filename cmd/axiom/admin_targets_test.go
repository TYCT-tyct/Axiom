package main

import (
	"net/http"
	"testing"

	"axiom/internal/operations"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
)

// TestAdminOperationsTargets: the owner sets all four targets of a UTC month, reads them back, and the overview
// prorates them to its window and states each gap as actual minus target. A later write replaces the whole set, and a
// target left out is cleared rather than kept.
//
//nolint:cyclop // one admin journey: set, read back, prorate, replay, replace and clear.
func TestAdminOperationsTargets(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	path := "/reports/targets/2026-11"

	rec := f.do(http.MethodGet, path, "", as(owner))
	expectAdmin(t, "empty targets", rec, 200, "")
	var empty adminapi.OperationsTargets
	decode(t, rec, &empty)
	if empty.Month != "2026-11" || empty.Rev != 0 || empty.SiteId == "" || empty.Calls != nil || empty.Errors != nil || empty.Revenue != nil || empty.Cost != nil {
		t.Fatalf("a month with no targets: %s", rec.Body.String())
	}

	key := newKey()
	rec = f.change(owner, http.MethodPut, path,
		`{"calls":3100,"errors":12,"revenue":{"amount":"5000000000000","currency":"USD"},`+
			`"cost":{"amount":"3000000000000","currency":"USD"},"rev":0,"reason":"set November targets"}`, key)
	expectAdmin(t, "set targets", rec, 200, "")
	var set adminapi.OperationsTargets
	decode(t, rec, &set)
	if set.Rev != 1 {
		t.Fatalf("the first write revision: %d, want 1", set.Rev)
	}
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
			`"cost":{"amount":"3000000000000","currency":"USD"},"rev":0,"reason":"set November targets"}`, key)
	expectAdmin(t, "replayed targets", replay, 200, "")
	if got := replay.Header().Get("Idempotency-Replayed"); got != "true" {
		t.Fatalf("replayed header: %q", got)
	}
	if n := f.auditCount("targets.update"); n != "1" {
		t.Fatalf("a replay wrote %s audit entries, want 1", n)
	}

	// The next write is the whole set: the errors, revenue and cost targets are gone, not carried over.
	rec = f.change(owner, http.MethodPut, path, `{"calls":4000,"rev":1,"reason":"raise calls"}`, newKey())
	expectAdmin(t, "replace targets", rec, 200, "")
	var replaced adminapi.OperationsTargets
	decode(t, rec, &replaced)
	if replaced.Rev != 2 {
		t.Fatalf("the second write revision: %d, want 2", replaced.Rev)
	}
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
	rec = f.change(owner, http.MethodPut, path, `{"rev":2,"reason":"clear the month"}`, newKey())
	expectAdmin(t, "clear targets", rec, 200, "")
	var cleared adminapi.OperationsTargets
	decode(t, rec, &cleared)
	if cleared.Rev != 3 {
		t.Fatalf("the clear revision: %d, want 3", cleared.Rev)
	}
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
	rec = f.change(owner, http.MethodPut, "/reports/targets/2026-12", `{"calls":0,"rev":0,"reason":"zero calls"}`, newKey())
	expectAdmin(t, "zero target", rec, 200, "")
	rec = f.do(http.MethodGet, "/reports/targets/2026-12", "", as(owner))
	expectAdmin(t, "zero target read", rec, 200, "")
	var zero adminapi.OperationsTargets
	decode(t, rec, &zero)
	if zero.Rev != 1 {
		t.Fatalf("the zero-target revision: %d, want 1", zero.Rev)
	}
	wantPtr(t, rec, "zero calls", zero.Calls, int64(0))
}

// TestAdminOperationsTargetsRefuseAStaleForm: two editors read the same empty month. The first write wins; the
// second form, even though it changes a different field, is refused until it reads the month again. The retry carries
// the new revision and keeps the first editor's calls target.
func TestAdminOperationsTargetsRefuseAStaleForm(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	path := "/reports/targets/2027-03"

	rec := f.do(http.MethodGet, path, "", as(owner))
	expectAdmin(t, "empty month", rec, 200, "")
	var read adminapi.OperationsTargets
	decode(t, rec, &read)
	if read.Rev != 0 {
		t.Fatalf("an untouched month is at revision %d, want 0", read.Rev)
	}

	rec = f.change(owner, http.MethodPut, path,
		`{"calls":3100,"errors":12,"rev":0,"reason":"first editor"}`, newKey())
	expectAdmin(t, "first editor", rec, 200, "")
	var first adminapi.OperationsTargets
	decode(t, rec, &first)
	if first.Rev != 1 {
		t.Fatalf("the first editor's revision: %d, want 1", first.Rev)
	}

	// The second editor still holds revision 0 and only wants to change errors. It must not restore calls=3100 over
	// a value it never saw, so the whole set is refused before anything is written.
	rec = f.change(owner, http.MethodPut, path,
		`{"errors":5,"rev":0,"reason":"second editor from the old form"}`, newKey())
	expectAdmin(t, "stale second editor", rec, http.StatusConflict, codeStaleRevision)
	if n := f.auditCount("targets.update"); n != "1" {
		t.Fatalf("a stale write produced %s audit entries, want 1", n)
	}

	rec = f.do(http.MethodGet, path, "", as(owner))
	expectAdmin(t, "targets after stale write", rec, 200, "")
	var current adminapi.OperationsTargets
	decode(t, rec, &current)
	if current.Rev != 1 {
		t.Fatalf("the stale write moved the revision to %d, want 1", current.Rev)
	}
	wantPtr(t, rec, "calls after stale write", current.Calls, int64(3100))
	wantPtr(t, rec, "errors after stale write", current.Errors, int64(12))

	// Reloading gives revision 1; the same edit now goes through without disturbing calls.
	rec = f.change(owner, http.MethodPut, path,
		`{"calls":3100,"errors":5,"rev":1,"reason":"second editor after reload"}`, newKey())
	expectAdmin(t, "retried second editor", rec, 200, "")
	var retried adminapi.OperationsTargets
	decode(t, rec, &retried)
	if retried.Rev != 2 {
		t.Fatalf("the retry revision: %d, want 2", retried.Rev)
	}
	wantPtr(t, rec, "calls after retry", retried.Calls, int64(3100))
	wantPtr(t, rec, "errors after retry", retried.Errors, int64(5))
}

// TestAdminOperationsTargetsRefuseAStaleFormAfterClear: clearing is a write and moves the revision, even though no
// target row remains. A form holding the pre-clear revision cannot restore the old targets; a form that reads the
// cleared month can set a new target from revision 2.
func TestAdminOperationsTargetsRefuseAStaleFormAfterClear(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	path := "/reports/targets/2027-04"

	rec := f.change(owner, http.MethodPut, path, `{"calls":10,"rev":0,"reason":"set"}`, newKey())
	expectAdmin(t, "set before clear", rec, 200, "")
	var set adminapi.OperationsTargets
	decode(t, rec, &set)
	if set.Rev != 1 {
		t.Fatalf("the set revision: %d, want 1", set.Rev)
	}

	rec = f.change(owner, http.MethodPut, path, `{"rev":1,"reason":"clear"}`, newKey())
	expectAdmin(t, "clear", rec, 200, "")
	var cleared adminapi.OperationsTargets
	decode(t, rec, &cleared)
	if cleared.Rev != 2 || cleared.Calls != nil || cleared.Errors != nil {
		t.Fatalf("the cleared month: %+v, want revision 2 and no targets", cleared)
	}

	// The old form still has revision 1 and would restore calls=10. The clear moved the month, so it is stale.
	rec = f.change(owner, http.MethodPut, path, `{"calls":10,"rev":1,"reason":"old form"}`, newKey())
	expectAdmin(t, "stale after clear", rec, http.StatusConflict, codeStaleRevision)

	rec = f.do(http.MethodGet, path, "", as(owner))
	expectAdmin(t, "after clear and stale write", rec, 200, "")
	var current adminapi.OperationsTargets
	decode(t, rec, &current)
	if current.Rev != 2 || current.Calls != nil || current.Errors != nil {
		t.Fatalf("the stale form changed the cleared month: %+v", current)
	}

	// Reading the cleared month gives revision 2; a new target from that snapshot is accepted and revision 3.
	rec = f.change(owner, http.MethodPut, path, `{"calls":7,"rev":2,"reason":"new target after clear"}`, newKey())
	expectAdmin(t, "set after clear", rec, 200, "")
	var after adminapi.OperationsTargets
	decode(t, rec, &after)
	if after.Rev != 3 {
		t.Fatalf("the post-clear set revision: %d, want 3", after.Rev)
	}
	wantPtr(t, rec, "calls after clear", after.Calls, int64(7))
}

// TestAdminOperationsTargetsKeepWideMoney: a target is a planning figure, not a ledger entry, so its money amount
// keeps the table's numeric(38,0) range. An amount above int64 and the 38-digit maximum are accepted and read back
// exactly; 39 digits is refused at the field rather than reaching PostgreSQL.
func TestAdminOperationsTargetsKeepWideMoney(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	path := "/reports/targets/2027-05"

	const aboveInt64 = "9223372036854775808"
	const max38 = "99999999999999999999999999999999999999"
	const over38 = "999999999999999999999999999999999999999" // 39 digits

	rec := f.change(owner, http.MethodPut, path,
		`{"revenue":{"amount":"`+aboveInt64+`","currency":"USD"},"rev":0,"reason":"wide revenue"}`, newKey())
	expectAdmin(t, "above int64", rec, 200, "")
	var wide adminapi.OperationsTargets
	decode(t, rec, &wide)
	wantPtr(t, rec, "revenue above int64", wide.Revenue, adminapi.Money{Amount: aboveInt64, Currency: "USD"})
	if wide.Rev != 1 {
		t.Fatalf("the wide write revision: %d, want 1", wide.Rev)
	}

	rec = f.change(owner, http.MethodPut, path,
		`{"revenue":{"amount":"`+max38+`","currency":"USD"},"rev":1,"reason":"38 digits"}`, newKey())
	expectAdmin(t, "38 digits", rec, 200, "")
	var max adminapi.OperationsTargets
	decode(t, rec, &max)
	wantPtr(t, rec, "38-digit revenue", max.Revenue, adminapi.Money{Amount: max38, Currency: "USD"})

	rec = f.change(owner, http.MethodPut, path,
		`{"revenue":{"amount":"`+over38+`","currency":"USD"},"rev":2,"reason":"too wide"}`, newKey())
	expectAdmin(t, "39 digits", rec, http.StatusBadRequest, codeInvalidRequest)
	detail := errorDetailOf(t, rec)
	if detail.Fields == nil || len(*detail.Fields) != 1 || (*detail.Fields)[0].Pointer != "/revenue/amount" {
		t.Fatalf("the wide-money field error: %s", rec.Body.String())
	}
}

// TestTargetComparisonHidesAnUnknownCostGap: the cost total leaves out calls whose supplier cost is unknown. A target
// may still be shown, but a gap would be actual minus a total that is not the whole window; it appears only once the
// cost is complete.
func TestTargetComparisonHidesAnUnknownCostGap(t *testing.T) {
	t.Parallel()
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	expected := operations.Expected{Cost: &operations.Money{Amount: "100000000000", Currency: currency}}
	incomplete := targetComparison(expected, 0, 0, "0", "50000000000", currency, true, false)
	if incomplete.Cost == nil || incomplete.Cost.Target.Amount != "100000000000" {
		t.Fatalf("an incomplete cost should keep its target: %+v", incomplete.Cost)
	}
	if incomplete.Cost.Gap != nil {
		t.Fatalf("an incomplete cost showed a gap: %+v", incomplete.Cost.Gap)
	}
	complete := targetComparison(expected, 0, 0, "0", "50000000000", currency, true, true)
	if complete.Cost == nil || complete.Cost.Gap == nil || complete.Cost.Gap.Amount != "-50000000000" {
		t.Fatalf("a complete cost should show actual minus target: %+v", complete.Cost)
	}
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
		{http.MethodPut, `{"calls":1,"rev":0,"reason":"r"}`},
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
		`{"revenue":{"amount":"1","currency":"CNY"},"rev":0,"reason":"r"}`, newKey())
	expectAdmin(t, "another currency", rec, http.StatusBadRequest, codeInvalidRequest)
	detail := errorDetailOf(t, rec)
	if detail.Fields == nil || len(*detail.Fields) != 1 || (*detail.Fields)[0].Pointer != "/revenue/currency" {
		t.Fatalf("the field error: %s", rec.Body.String())
	}
}
