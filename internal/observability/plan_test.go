package observability

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

const (
	heavyAccount = "00000000-0000-4000-8000-000000000001" // half the calls; 60,000 in the 120,000-row fixture
	lightAccount = "00000000-0000-4000-8000-000000000007" // one of 100 light accounts; 600 calls in the fixture
	pageSize     = 50
)

// seedForPlans fills the table as the superuser: one heavy account and 100 light ones over eight hours.
// Every 1200th call is of the model "rare" and from the application "codex", all of them the heavy account's; the others are of "m" and "cli" (a dense one); "absent" matches nothing.
func seedForPlans(t *testing.T, database pgtest.Database) {
	t.Helper()
	if err := pgtest.Run(t.Context(), database.Superuser, `INSERT INTO observability.call_log (id, at, site_id, account_id,
			workspace_id, key_id, ip, client_app, request_class, used_tools, has_image, has_file, model, endpoint_id,
			attempt_count, usage, service_tier, price_nano, cost_nano, currency, free_of_charge, latency_ms, first_token_ms,
			consent_version, training_allowed)
		SELECT uuidv7(), now() - make_interval(secs => n / 4.0), '00000000-0000-4000-8000-0000000000ff',
			CASE WHEN n % 2 = 0 THEN '`+heavyAccount+`'::uuid
				ELSE ('00000000-0000-4000-8000-' || lpad((n % 200 + 2)::text, 12, '0'))::uuid END,
			uuidv7(), uuidv7(), '203.0.113.7', CASE WHEN n % 1200 = 0 THEN 'codex' ELSE 'cli' END, 'chat', false, false,
			false, CASE WHEN n % 1200 = 0 THEN 'rare' ELSE 'm' END, uuidv7(), 1, '{}', 'standard', 1, 1,
			'USD', false, 1, 0, 'v', true
		FROM generate_series(1, 120000) n`, "ANALYZE observability.call_log"); err != nil {
		t.Fatal(err)
	}
}

// window is the time the plan tests ask about: every call seeded.
func window() (since, until time.Time) {
	return time.Now().Add(-48 * time.Hour), time.Now().Add(time.Hour)
}

// listCall is the text of the list query listRows runs for q, and its arguments in the order of the text's $n.
func listCall(q OwnQuery, size int) (string, []any) {
	at, id := cursorOf(q)
	account := q.Account.String()
	switch {
	case q.Model != "":
		return store.ListByModelQuery, []any{account, q.Model, at, id, q.Since, q.Until, q.ClientApp, size + 1}
	case q.ClientApp != "":
		return store.ListByAppQuery, []any{account, q.ClientApp, at, id, q.Since, q.Until, size + 1}
	}
	return store.ListQuery, []any{account, at, id, q.Since, q.Until, size + 1}
}

// explain runs EXPLAIN (ANALYZE, BUFFERS) on the list query of q, as the application role in the account's scope with
// row-level security, planned for its values as ListOwn has it planned.
func explain(t *testing.T, db *pg.DB, q OwnQuery, scope pg.Scope) string {
	t.Helper()
	size := q.PageSize
	if size == 0 {
		size = pageSize
	}
	sql, args := listCall(q, size)
	var lines []string
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), "SET LOCAL plan_cache_mode = force_custom_plan"); err != nil {
			return err
		}
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
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

var (
	topBuffers  = regexp.MustCompile(`Buffers: shared((?: \w+=\d+)+)`)
	bufferCount = regexp.MustCompile(` (hit|read)=(\d+)`)
)

// buffers is the buffers the plan touched, from cache or disk: the first Buffers line is the top node's, which counts
// everything below.
func buffers(t *testing.T, plan string) int {
	t.Helper()
	m := topBuffers.FindStringSubmatch(plan)
	if m == nil {
		t.Fatalf("no buffers in the plan:\n%s", plan)
	}
	n := 0
	for _, c := range bufferCount.FindAllStringSubmatch(m[1], -1) {
		k, err := strconv.Atoi(c[2])
		if err != nil {
			t.Fatal(err)
		}
		n += k
	}
	return n
}

func TestBuffersCountsCacheAndDisk(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]int{
		"Buffers: shared hit=3":                                    3,
		"Buffers: shared hit=3 read=7000":                          7003,
		"Buffers: shared read=7000":                                7000,
		"Buffers: shared hit=3 read=4 dirtied=9":                   7,
		"Limit\n  Buffers: shared hit=1\n  Buffers: shared hit=99": 1,
	} {
		if got := buffers(t, line); got != want {
			t.Errorf("buffers(%q) = %d, want %d", line, got, want)
		}
	}
}

// nextCursor is the cursor ListOwn returns after the first page of q, for the plan of a later page.
func nextCursor(t *testing.T, db *pg.DB, q OwnQuery) *Cursor {
	t.Helper()
	q.PageSize = pageSize
	var next *Cursor
	if err := db.ReadTx(t.Context(), accountScope(q.Account), func(tx pg.Tx) (err error) {
		_, next, err = ListOwn(t.Context(), tx, q)
		return err
	}); err != nil || next == nil {
		t.Fatalf("no second page to explain: %v", err)
	}
	return next
}

// TestListPlansFollowTheIndexes explains the three list queries as they run, over a heavy and a light account among
// many, for the first page, a later page, a dense filter, a sparse one and one that matches nothing. What the plans show, and what is asserted:
//
//   - no plan scans the table or the partition, and each reads the index range of the account (Index Cond on
//     account_id);
//   - unfiltered, the heavy account's page is an ordered index scan under the limit with no sort: it reads about one
//     page for the first and immediately following pages, as well as deep pages in the same fixture. This needs the order
//     to be the index's, which the query's ORDER BY says
//     with qualified columns: a bare "id" is the output column id::text, which sorts as text and turns the plan into a
//     bitmap scan of the whole range and a sort of it;
//   - filtered by model or by application, a page is read from the filter's own index (account_id, model|client_app, at
//     DESC, id DESC), so the reads follow the matches: a dense filter reads about a page, a sparse one reads its few
//     matches (100 of 60,000), one that matches nothing reads an index page. Without these two indexes the sparse and the
//     empty filter read the whole month's partition (7,059 buffers at 360,000 rows, growing with the table) and took
//     35 to 100 ms;
//   - for the light account (600 calls), an ordered scan or a bitmap scan of its range and a top-N sort are accepted:
//     even the latter is bounded by the account's 600 calls in the window.
func TestListPlansFollowTheIndexes(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	seedForPlans(t, database)
	heavy, light := uuid.MustParse(heavyAccount), uuid.MustParse(lightAccount)
	since, until := window()
	base := func(account uuid.UUID) OwnQuery { return OwnQuery{Account: account, Since: since, Until: until} }
	with := func(q OwnQuery, model, app string) OwnQuery { q.Model, q.ClientApp = model, app; return q }
	cases := []struct {
		name    string
		q       OwnQuery
		later   bool // the page after the first
		ordered bool // an ordered index scan under the limit, with no sort
		max     int  // the most buffers it may touch
	}{
		{"heavy, first page", base(heavy), false, true, 40},
		{"heavy, later page", base(heavy), true, true, 40},
		{"light, first page", base(light), false, false, 800},
		{"light, later page", base(light), true, false, 800},
		{"model, dense", with(base(heavy), "m", ""), false, true, 40},
		{"model, dense, later page", with(base(heavy), "m", ""), true, true, 40},
		{"model, sparse", with(base(heavy), "rare", ""), false, false, 400},
		{"model, sparse, later page", with(base(heavy), "rare", ""), true, false, 400},
		{"model, no match", with(base(heavy), "absent", ""), false, false, 20},
		{"app, dense", with(base(heavy), "", "cli"), false, true, 40},
		{"app, sparse", with(base(heavy), "", "codex"), false, false, 400},
		{"app, no match", with(base(heavy), "", "absent"), false, false, 20},
		{"model and app, sparse", with(base(heavy), "rare", "codex"), false, false, 400},
		{"model sparse, app no match", with(base(heavy), "rare", "absent"), false, false, 400},
	}
	for _, c := range cases {
		q := c.q
		if c.later {
			q.After = nextCursor(t, db, c.q)
		}
		plan := explain(t, db, q, accountScope(q.Account))
		t.Logf("%s: %d buffers\n%s", c.name, buffers(t, plan), plan)
		if !strings.Contains(plan, "Index Cond: ((account_id") || strings.Contains(plan, "Seq Scan") {
			t.Errorf("%s: not read through an index of the account:\n%s", c.name, plan)
		}
		if c.ordered && (strings.Contains(plan, "Sort") || !strings.Contains(plan, "Index Scan using")) {
			t.Errorf("%s: not an ordered index scan under the limit:\n%s", c.name, plan)
		}
		if n := buffers(t, plan); n > c.max {
			t.Errorf("%s: touched %d buffers, want at most %d:\n%s", c.name, n, c.max, plan)
		}
	}
	checkListDeepPagesReadInIndexOrder(t, db)
}

// TestFilteredListsReturnTheMatches: the three list queries return what they should, also when the match is sparse or
// absent: a filtered list holds only the calls of the filter, newest first, in pages that join without gap or repeat;
// no match is an empty page; a model and an application together narrow further.
func TestFilteredListsReturnTheMatches(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	seedForPlans(t, database)
	heavy := uuid.MustParse(heavyAccount)
	for name, c := range map[string]struct {
		q    OwnQuery
		want int
		call func(OwnCall) bool
	}{
		"sparse model":                  {OwnQuery{Account: heavy, Model: "rare"}, 100, func(c OwnCall) bool { return c.Model == "rare" }},
		"sparse application":            {OwnQuery{Account: heavy, ClientApp: "codex"}, 100, func(c OwnCall) bool { return c.ClientApp == "codex" }},
		"model and application":         {OwnQuery{Account: heavy, Model: "rare", ClientApp: "codex"}, 100, func(c OwnCall) bool { return c.Model == "rare" && c.ClientApp == "codex" }},
		"model, other application":      {OwnQuery{Account: heavy, Model: "rare", ClientApp: "cli"}, 0, nil},
		"no such model":                 {OwnQuery{Account: heavy, Model: "absent"}, 0, nil},
		"no such application":           {OwnQuery{Account: heavy, ClientApp: "absent"}, 0, nil},
		"dense model, the whole window": {OwnQuery{Account: heavy, Model: "m"}, 60000 - 100, func(c OwnCall) bool { return c.Model == "m" }},
	} {
		got := allPages(t, db, c.q)
		if len(got) != c.want {
			t.Errorf("%s: %d calls, want %d", name, len(got), c.want)
			continue
		}
		for i, call := range got {
			if !c.call(call) || (i > 0 && call.At.After(got[i-1].At)) {
				t.Errorf("%s: call %d is %+v, not a match in newest-first order", name, i, call)
				break
			}
		}
	}
}

// TestListIsPlannedForItsValues: a connection that plans every statement generically, as the server may after a few
// executions of a prepared one, still reads a page of the list from the index: ListOwn asks for a plan made for the
// values. The tuples the transaction fetched from the call log are counted by its own statistics: about a page, not the
// 60,000 calls of the account in the window that the generic plan reads.
func TestListIsPlannedForItsValues(t *testing.T) {
	t.Parallel()
	_, database := openDBWithURL(t)
	seedForPlans(t, database)
	generic, err := pg.Open(t.Context(), database.App+"&plan_cache_mode=force_generic_plan")
	if err != nil {
		t.Fatal(err)
	}
	defer generic.Close()
	heavy := uuid.MustParse(heavyAccount)
	since, until := window()
	for name, c := range map[string]struct {
		q   OwnQuery
		max int
	}{
		// A generic plan fetches every one of the account's 60,000 calls in the window. A plan made for the values fetches
		// about a page, or for a sparse filter its 100 matches, but which page-bounded plan the planner picks depends on
		// statistics ANALYZE samples at random, and the counts move between runs (150 to 202 seen unfiltered). The bound
		// only has to tell the two apart, so it is twenty pages: far from 60,000, and no sampled plan reaches it.
		"unfiltered":         {OwnQuery{Account: heavy}, 20 * pageSize},
		"dense model":        {OwnQuery{Account: heavy, Model: "m"}, 20 * pageSize},
		"sparse application": {OwnQuery{Account: heavy, ClientApp: "codex"}, 20 * pageSize},
	} {
		q := c.q
		q.Since, q.Until, q.PageSize = since, until, pageSize
		var fetched int
		if err := generic.ReadTx(t.Context(), accountScope(heavy), func(tx pg.Tx) error {
			if _, _, err := ListOwn(t.Context(), tx, q); err != nil {
				return err
			}
			return tx.QueryRow(t.Context(), `SELECT coalesce(sum(coalesce(idx_tup_fetch, 0) + seq_tup_read), 0)::int
				FROM pg_stat_xact_user_tables WHERE relname LIKE 'call_log\_20%' OR relname = 'call_log_default'`).Scan(&fetched)
		}); err != nil {
			t.Fatal(err)
		}
		if fetched > c.max {
			t.Errorf("%s: %d call-log tuples fetched (at most %d) on a connection that plans generically", name, fetched, c.max)
		}
	}
}

// allPages reads every page of q, as the account, and returns the calls in the order they came.
func allPages(t *testing.T, db *pg.DB, q OwnQuery) (calls []OwnCall) {
	t.Helper()
	if q.Since.IsZero() && q.Until.IsZero() {
		q.Since, q.Until = window()
	}
	if q.PageSize == 0 {
		q.PageSize = MaxPageSize
	}
	for {
		var page []OwnCall
		var next *Cursor
		if err := db.ReadTx(t.Context(), accountScope(q.Account), func(tx pg.Tx) (err error) {
			page, next, err = ListOwn(t.Context(), tx, q)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, page...)
		if next == nil {
			return calls
		}
		if q.After != nil && (next.At.After(q.After.At) || (next.At.Equal(q.After.At) && next.ID.String() >= q.After.ID.String())) {
			t.Fatalf("page cursor did not advance: %v -> %v", q.After, next)
		}
		q.After = next
	}
}
