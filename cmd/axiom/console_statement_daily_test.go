package main

import (
	"fmt"
	"net/http"
	"testing"

	"axiom/internal/billing"
	"axiom/internal/platform/consoleapi"
)

func TestStatementDaily(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	f.seedStatementHistory(s, 1)
	f.run("UPDATE billing.transactions SET created_at = '2026-11-01T16:00:00Z', seq = NULL")
	for _, tc := range []struct{ query, day string }{
		{"", "2026-11-01"}, {"?time_zone=Asia%2FShanghai", "2026-11-02"}, {"?limit=200", "2026-11-01"},
	} {
		var page consoleapi.StatementDaily
		decode(t, f.do(http.MethodGet, "/statement/daily"+tc.query, "", as(s)), &page)
		if len(page.Data) != 1 || page.Data[0].Day != tc.day || page.Data[0].Gifted.Amount != "1" || page.Data[0].Gifted.Currency != "USD" || page.Data[0].Spent.Amount != "1" || page.Data[0].ClosingBalance != nil || page.NextCursor != nil {
			t.Fatalf("daily page = %+v", page)
		}
	}
	for _, query := range []string{"?time_zone=not-a-zone", "?time_zone=Local", "?time_zone=", "?cursor=nonsense", "?limit=201", "?limit=0"} {
		expect(t, query, f.do(http.MethodGet, "/statement/daily"+query, "", as(s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	}
}

func TestStatementDailyBudget(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	account, site := f.account(s)
	f.run(fmt.Sprintf("INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id) SELECT uuidv7(), '%s', '%s', 'settle', uuidv7() FROM generate_series(1, %d)", site, account, billing.MaxStatementTransactions+1))
	expect(t, "daily history budget", f.do(http.MethodGet, "/statement/daily", "", as(s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	var statement consoleapi.Statement
	decode(t, f.do(http.MethodGet, "/statement?limit=1", "", as(s)), &statement)
}
