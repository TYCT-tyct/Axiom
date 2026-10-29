package gateway

import (
	"net/http"
	"testing"
	"time"
	"uuid"
)

func TestKeyReportsRateAndEffectiveBudgets(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(50e9)
	r.sql("UPDATE accounts.accounts SET rate_requests = 100", "UPDATE accounts.api_keys SET rate_requests = 50, spend_limit = 5000000000, limit_reset = 'daily'",
		"UPDATE accounts.workspaces SET budget = 10000000000, budget_reset = 'monthly'")
	r.spendRow(a, uuid.UUID(a.keyID), 0, 1e9, 2e9)
	r.spendRow(a, a.workspace, 0, 6e9, 3e9)
	r.spendRow(a, a.workspace, 400, 99e9, 0)
	resp, body := r.do(http.MethodGet, "/api/v1/key", a.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	d := data(t, body)
	budgets := d["budgets"].([]any)
	effective := d["effective_budget"].(map[string]any)
	if len(budgets) != 2 || effective["scope"] != "workspace" || !near(effective["spend_usd"], 6) || !near(effective["reserved_usd"], 3) || !near(effective["remaining_usd"], 1) || effective["reset_interval"] != "monthly" {
		t.Fatalf("budgets: %s", body)
	}
	requireBudgetReset(t, effective["resets_at"].(string))
	if !near(d["limit"], 5) || !near(d["limit_remaining"], 2) {
		t.Fatalf("key-only legacy fields: %s", body)
	}
}

func TestKeyReportsBusinessRate(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(50e9)
	r.sql("UPDATE accounts.accounts SET rate_requests = 100", "UPDATE accounts.api_keys SET rate_requests = 50")
	_, body := r.do(http.MethodGet, "/api/v1/key", a.key, nil)
	d := data(t, body)
	rate := d["rate_limit"].(map[string]any)
	if !near(rate["requests"], 50) || !near(rate["account_requests"], 100) || rate["interval"] != "1m0s" {
		t.Fatalf("rate: %s", body)
	}
}

func TestKeyBudgetExhaustionAndNoLimits(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(50e9)
	r.sql("UPDATE accounts.workspaces SET budget = 1")
	r.spendRow(a, uuid.UUID(a.keyID), 0, 1e9, 2e9)
	r.spendRow(a, a.workspace, 0, 6e9, 3e9)
	var body string
	var d map[string]any
	// When both are spent, report the first budget billing rejects, even when the other has a larger overrun.
	r.sql("UPDATE accounts.api_keys SET spend_limit = 0", "UPDATE accounts.workspaces SET budget = 0")
	_, body = r.do(http.MethodGet, "/api/v1/key", a.key, nil)
	if data(t, body)["effective_budget"].(map[string]any)["scope"] != "workspace" {
		t.Fatal(body)
	}
	r.sql("UPDATE accounts.api_keys SET spend_limit = NULL, limit_reset = NULL", "UPDATE accounts.workspaces SET budget = NULL, budget_reset = NULL")
	_, body = r.do(http.MethodGet, "/api/v1/key", a.key, nil)
	d = data(t, body)
	if len(d["budgets"].([]any)) != 0 || d["effective_budget"] != nil {
		t.Fatalf("unlimited budgets: %s", body)
	}
	r.sql("UPDATE accounts.api_keys SET spend_limit = 5000000000")
	_, body = r.do(http.MethodGet, "/api/v1/key", a.key, nil)
	if data(t, body)["effective_budget"].(map[string]any)["resets_at"] != nil {
		t.Fatalf("lifetime reset: %s", body)
	}
}

func TestEffectiveBudgetComparesExactUnits(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(50e9)
	// These amounts round to the same float64; admission still distinguishes one billionth.
	r.sql("UPDATE accounts.api_keys SET spend_limit = 9000000000000000000, limit_reset = NULL", "UPDATE accounts.workspaces SET budget = 9000000000000000001")
	_, body := r.do(http.MethodGet, "/api/v1/key", a.key, nil)
	if data(t, body)["effective_budget"].(map[string]any)["scope"] != "key_limit" {
		t.Fatal(body)
	}
	// Each counter fits int64, but their sum does not. Remaining must saturate at zero.
	r.spendRow(a, a.workspace, 0, 8000000000000000000, 8000000000000000000)
	_, body = r.do(http.MethodGet, "/api/v1/key", a.key, nil)
	effective := data(t, body)["effective_budget"].(map[string]any)
	if effective["scope"] != "workspace" || !near(effective["remaining_usd"], 0) {
		t.Fatal(body)
	}
}

func requireBudgetReset(t *testing.T, text string) {
	t.Helper()
	at, err := time.Parse(time.RFC3339, text)
	if err != nil || at.Day() != 1 || at.Hour() != 0 || !at.After(time.Now()) {
		t.Fatalf("reset: %v %v", at, err)
	}
}
