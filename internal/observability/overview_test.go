package observability

import (
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
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
	if len(overview.Accounts) != adminTop {
		t.Fatalf("accounts = %d, want %d", len(overview.Accounts), adminTop)
	}
	if overview.Accounts[0].Account != a || overview.Accounts[0].Calls != 3 || overview.Accounts[0].Errors != 1 {
		t.Errorf("first account = %+v, want %s with 3 calls and 1 error", overview.Accounts[0], a)
	}
	for _, account := range overview.Accounts[1:] {
		if account.Calls != 1 || account.Errors != 0 {
			t.Errorf("account %s = %+v, want one call and no error", account.Account, account)
		}
	}
	wantModels := []AdminModelCalls{{Model: "m1", Calls: 15}, {Model: "m2", Calls: 1, Errors: 1}}
	if len(overview.Models) != len(wantModels) {
		t.Fatalf("models = %+v, want %+v", overview.Models, wantModels)
	}
	for i, want := range wantModels {
		if overview.Models[i] != want {
			t.Errorf("model %d = %+v, want %+v", i, overview.Models[i], want)
		}
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
}
