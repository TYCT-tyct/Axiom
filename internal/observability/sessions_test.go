package observability

import (
	"regexp"
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
}

func explainSessions(t *testing.T, db *pg.DB, q OwnQuery) string {
	t.Helper()
	var lines []string
	if err := db.ReadTx(t.Context(), accountScope(q.Account), func(tx pg.Tx) error {
		if err := planOwnQuery(t.Context(), tx); err != nil {
			return err
		}
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+store.SessionsQuery, q.Until, uuid.Nil().String(), 51, q.Account.String(), q.Since, q.Until, "", "", "", "", "", "", false)
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
