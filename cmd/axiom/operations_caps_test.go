package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"axiom/internal/operations"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// caps reads caps_get as token.
func (f supplierFixture) caps(token string) operationsapi.OperationsCapUsageList {
	f.t.Helper()
	rec := f.agent(token, because("/caps", "how much may I top up"))
	expectAdmin(f.t, "capsGet", rec, http.StatusOK, "")
	var list operationsapi.OperationsCapUsageList
	decode(f.t, rec, &list)
	return list
}

// TestOperationsCapsScope: caps_get lists the policies of the scopes the credential reaches, the platform's only with
// the platform grant.
func TestOperationsCapsScope(t *testing.T) {
	t.Parallel()
	f, _, token := topUpReady(t)
	expectAdmin(t, "the site's policy", f.change(f.s, http.MethodPut, "/operations/cap-policies/"+f.siteID()+"/money.top_up",
		`{"currency":"CNY",`+moneyLimits+`,"rev":0,"reason":"the site's budget"}`, newKey()), http.StatusOK, "")
	_, sites := f.makeCredential(f.s, `{"name":"sites","permissions":["suppliers.manage","operations.money"],"platform":false,
		"sites":["`+f.siteID()+`"],"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	if got := f.caps(sites).Data; len(got) != 1 || got[0].Scope != f.siteID() || got[0].Currency != "CNY" {
		t.Errorf("a credential of one site sees %+v", got)
	}
	if got := f.caps(token).Data; len(got) != 2 || got[0].Scope != "platform" || got[0].Paused || got[0].Day.Reserved != "0" || got[0].Day.Count != 0 {
		t.Errorf("before any top-up the platform credential sees %+v", got)
	}
}

// TestOperationsCapsUse: caps_get shows what the day and the week hold, reserved and committed apart.
func TestOperationsCapsUse(t *testing.T) {
	t.Parallel()
	f, supplier, token := topUpReady(t)
	five := *agentOperation(t, f.proposeTopUp(token, supplier, "2500000", newKey(), "")).OperationId
	expectAdmin(t, "the confirmation", f.confirmTopUp(token, supplier, five), http.StatusOK, "")
	f.proposeTopUp(token, supplier, "1500000", newKey(), "")
	got := f.caps(token).Data[0]
	for _, w := range []operationsapi.OperationsCapWindow{got.Day, got.Week} {
		if w.Reserved != "3000000000" || w.Committed != "5000000000" || w.Count != 2 {
			t.Errorf("after a confirmed five dollars and a reserved three: %+v", w)
		}
	}
	if got.Limits.HardDailyMax != "50000000000" || got.Rev != 1 {
		t.Errorf("the platform's caps: %+v", got)
	}
}

// TestOperationsCapRepair: counters that drift from the money pause the scope, which the owner and the agent both see,
// and every top-up there waits for the owner; the owner's repair recomputes them and resumes it.
func TestOperationsCapRepair(t *testing.T) {
	t.Parallel()
	f, supplier, token := topUpReady(t)
	agentOperation(t, f.proposeTopUp(token, supplier, "2500000", newKey(), ""))
	f.run(`UPDATE operations.cap_counters SET reserved = 1`)
	if n, err := operations.ReconcileCaps(t.Context(), f.db, pg.PlatformScope()); err != nil || n != 1 {
		t.Fatalf("the reconciliation paused %d: %v", n, err)
	}
	var list adminapi.OperationsCapPolicyList
	decode(t, f.do(http.MethodGet, "/operations/cap-policies", "", as(f.s)), &list)
	if list.Data[0].PausedAt == nil || !f.caps(token).Data[0].Paused {
		t.Errorf("the pause is not shown: %+v", list.Data[0])
	}
	expectAdmin(t, "a dollar while paused", f.proposeTopUp(token, supplier, "500000", newKey(), ""), http.StatusAccepted, "")

	repair := func(scope string) *httptest.ResponseRecorder {
		return f.change(f.s, http.MethodPost, "/operations/cap-policies/"+scope+"/money.top_up/repair", `{"reason":"recount"}`, newKey())
	}
	expectAdmin(t, "a scope without a policy", repair(f.siteID()), http.StatusNotFound, codeNotFound)
	rec := repair("platform")
	var resumed adminapi.OperationsCapPolicy
	decode(t, rec, &resumed)
	if rec.Code != http.StatusOK || resumed.PausedAt != nil {
		t.Errorf("the repair: %d %s", rec.Code, rec.Body)
	}
	if got := f.text(`SELECT reserved::text FROM operations.cap_counters WHERE window_kind = 'day' AND object_kind = ''`); got != "6000000000" {
		t.Errorf("the day's reservation after the repair: %s", got)
	}
	f.expectAudit("cap_counters.repair", map[string]string{"actor_kind": "user", "reason": "recount"})
	expectAdmin(t, "a dollar after the repair", f.proposeTopUp(token, supplier, "500000", newKey(), ""), http.StatusOK, "")
}

// TestOperationsMoneyRate: a credential's money writes have their own limit, previews and refusals included; past it
// a money write is a recorded 429 with Retry-After, and the credential's other calls go on.
func TestOperationsMoneyRate(t *testing.T) {
	t.Parallel()
	f, supplier, token := topUpReady(t)
	for i := range 15 {
		expectAdmin(t, "money write "+strconv.Itoa(i), f.proposeTopUp(token, supplier, "1", newKey(), `,"dry_run":true`), http.StatusOK, "")
	}
	rec := f.proposeTopUp(token, supplier, "1", newKey(), "")
	expectAdmin(t, "past the limit", rec, http.StatusTooManyRequests, codeRateLimited)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	expectAdmin(t, "a read", f.agent(token, because("/caps", "x")), http.StatusOK, "")
	log := f.agent(token, because("/log", "x")).Body.String()
	if !strings.Contains(log, `"error_code":"rate_limited"`) || !strings.Contains(log, `"tool":"supplier_top_up_propose"`) {
		t.Errorf("the refusal is not in the log: %s", log)
	}
	if got := f.text(`SELECT count(*)::text FROM operations.money_actions`); got != "0" {
		t.Errorf("%s reservations after previews and a refusal", got)
	}
}
