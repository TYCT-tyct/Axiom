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

// TestCallCoverageStartsAtTheOldestPartition: after a month is archived and dropped, coverage begins at the next month;
// a window that reaches the dropped month is incomplete, and the dashboard and call list count only what they still
// have instead of presenting the archived calls as zero.
func TestCallCoverageStartsAtTheOldestPartition(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	site, account := uuid.NewV7(), uuid.NewV7()
	october := record(account, time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC))
	november := record(account, time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC))
	october.Site, november.Site = site, site
	keep(t, db, october, november)
	if err := pgtest.Run(t.Context(), database.Superuser,
		"DROP TABLE observability.call_log_2026_10",
		"DROP TABLE observability.call_attempt_2026_10"); err != nil {
		t.Fatal(err)
	}
	wide := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	firstOfNovember := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var coverage Coverage
	var overview AdminOverview
	var calls []CallSummary
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		if coverage, err = CallCoverage(t.Context(), tx); err != nil {
			return err
		}
		if overview, err = ReadAdminOverview(t.Context(), tx, site, wide, until); err != nil {
			return err
		}
		calls, _, _, err = ListAdmin(t.Context(), tx, AdminQuery{Since: wide, Until: until, PageSize: 10})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !coverage.Known || !coverage.Since.Equal(firstOfNovember) {
		t.Fatalf("coverage = %+v, want from %s", coverage, firstOfNovember)
	}
	if coverage.Covers(wide, until) || !coverage.Covers(firstOfNovember, until) {
		t.Errorf("covers %s: %v; covers the boundary: %v", wide, coverage.Covers(wide, until), coverage.Covers(firstOfNovember, until))
	}
	if overview.Calls != 1 || overview.Errors != 1 {
		t.Errorf("overview = %d calls, %d errors, want only November's one call and its error", overview.Calls, overview.Errors)
	}
	if overview.Coverage.Covers(wide, until) {
		t.Errorf("the overview covers %s, before the oldest partition", wide)
	}
	if len(calls) != 1 || calls[0].ID != november.ID {
		t.Errorf("call list = %+v, want only November's call", calls)
	}

	var names []string
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT c.relname FROM pg_partition_tree('observability.call_log'::regclass) t
 JOIN pg_class c ON c.oid = t.relid WHERE t.isleaf AND c.relname LIKE 'call_log_20%'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			names = append(names, `DROP TABLE observability."`+name+`"`)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if err := pgtest.Run(t.Context(), database.Superuser, names...); err != nil {
		t.Fatal(err)
	}
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		coverage, err = CallCoverage(t.Context(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if coverage.Known || coverage.Covers(wide, until) {
		t.Errorf("with no monthly partition left, coverage = %+v and covers %s", coverage, wide)
	}
}

// TestCallCoverageChecksTheWindowForGaps: retention records a failed month and still drops a newer one, so an older
// partition can remain with a hole after it. Coverage must check every month the window touches, not just the oldest
// partition, or a window that crosses the hole would be presented as complete.
func TestCallCoverageChecksTheWindowForGaps(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	site, account := uuid.NewV7(), uuid.NewV7()
	october := record(account, time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC))
	november := record(account, time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC))
	december := record(account, time.Date(2026, 12, 2, 1, 0, 0, 0, time.UTC))
	october.Site, november.Site, december.Site = site, site, site
	keep(t, db, october, november, december)
	if err := pgtest.Run(t.Context(), database.Superuser,
		"DROP TABLE observability.call_log_2026_11",
		"DROP TABLE observability.call_attempt_2026_11"); err != nil {
		t.Fatal(err)
	}
	wide := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)
	firstOfDecember := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	var coverage Coverage
	var overview AdminOverview
	var calls []CallSummary
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		if coverage, err = CallCoverage(t.Context(), tx); err != nil {
			return err
		}
		if overview, err = ReadAdminOverview(t.Context(), tx, site, wide, until); err != nil {
			return err
		}
		calls, _, _, err = ListAdmin(t.Context(), tx, AdminQuery{Since: wide, Until: until, PageSize: 10})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	firstOfOctober := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	firstOfNovember := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if !coverage.Known || !coverage.Since.Equal(firstOfOctober) {
		t.Fatalf("coverage = %+v, want from %s, the oldest partition still present", coverage, firstOfOctober)
	}
	if coverage.Covers(wide, until) || !coverage.Covers(firstOfDecember, until) {
		t.Errorf("covers %s: %v; covers December: %v", wide, coverage.Covers(wide, until), coverage.Covers(firstOfDecember, until))
	}
	// October is still on disk and a window that stays inside it is complete; one that reaches the November gap is not,
	// even though the oldest partition is still October.
	if !coverage.Covers(firstOfOctober, firstOfNovember) {
		t.Error("a window wholly inside October is not covered")
	}
	if coverage.Covers(firstOfOctober, wide) {
		t.Error("coverage claims a window that crosses the gap is complete")
	}
	if overview.Calls != 1 || overview.Errors != 1 {
		t.Errorf("overview = %d calls, %d errors, want only December's one call and its error", overview.Calls, overview.Errors)
	}
	if overview.Coverage.Covers(wide, until) {
		t.Errorf("the overview covers %s, across the gap", wide)
	}
	if len(calls) != 1 || calls[0].ID != december.ID {
		t.Errorf("call list = %+v, want only December's call", calls)
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
