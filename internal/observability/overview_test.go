package observability

import (
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestReadAdminOverviewCountsGroupsAndLimits: the overview counts only the site's window, sums errors as any error
// kind, groups by account and by model with the busiest first, and lists no more than its top ten.
func TestReadAdminOverviewCountsGroupsAndLimits(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	site, elsewhere := uuid.NewV7(), uuid.NewV7()
	at := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	a, b := uuid.NewV7(), uuid.NewV7()
	call := func(account, recordSite uuid.UUID, when time.Time, model, errorKind string) CallRecord {
		r := record(account, when)
		r.Site, r.Model, r.ErrorKind = recordSite, model, errorKind
		return r
	}
	records := []CallRecord{
		call(a, site, at, "m1", ""),
		call(a, site, at.Add(time.Minute), "m1", ""),
		call(a, site, at.Add(2*time.Minute), "m2", "upstream"),
		call(b, site, at.Add(3*time.Minute), "m1", ""),
		call(a, site, at.Add(-time.Hour), "m1", ""),         // before the window
		call(a, elsewhere, at.Add(4*time.Minute), "m1", ""), // another site
	}
	for range 12 {
		records = append(records, call(uuid.NewV7(), site, at.Add(5*time.Minute), "m1", ""))
	}
	keep(t, db, records...)

	var overview AdminOverview
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		overview, err = ReadAdminOverview(t.Context(), tx, site, at, at.Add(time.Hour))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if overview.Calls != 16 || overview.Errors != 1 {
		t.Errorf("totals = %d calls, %d errors; want 16 and 1", overview.Calls, overview.Errors)
	}
	checkOverviewAccounts(t, overview.Accounts, a)
	wantModels := []AdminModelCalls{{Model: "m1", Calls: 15}, {Model: "m2", Calls: 1, Errors: 1}}
	if !slices.Equal(overview.Models, wantModels) {
		t.Errorf("models = %+v, want %+v", overview.Models, wantModels)
	}
}

// checkOverviewAccounts: the busiest account comes first, the rest of the top ten each have one call and no error.
func checkOverviewAccounts(t *testing.T, accounts []AdminAccountCalls, busiest uuid.UUID) {
	t.Helper()
	if len(accounts) != adminTop {
		t.Fatalf("accounts = %d, want %d", len(accounts), adminTop)
	}
	if got := accounts[0]; got.Account != busiest || got.Calls != 3 || got.Errors != 1 {
		t.Errorf("first account = %+v, want %s with 3 calls and 1 error", got, busiest)
	}
	for _, account := range accounts[1:] {
		if account.Calls != 1 || account.Errors != 0 {
			t.Errorf("account %s = %+v, want one call and no error", account.Account, account)
		}
	}
}

// firstOf is the first instant of month in 2026, the year the call log's partitions start: the coverage tests drop a
// month by it and ask for windows around it.
func firstOf(month time.Month) time.Time {
	return time.Date(2026, month, 1, 0, 0, 0, 0, time.UTC)
}

// dropCallMonths drops the partitions of months as retention does once their archive is confirmed: the attempts first,
// then the calls.
func dropCallMonths(t *testing.T, database pgtest.Database, months ...time.Time) {
	t.Helper()
	var statements []string
	for _, month := range months {
		for _, table := range archiveTables {
			statements = append(statements, `DROP TABLE observability."`+table+"_"+month.Format("2006_01")+`"`)
		}
	}
	if err := pgtest.Run(t.Context(), database.Superuser, statements...); err != nil {
		t.Fatal(err)
	}
}

// coverageOf reads the log's coverage.
func coverageOf(t *testing.T, db *pg.DB) Coverage {
	t.Helper()
	var coverage Coverage
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		coverage, err = CallCoverage(t.Context(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return coverage
}

// readWindow reads what the dashboard and the call list return for since..until, and the coverage the list reports.
func readWindow(t *testing.T, db *pg.DB, site uuid.UUID, since, until time.Time) (AdminOverview, []CallSummary, Coverage) {
	t.Helper()
	var overview AdminOverview
	var calls []CallSummary
	var coverage Coverage
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		if overview, err = ReadAdminOverview(t.Context(), tx, site, since, until); err != nil {
			return err
		}
		calls, _, coverage, err = ListAdmin(t.Context(), tx, AdminQuery{Since: since, Until: until, PageSize: 10})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return overview, calls, coverage
}

// coversCase is a window and whether the log still answers it completely.
type coversCase struct {
	what         string
	since, until time.Time
	covers       bool
}

// checkCoverage requires coverage to start at since and to answer each window as its case says.
func checkCoverage(t *testing.T, coverage Coverage, since time.Time, cases []coversCase) {
	t.Helper()
	if !coverage.Known || !coverage.Since.Equal(since) {
		t.Fatalf("coverage = %+v, want known from %s", coverage, since)
	}
	for _, want := range cases {
		if got := coverage.Covers(want.since, want.until); got != want.covers {
			t.Errorf("%s: Covers(%s, %s) = %v, want %v", want.what, want.since, want.until, got, want.covers)
		}
	}
}

// TestCallCoverageStartsAtTheOldestPartition: after a month is archived and dropped, coverage begins at the next month;
// a window that reaches the dropped month is incomplete, and the dashboard and call list count only what they still
// have instead of presenting the archived calls as zero.
func TestCallCoverageStartsAtTheOldestPartition(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	site, account := uuid.NewV7(), uuid.NewV7()
	october := record(account, firstOf(time.October).Add(30*24*time.Hour))
	november := record(account, firstOf(time.November).Add(24*time.Hour))
	october.Site, november.Site = site, site
	keep(t, db, october, november)
	dropCallMonths(t, database, firstOf(time.October))
	wide := firstOf(time.October).Add(14 * 24 * time.Hour)
	until := firstOf(time.November).Add(14 * 24 * time.Hour)
	checkCoverage(t, coverageOf(t, db), firstOf(time.November), []coversCase{
		{"a window reaching the archived month", wide, until, false},
		{"a window from the oldest month", firstOf(time.November), until, true},
	})
	overview, calls, listCoverage := readWindow(t, db, site, wide, until)
	if overview.Calls != 1 || overview.Errors != 1 {
		t.Errorf("overview = %d calls, %d errors, want only November's one call and its error", overview.Calls, overview.Errors)
	}
	if overview.Coverage.Covers(wide, until) || listCoverage.Covers(wide, until) {
		t.Errorf("the dashboard covers %s: %v; the list: %v", wide, overview.Coverage.Covers(wide, until), listCoverage.Covers(wide, until))
	}
	if len(calls) != 1 || calls[0].ID != november.ID {
		t.Errorf("call list = %+v, want only November's call", calls)
	}
}

// TestCallCoverageChecksTheWindowForGaps: retention records a failed month and still drops a newer one, so an older
// partition can remain with a hole after it. Coverage must check every month the window touches, not just the oldest
// partition, or a window that crosses the hole would be presented as complete.
func TestCallCoverageChecksTheWindowForGaps(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	site, account := uuid.NewV7(), uuid.NewV7()
	october := record(account, firstOf(time.October).Add(30*24*time.Hour))
	november := record(account, firstOf(time.November).Add(24*time.Hour))
	december := record(account, firstOf(time.December).Add(24*time.Hour))
	october.Site, november.Site, december.Site = site, site, site
	keep(t, db, october, november, december)
	dropCallMonths(t, database, firstOf(time.November))
	wide := firstOf(time.November).Add(14 * 24 * time.Hour)
	until := firstOf(time.December).Add(14 * 24 * time.Hour)
	checkCoverage(t, coverageOf(t, db), firstOf(time.October), []coversCase{
		{"a window across the gap", wide, until, false},
		{"a window wholly inside the oldest month", firstOf(time.October), firstOf(time.November), true},
		{"a window that reaches the gap", firstOf(time.October), wide, false},
		{"a window wholly inside the dropped month", firstOf(time.November), firstOf(time.December), false},
		{"a window from the newest month", firstOf(time.December), until, true},
	})
	overview, calls, listCoverage := readWindow(t, db, site, wide, until)
	if overview.Calls != 1 || overview.Errors != 1 {
		t.Errorf("overview = %d calls, %d errors, want only December's one call and its error", overview.Calls, overview.Errors)
	}
	if overview.Coverage.Covers(wide, until) || listCoverage.Covers(wide, until) {
		t.Errorf("the dashboard covers %s across the gap: %v; the list: %v", wide, overview.Coverage.Covers(wide, until), listCoverage.Covers(wide, until))
	}
	if len(calls) != 1 || calls[0].ID != december.ID {
		t.Errorf("call list = %+v, want only December's call", calls)
	}
}

// TestCallCoverageWithNoMonthLeft: once every monthly partition is archived, the log answers no window, so the pages
// mark the call part incomplete instead of presenting the empty remainder as the whole log.
func TestCallCoverageWithNoMonthLeft(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	coverage := coverageOf(t, db)
	if !coverage.Known {
		t.Fatal("a freshly migrated log has no monthly partition")
	}
	months := make([]time.Time, 0, len(coverage.months))
	for month := range coverage.months {
		months = append(months, month)
	}
	dropCallMonths(t, database, months...)
	if got := coverageOf(t, db); got.Known || got.Covers(firstOf(time.November), firstOf(time.December)) {
		t.Errorf("coverage = %+v with no monthly partition left", got)
	}
}

// TestAdminOverviewPlansReadTheWindow: over a large log, the three dashboard queries read the window through an index
// instead of scanning the partition, and touch a bounded number of blocks.
func TestAdminOverviewPlansReadTheWindow(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	seedForPlans(t, database, false)
	site := uuid.MustParse("00000000-0000-4000-8000-0000000000ff")
	until := time.Now().UTC()
	since := until.Add(-10 * time.Minute)
	for name, query := range map[string]string{
		"counts": adminOverviewCountsSQL, "accounts": adminTopAccountsSQL, "models": adminTopModelsSQL,
	} {
		checkOverviewPlan(t, db, name, query, site, since, until)
	}
}

// checkOverviewPlan runs one dashboard query with EXPLAIN and requires an index read of the window within its budget.
func checkOverviewPlan(t *testing.T, db *pg.DB, name, query string, site uuid.UUID, since, until time.Time) {
	t.Helper()
	var lines []string
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		if err := planOwnPage(t.Context(), tx); err != nil {
			return err
		}
		args := []any{site, since, until}
		if name != "counts" {
			args = append(args, adminTop)
		}
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			lines = append(lines, line)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	if strings.Contains(plan, "Seq Scan") || !strings.Contains(plan, "Index") {
		t.Errorf("%s does not read the window through an index:\n%s", name, plan)
	}
	if n := buffers(t, plan); n > 1500 {
		t.Errorf("%s touched %d blocks, want at most 1500:\n%s", name, n, plan)
	}
}
