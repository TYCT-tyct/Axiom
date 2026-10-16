package observability

import (
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

const (
	heavyAccount = "00000000-0000-4000-8000-000000000001" // 60,000 calls in the window
	lightAccount = "00000000-0000-4000-8000-000000000007" // 600 calls, among 100 such accounts
	pageSize     = 50
)

// seedForPlans fills the table as the superuser: one heavy account and 100 light ones, 120,000 calls over eight hours.
func seedForPlans(t *testing.T, database pgtest.Database) {
	t.Helper()
	if err := pgtest.Run(t.Context(), database.Superuser, `INSERT INTO observability.call_log (id, at, site_id, account_id,
			workspace_id, key_id, ip, request_class, used_tools, has_image, has_file, model, endpoint_id, attempt_count,
			usage, service_tier, price_nano, cost_nano, currency, free_of_charge, latency_ms, first_token_ms,
			consent_version, training_allowed)
		SELECT uuidv7(), now() - make_interval(secs => n / 4.0), '00000000-0000-4000-8000-0000000000ff',
			CASE WHEN n % 2 = 0 THEN '`+heavyAccount+`'::uuid
				ELSE ('00000000-0000-4000-8000-' || lpad((n % 200 + 2)::text, 12, '0'))::uuid END,
			uuidv7(), uuidv7(), '203.0.113.7', 'chat', false, false, false, 'm', uuidv7(), 1, '{}', 'standard', 1, 1,
			'USD', false, 1, 0, 'v', true
		FROM generate_series(1, 120000) n`, "ANALYZE observability.call_log"); err != nil {
		t.Fatal(err)
	}
}

// explainList runs EXPLAIN (ANALYZE, BUFFERS) on the text of the list query itself (store.ListQuery) with the
// parameters ListOwn would give it, as the application role in the account's scope, row-level security included.
func explainList(t *testing.T, db *pg.DB, account uuid.UUID, model string, after *Cursor) string {
	t.Helper()
	q := OwnQuery{Account: account, Since: time.Now().Add(-48 * time.Hour), Until: time.Now().Add(time.Hour), Model: model, After: after}
	p := ownParams(q, pageSize)
	var lines []string
	if err := db.Tx(t.Context(), accountScope(account), func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+store.ListQuery,
			p.AccountID, p.Since, p.Until, p.Model, p.ClientApp, p.AfterAt, p.AfterID, p.PageSize)
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
	return strings.Join(lines, "\n")
}

// firstPageCursor is the cursor ListOwn returns after a page of the account's calls, for the plan of a later page.
func firstPageCursor(t *testing.T, db *pg.DB, account uuid.UUID) *Cursor {
	t.Helper()
	var next *Cursor
	if err := db.ReadTx(t.Context(), accountScope(account), func(tx pg.Tx) (err error) {
		_, next, err = ListOwn(t.Context(), tx, OwnQuery{Account: account, Since: time.Now().Add(-48 * time.Hour), Until: time.Now().Add(time.Hour), PageSize: pageSize})
		return err
	}); err != nil || next == nil {
		t.Fatalf("no second page to explain: %v", err)
	}
	return next
}

// TestListPlansFollowTheAccountIndex explains the list query as it runs, for the first page, a later page and a
// filtered page, of a heavy and of a light account among many. What the plans show, and what is asserted:
//
//   - every plan reads the account's index range (Index Cond on account_id) and never scans the table;
//   - for the heavy account (60,000 calls in the window) the plan is an ordered index scan under the limit, with no sort:
//     it reads about one page, so the cost of a page does not grow with the account's volume. This needs the order to be
//     the index's, which the query's ORDER BY says with qualified columns: a bare "id" is the output column id::text, which
//     sorts as text and turns the plan into a bitmap scan of the whole range and a sort of it;
//   - for the light account (600 calls) the planner prefers a bitmap scan of the account's range and a top-N sort. That
//     reads all 600 rows, and the cost is bounded by the account's calls in the window: the planner estimates the row-level
//     security filter to keep about 1% of the rows, so it takes the sort for a few rows, and it changes to the ordered scan
//     as the account grows. No further index is needed: the ordered scan is on the one that exists.
func TestListPlansFollowTheAccountIndex(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	seedForPlans(t, database)
	heavy, light := uuid.MustParse(heavyAccount), uuid.MustParse(lightAccount)
	cases := []struct {
		name    string
		account uuid.UUID
		model   string
		page    func() *Cursor
		ordered bool // an ordered index scan under the limit, with no sort
	}{
		{"heavy, first page", heavy, "", func() *Cursor { return nil }, true},
		{"heavy, later page", heavy, "", func() *Cursor { return firstPageCursor(t, db, heavy) }, true},
		{"heavy, filtered by model", heavy, "m", func() *Cursor { return nil }, true},
		{"light, first page", light, "", func() *Cursor { return nil }, false},
		{"light, later page", light, "", func() *Cursor { return firstPageCursor(t, db, light) }, false},
	}
	for _, c := range cases {
		plan := explainList(t, db, c.account, c.model, c.page())
		t.Logf("%s\n%s", c.name, plan)
		if !strings.Contains(plan, "Index Cond: ((account_id") || strings.Contains(plan, "Seq Scan") {
			t.Errorf("%s: not read through the account index:\n%s", c.name, plan)
		}
		if c.ordered && (strings.Contains(plan, "Sort") || !strings.Contains(plan, "Index Scan using")) {
			t.Errorf("%s: not an ordered index scan under the limit:\n%s", c.name, plan)
		}
	}
}
