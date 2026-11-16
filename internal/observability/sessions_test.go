package observability

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func sessionPage(t *testing.T, db *pg.DB, scope pg.Scope, q OwnQuery) ([]OwnSession, *Cursor) {
	t.Helper()
	var rows []OwnSession
	var next *Cursor
	if err := db.ReadTx(t.Context(), scope, func(tx pg.Tx) (err error) { rows, next, err = ListOwnSessions(t.Context(), tx, q); return err }); err != nil {
		t.Fatal(err)
	}
	return rows, next
}

func TestOwnSessionsGroupFilterAndRefresh(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	account := uuid.NewV7()
	at := time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC)
	a := record(account, at)
	a.PriceNano = 10
	b, c := a, a
	b.ID, b.At, b.Model, b.PriceNano = uuid.NewV7(), at.Add(time.Second), "other", 20
	c.ID, c.At, c.PriceNano = uuid.NewV7(), at.Add(2*time.Second), 30
	missing, foreign, otherWorkspace := a, a, a
	missing.ID, missing.SessionID = uuid.NewV7(), ""
	foreign.ID, foreign.Account = uuid.NewV7(), uuid.NewV7()
	otherWorkspace.ID, otherWorkspace.Workspace, otherWorkspace.SessionID = uuid.NewV7(), uuid.NewV7(), "elsewhere"
	keep(t, db, a, b, c, missing, foreign, otherWorkspace)
	q := OwnQuery{Account: account, Since: at, Until: at.Add(time.Hour), Workspace: &a.Workspace, PageSize: 1}
	rows, next := sessionPage(t, db, accountScope(account), q)
	if next != nil || len(rows) != 1 {
		t.Fatalf("page: %+v %v", rows, next)
	}
	r := rows[0]
	want := OwnSession{SessionID: a.SessionID, ClientApp: a.ClientApp, Currency: "USD", Calls: "3", PriceNano: "60", MainModel: a.Model, FirstAt: at, LastAt: c.At}
	if diff := cmp.Diff(want, r); diff != "" {
		t.Fatalf("aggregate (-want +got):\n%s", diff)
	}
	d := a
	d.ID, d.At, d.ClientApp = uuid.NewV7(), at.Add(3*time.Second), "codex"
	keep(t, db, d)
	rows, _ = sessionPage(t, db, accountScope(account), q)
	want.Calls, want.PriceNano, want.LastAt, want.ClientApp = "4", "70", d.At, "codex"
	if diff := cmp.Diff(want, rows[0]); diff != "" {
		t.Fatalf("refresh (-want +got):\n%s", diff)
	}

	for _, test := range []struct {
		name   string
		filter OwnQuery
		calls  string
	}{
		{"key", OwnQuery{Key: &a.Key}, "4"},
		{"model", OwnQuery{Model: "other"}, "1"},
		{"app", OwnQuery{ClientApp: "codex"}, "1"},
		{"request", OwnQuery{RequestID: &b.ID}, "1"},
		{"status", OwnQuery{Status: "success"}, "0"},
		{"fallback", OwnQuery{FellBack: true}, "4"},
	} {
		filter := test.filter
		filter.Account, filter.Since, filter.Until, filter.Workspace = account, at, q.Until, &a.Workspace
		got, _ := sessionPage(t, db, accountScope(account), filter)
		calls := "0"
		if len(got) > 0 {
			calls = got[0].Calls
		}
		if len(got) > 1 || calls != test.calls {
			t.Fatalf("%s: %+v", test.name, got)
		}
	}

	got, _ := sessionPage(t, db, accountScope(foreign.Account), q)
	if len(got) != 0 {
		t.Fatalf("tenant leak: %+v", got)
	}
}

func TestOwnSessionsPagesCurrenciesAndTies(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	account := uuid.NewV7()
	at := time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC) // default partition
	a := record(account, at)
	a.Model, a.PriceNano = "z", 9223372036854775807
	b := a
	b.ID, b.Model = uuid.NewV7(), "a"
	eur, other := a, a
	eur.ID, eur.Currency = uuid.NewV7(), mustCurrency("EUR")
	other.ID, other.SessionID = uuid.NewV7(), "another"
	keep(t, db, a, b, eur, other)
	q := OwnQuery{Account: account, Since: at, Until: at.Add(time.Hour), PageSize: 1}
	var all []OwnSession
	for range 4 {
		rows, next := sessionPage(t, db, accountScope(account), q)
		all = append(all, rows...)
		if next == nil {
			break
		}
		q.After = next
	}
	if len(all) != 3 {
		t.Fatalf("pages: %+v", all)
	}
	for _, r := range all {
		if r.SessionID == a.SessionID && r.Currency == "USD" && (r.MainModel != "a" || r.Calls != "2" || r.PriceNano != "18446744073709551614") {
			t.Fatalf("tie and exact sum: %+v", r)
		}
	}
	for _, bad := range []OwnQuery{{Account: account, Since: at, Until: at}, {Account: account, Since: at, Until: at.Add(32 * 24 * time.Hour)}, {Account: account, Since: at, Until: at.Add(time.Hour), PageSize: 201}} {
		if err := db.ReadTx(t.Context(), accountScope(account), func(tx pg.Tx) error { _, _, err := ListOwnSessions(t.Context(), tx, bad); return err }); err == nil {
			t.Fatal("accepted invalid query")
		}
	}
}

func TestOwnSessionsPlanBoundsCallRecords(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	seedForPlans(t, database, false)
	if err := pgtest.Run(t.Context(), database.Superuser, `UPDATE observability.call_log SET session_id='session-'||(mod(abs(hashtext(id::text)::bigint),100))::text`, `VACUUM ANALYZE observability.call_log`); err != nil {
		t.Fatal(err)
	}
	account := uuid.MustParse(heavyAccount)
	var until time.Time
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT max(at) + interval '1 microsecond' FROM observability.call_log").Scan(&until)
	}); err != nil {
		t.Fatal(err)
	}
	var heapBytes, indexBytes int64
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT
 (SELECT sum(pg_relation_size(relid)) FROM pg_partition_tree('observability.call_log') WHERE isleaf),
 (SELECT sum(pg_relation_size(relid)) FROM pg_partition_tree('observability.call_log_account_sessions_at') WHERE isleaf)`).Scan(&heapBytes, &indexBytes)
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("120000 nonempty-session calls: heap=%d bytes, session index=%d bytes (%.1f bytes/call)", heapBytes, indexBytes, float64(indexBytes)/120000)
	for _, span := range []time.Duration{10 * time.Minute, 31 * 24 * time.Hour} {
		q := OwnQuery{Account: account, Since: until.Add(-span), Until: until}
		plan := explainSessions(t, db, q)
		t.Logf("%s window over 120000 calls:\n%s", span, plan)
		if regexp.MustCompile(`Seq Scan on call_log_[^\n]*actual[^\n]*rows=[1-9]`).MatchString(plan) || !strings.Contains(plan, "Index") {
			t.Fatalf("unbounded call read:\n%s", plan)
		}
		if span == 10*time.Minute && buffers(t, plan) > 1500 {
			t.Fatalf("narrow window exceeds 1500-block budget:\n%s", plan)
		}
	}
	checkSessionFilterPlans(t, db, until)
	checkSessionReadBudget(t, db, database, until)
}

func checkSessionFilterPlans(t *testing.T, db *pg.DB, until time.Time) {
	t.Helper()
	account := uuid.MustParse(heavyAccount)
	var requestText string
	if err := db.ReadTx(t.Context(), accountScope(account), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT id::text FROM observability.call_log WHERE account_id=$1 LIMIT 1", account.String()).Scan(&requestText)
	}); err != nil {
		t.Fatal(err)
	}
	request := uuid.MustParse(requestText)
	for _, test := range []struct {
		name string
		q    OwnQuery
		max  int
	}{
		{"model", OwnQuery{Model: "rare"}, 400},
		{"status", OwnQuery{Status: "error"}, 400},
		{"fallback", OwnQuery{FellBack: true}, 400},
		{"request", OwnQuery{RequestID: &request}, 40},
	} {
		q := test.q
		q.Account, q.Since, q.Until = account, until.Add(-31*24*time.Hour), until
		plan := explainSessions(t, db, q)
		t.Logf("%s filter: %d buffers\n%s", test.name, buffers(t, plan), plan)
		if buffers(t, plan) > test.max || regexp.MustCompile(`Seq Scan on call_log_[^\n]*actual[^\n]*rows=[1-9]`).MatchString(plan) {
			t.Errorf("%s exceeds %d-block index budget:\n%s", test.name, test.max, plan)
		}
	}
}

func explainSessions(t *testing.T, db *pg.DB, q OwnQuery) string {
	t.Helper()
	size, err := ownPageSize(q.PageSize)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	if err := db.ReadTx(t.Context(), accountScope(q.Account), func(tx pg.Tx) error {
		if err := planOwnPage(t.Context(), tx); err != nil {
			return err
		}
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+store.SessionsQuery, q.Account.String(), q.Since, q.Until, q.Model, q.ClientApp, uuidFilter(q.Key), uuidFilter(q.Workspace), q.Status, uuidFilter(q.RequestID), q.FellBack, maxOwnSessionCalls, q.Until, uuid.Nil().String(), int64(size)+1)
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

func checkSessionReadBudget(t *testing.T, db *pg.DB, database pgtest.Database, until time.Time) {
	t.Helper()
	if err := pgtest.Run(t.Context(), database.Superuser, `WITH chosen AS (
 SELECT id,at FROM observability.call_log WHERE account_id<>'`+heavyAccount+`' ORDER BY at,id LIMIT 40000
 ) UPDATE observability.call_log c SET account_id='`+heavyAccount+`' FROM chosen WHERE (c.id,c.at)=(chosen.id,chosen.at)`,
		`VACUUM ANALYZE observability.call_log`); err != nil {
		t.Fatal(err)
	}
	q := OwnQuery{Account: uuid.MustParse(heavyAccount), Since: until.Add(-31 * 24 * time.Hour), Until: until, PageSize: 200}
	rows, next := sessionPage(t, db, accountScope(q.Account), q)
	var calls int64
	for _, r := range rows {
		n, err := strconv.ParseInt(r.Calls, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		calls += n
	}
	if calls != 100000 || next != nil {
		t.Fatalf("at budget: calls=%d next=%v", calls, next)
	}
	plan := explainSessions(t, db, q)
	t.Log("100000 matching calls, at budget:\n" + plan)
	if buffers(t, plan) > 6000 || regexp.MustCompile(`Seq Scan on call_log_[^\n]*actual[^\n]*rows=[1-9]`).MatchString(plan) {
		t.Fatalf("maximum window exceeds 6000-block budget:\n%s", plan)
	}
	if err := pgtest.Run(t.Context(), database.Superuser, `UPDATE observability.call_log SET account_id='`+heavyAccount+`' WHERE id=(SELECT id FROM observability.call_log WHERE account_id<>'`+heavyAccount+`' LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	checkSessionSizeRefusal(t, db, q)
	if err := pgtest.Run(t.Context(), database.Superuser, `UPDATE observability.call_log SET account_id='`+heavyAccount+`'`, `VACUUM ANALYZE observability.call_log`); err != nil {
		t.Fatal(err)
	}
	checkSessionSizeRefusal(t, db, q)
	plan = explainSessions(t, db, q)
	t.Log("120000 matching calls, refuse after at most 100001 inputs:\n" + plan)
	if !strings.Contains(plan, "rows=100001.00") && !strings.Contains(plan, "rows=100001 ") {
		t.Fatalf("no bounded input in plan:\n%s", plan)
	}
}

func checkSessionSizeRefusal(t *testing.T, db *pg.DB, q OwnQuery) {
	t.Helper()
	for _, after := range []*Cursor{nil, {At: q.Until.Add(-time.Hour), ID: uuid.NewV7()}} {
		q.After = after
		err := db.ReadTx(t.Context(), accountScope(q.Account), func(tx pg.Tx) error {
			rows, next, err := ListOwnSessions(t.Context(), tx, q)
			if len(rows) != 0 || next != nil {
				t.Errorf("oversized window returned a partial page: %+v %v", rows, next)
			}
			return err
		})
		if !errors.Is(err, ErrSessionSize) {
			t.Fatalf("oversized window: %v", err)
		}
	}
}
