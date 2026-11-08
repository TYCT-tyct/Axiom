package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
)

func (f consoleFixture) budget(s session, method, workspace, body string) consoleapi.WorkspaceBudget {
	f.t.Helper()
	rec := f.do(method, "/workspaces/"+workspace+"/budget", body, as(s))
	expect(f.t, method+" budget "+body, rec, http.StatusOK, "")
	var b consoleapi.WorkspaceBudget
	decode(f.t, rec, &b)
	return b
}

func dollars(amount string) *consoleapi.Money {
	return &consoleapi.Money{Amount: amount, Currency: "USD"}
}

// TestWorkspaceBudget: a workspace starts without a budget; one set reads back with what the window has used, spent
// and in flight, counted over the days its reset covers; a change of nothing is not audited, the others are, in a key
// limit's shape; an empty body removes the budget.
func TestWorkspaceBudget(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	prod := f.workspace(s, "Prod").Id
	id, err := accounts.ParseWorkspaceID(prod)
	if err != nil {
		t.Fatal(err)
	}
	account, site := f.account(s)
	// 1200 charged and 300 in flight on the database's day and on the next, so a daily window holds 1500 whether or not
	// midnight UTC passes before the reads (the node reads the database's clock, which a test cannot stop); 40 days ago
	// 1000, which only a budget that never resets counts, with both days: 4000.
	f.run(fmt.Sprintf(`INSERT INTO billing.spend (subject, window_start, site_id, account_id, spent, reserved) SELECT
		'%[1]s', date_trunc('day', now(), 'UTC') + d * interval '1 day', '%[2]s', '%[3]s', CASE d WHEN -40 THEN 1000 ELSE 1200 END,
		CASE d WHEN -40 THEN 0 ELSE 300 END FROM unnest(ARRAY[0, 1, -40]) AS d`, uuid.UUID(id), site, account))
	if diff := cmp.Diff(consoleapi.WorkspaceBudget{}, f.budget(s, http.MethodGet, prod, "")); diff != "" {
		t.Errorf("a new workspace's budget (-want +got):\n%s", diff)
	}
	daily := new(consoleapi.LimitReset("daily"))
	started := time.Now()
	for _, step := range []struct {
		method, body string
		want         consoleapi.WorkspaceBudget
	}{
		{
			http.MethodPut, `{"limit":{"amount":"1000","currency":"USD"},"limit_reset":"daily"}`,
			consoleapi.WorkspaceBudget{Limit: dollars("1000"), LimitReset: daily, Used: dollars("1500"), LimitRemaining: dollars("0")},
		},
		{
			http.MethodPut, `{"limit":{"amount":"1000","currency":"USD"},"limit_reset":"daily"}`,
			consoleapi.WorkspaceBudget{Limit: dollars("1000"), LimitReset: daily, Used: dollars("1500"), LimitRemaining: dollars("0")},
		},
		{http.MethodGet, "", consoleapi.WorkspaceBudget{Limit: dollars("1000"), LimitReset: daily, Used: dollars("1500"), LimitRemaining: dollars("0")}},
		{
			http.MethodPut, `{"limit":{"amount":"5000","currency":"USD"}}`,
			consoleapi.WorkspaceBudget{Limit: dollars("5000"), Used: dollars("4000"), LimitRemaining: dollars("1000")},
		},
		{http.MethodPut, `{}`, consoleapi.WorkspaceBudget{}},
		{http.MethodGet, "", consoleapi.WorkspaceBudget{}},
	} {
		got := f.budget(s, step.method, prod, step.body)
		// A daily window ends at the UTC midnight after the read, whichever day that was.
		if at := got.ResetsAt; (at != nil) != (step.want.LimitReset != nil) ||
			at != nil && (!at.Equal(at.UTC().Truncate(24*time.Hour)) || !at.After(started) || at.After(time.Now().Add(24*time.Hour))) {
			t.Errorf("%s %s: resets at %v", step.method, step.body, at)
		}
		got.ResetsAt = nil
		if diff := cmp.Diff(step.want, got); diff != "" {
			t.Errorf("%s %s (-want +got):\n%s", step.method, step.body, diff)
		}
	}
	audited := f.text(`SELECT string_agg(detail::text, '; ' ORDER BY id) FROM platform.audit_log
		WHERE action = 'workspace.update' AND target_id = '` + prod + `'`)
	want := strings.Join([]string{
		`{"after": {"budget": 1000, "budget_reset": "daily"}, "before": {"budget": null, "budget_reset": null}, "changed": ["budget", "budget_reset"]}`,
		`{"after": {"budget": 5000, "budget_reset": null}, "before": {"budget": 1000, "budget_reset": "daily"}, "changed": ["budget", "budget_reset"]}`,
		`{"after": {"budget": null}, "before": {"budget": 5000}, "changed": ["budget"]}`,
	}, "; ")
	if audited != want {
		t.Errorf("audited\n%s\nwant\n%s", audited, want)
	}
	// What admission reads: the row the key's call joins, so every node applies the budget on the next call.
	f.budget(s, http.MethodPut, prod, `{"limit":{"amount":"0","currency":"USD"},"limit_reset":"monthly"}`)
	if got := f.text(`SELECT budget || ' ' || budget_reset FROM accounts.workspaces WHERE id = '` + uuid.UUID(id).String() + `'`); got != "0 monthly" {
		t.Errorf("the budget admission reads: %q", got)
	}
}

// TestWorkspaceBudgetRefusals: a reset without a limit, another currency, a negative amount, another account's workspace
// and a member's change are refused and not audited; a member reads the budget, and the default workspace takes one.
func TestWorkspaceBudgetRefusals(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	main, prod := f.listWorkspaces(s)[0].Id, f.workspace(s, "Prod").Id
	theirs := f.listWorkspaces(f.registered(s, "other@example.com"))[0].Id
	for _, r := range []struct {
		what, method, workspace, body string
		status                        int
		code                          consoleapi.ErrorCode
	}{
		{"a reset without a limit", http.MethodPut, prod, `{"limit_reset":"daily"}`, http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest},
		{"another currency", http.MethodPut, prod, `{"limit":{"amount":"1","currency":"EUR"}}`, http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest},
		{"a negative amount", http.MethodPut, prod, `{"limit":{"amount":"-1","currency":"USD"}}`, http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest},
		{"reading another account's", http.MethodGet, theirs, "", http.StatusNotFound, consoleapi.ErrorCodeNotFound},
		{"setting another account's", http.MethodPut, theirs, `{"limit":{"amount":"1","currency":"USD"}}`, http.StatusNotFound, consoleapi.ErrorCodeNotFound},
		{"setting one that is none", http.MethodPut, "ws_00000000000000000000000000", `{}`, http.StatusNotFound, consoleapi.ErrorCodeNotFound},
	} {
		expect(t, r.what, f.do(r.method, "/workspaces/"+r.workspace+"/budget", r.body, as(s)), r.status, r.code)
	}
	if got := f.budget(s, http.MethodPut, main, `{"limit":{"amount":"7","currency":"USD"}}`); got.Limit == nil || got.Limit.Amount != "7" {
		t.Errorf("the default workspace's budget: %+v", got)
	}
	f.run(`UPDATE accounts.members SET role_id = (SELECT id FROM accounts.roles WHERE name = 'member')`)
	if got := f.budget(s, http.MethodGet, main, ""); got.Limit == nil || got.Limit.Amount != "7" {
		t.Errorf("a member reads %+v", got)
	}
	expect(t, "a member sets one", f.do(http.MethodPut, "/workspaces/"+main+"/budget", `{}`, as(s)), http.StatusForbidden, consoleapi.ErrorCodeForbidden)
	if got := f.text(`SELECT count(*)::text FROM platform.audit_log WHERE action = 'workspace.update'`); got != "1" {
		t.Errorf("%s workspace changes audited, want only the default's budget", got)
	}
}
