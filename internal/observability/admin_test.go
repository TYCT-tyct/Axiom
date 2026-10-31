package observability

import (
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestAdminCallsFilterAndPage(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	account := uuid.NewV7()
	at := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	a, b, c := record(account, at), record(account, at), record(uuid.NewV7(), at.Add(time.Second))
	b.Model, b.ErrorKind = "other", ""
	keep(t, db, a, b, c)
	for _, tc := range []struct {
		name             string
		account, request *uuid.UUID
		model, status    string
		scope            pg.Scope
		want             []uuid.UUID
	}{
		{"all", nil, nil, "", "", platformScope, []uuid.UUID{c.ID, b.ID, a.ID}},
		{"account", &account, nil, "", "", platformScope, []uuid.UUID{b.ID, a.ID}},
		{"request", nil, &b.ID, "", "", platformScope, []uuid.UUID{b.ID}},
		{"mismatch", &account, &c.ID, "", "", platformScope, nil},
		{"model", nil, nil, "other", "", platformScope, []uuid.UUID{b.ID}},
		{"ok", nil, nil, "", "ok", platformScope, []uuid.UUID{b.ID}},
		{"error", nil, nil, "", "error", platformScope, []uuid.UUID{c.ID, a.ID}},
		{"model that did not call", nil, nil, "absent", "", platformScope, nil},
		{"model and the other result", nil, nil, "other", "error", platformScope, nil},
		{"RLS", nil, nil, "", "", accountScope(account), []uuid.UUID{b.ID, a.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := AdminQuery{Since: at, Until: at.Add(2 * time.Second), Account: tc.account, RequestID: tc.request,
				Model: tc.model, Status: tc.status, PageSize: 1}
			got := adminPageIDs(t, db, tc.scope, q)

			if !slices.Equal(tc.want, got) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAdminCallsPlansUseIndexOrder(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	seedForPlans(t, database, false)
	since, until := window()
	for _, account := range []string{"", heavyAccount, lightAccount} {
		lines := adminPlanLines(t, db, since, until, account)

		plan := strings.Join(lines, "\n")
		if strings.Contains(plan, "Sort") || strings.Contains(plan, "Bitmap") || strings.Contains(plan, "Seq Scan") || !strings.Contains(plan, "Index Scan") {
			t.Fatalf("account %q:\n%s", account, plan)
		}
		if n := buffers(t, plan); n > 800 {
			t.Fatalf("account %q reads %d buffers:\n%s", account, n, plan)
		}
	}
}

func adminPageIDs(t *testing.T, db *pg.DB, scope pg.Scope, q AdminQuery) []uuid.UUID {
	t.Helper()
	var got []uuid.UUID
	for {
		var next *Cursor
		if err := db.ReadTx(t.Context(), scope, func(tx pg.Tx) (err error) {
			var rows []CallSummary
			rows, next, err = ListAdmin(t.Context(), tx, q)
			for _, r := range rows {
				got = append(got, r.ID)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if next == nil {
			return got
		}
		q.After = next
	}
}

func adminPlanLines(t *testing.T, db *pg.DB, since, until time.Time, account string) []string {
	t.Helper()
	var lines []string
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		if err := planOwnPage(t.Context(), tx); err != nil {
			return err
		}
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+adminListSQL(account != ""), until, uuid.Nil().String(), since, until, account, "", "", "", 51)
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
	return lines
}
