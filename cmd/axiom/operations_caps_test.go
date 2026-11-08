package main

import (
	"fmt"
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

// TestOperationsAgentLimitsLowered: the owner lowers a credential's money writes in a minute, then in an hour, then its
// requests, each from the revision read and keeping the limits left out, and the agent is held back sooner from its
// next request on; previews count as money writes.
func TestOperationsAgentLimitsLowered(t *testing.T) {
	t.Parallel()
	f, supplier, token := topUpReady(t)
	limits := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		var l adminapi.OperationsAgentLimits
		decode(t, rec, &l)
		return fmt.Sprintf("requests %d/%d, money %d/%d a minute and %d/%d an hour, alerts %d/%d, rev %d", *l.RequestsPerMinute,
			*l.RequestsPerMinuteMax, *l.MoneyWritesPerMinute, *l.MoneyWritesPerMinuteMax, *l.MoneyWritesPerHour,
			*l.MoneyWritesPerHourMax, *l.AlertsPerHour, *l.AlertsPerHourMax, *l.Rev)
	}
	set := func(body, want string) {
		t.Helper()
		rec := f.change(f.s, http.MethodPut, "/operations/agent-limits", body, newKey())
		if got := limits(rec); rec.Code != http.StatusOK || got != want {
			t.Fatalf("setting %s: %d %s", body, rec.Code, got)
		}
	}
	if got := limits(f.do(http.MethodGet, "/operations/agent-limits", "", as(f.s))); got != "requests 600/600, money 30/30 a minute and 300/300 an hour, alerts 60/60, rev 0" {
		t.Errorf("before the owner sets them: %s", got)
	}
	preview := func(token string) *httptest.ResponseRecorder {
		return f.proposeTopUp(token, supplier, "1", newKey(), `,"dry_run":true`)
	}
	for i := range 2 {
		expectAdmin(t, "a money write before lowering "+strconv.Itoa(i), preview(token), http.StatusOK, "")
	}
	set(`{"alerts_per_hour":60,"money_writes_per_minute":2,"rev":0,"reason":"slower spending"}`,
		"requests 600/600, money 2/30 a minute and 300/300 an hour, alerts 60/60, rev 1")
	expectAdmin(t, "the third money write, which the default lets through", preview(token), http.StatusTooManyRequests, codeRateLimited)
	expectAdmin(t, "a read", f.agent(token, because("/caps", "x")), http.StatusOK, "")

	set(`{"alerts_per_hour":60,"money_writes_per_minute":30,"money_writes_per_hour":2,"rev":1,"reason":"fewer an hour"}`,
		"requests 600/600, money 30/30 a minute and 2/300 an hour, alerts 60/60, rev 2")
	fresh := f.moneyAgent(true)
	expectAdmin(t, "the first money write under 2 an hour", preview(fresh), http.StatusOK, "")
	expectAdmin(t, "the second money write under 2 an hour", preview(fresh), http.StatusTooManyRequests, codeRateLimited)

	set(`{"alerts_per_hour":60,"requests_per_minute":10,"rev":2,"reason":"a quieter patrol"}`,
		"requests 10/600, money 30/30 a minute and 2/300 an hour, alerts 60/60, rev 3")
	reader := f.moneyAgent(false)
	expectAdmin(t, "the first request under 10 a minute", f.agent(reader, because("/me", "x")), http.StatusOK, "")
	rec := f.agent(reader, because("/me", "x"))
	expectAdmin(t, "the second request under 10 a minute", rec, http.StatusTooManyRequests, codeRateLimited)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	expectAdmin(t, "from a stale revision", f.change(f.s, http.MethodPut, "/operations/agent-limits",
		`{"alerts_per_hour":60,"requests_per_minute":20,"rev":2,"reason":"r"}`, newKey()), http.StatusConflict, codeStaleRevision)
	expectAdmin(t, "past a ceiling", f.change(f.s, http.MethodPut, "/operations/agent-limits",
		`{"alerts_per_hour":60,"money_writes_per_hour":301,"rev":3,"reason":"r"}`, newKey()), http.StatusBadRequest, codeInvalidRequest)
	got := f.text(`SELECT string_agg(concat_ws(':', detail->'after'->>'requests_per_minute', detail->'after'->>'money_writes_per_minute',
		detail->'after'->>'money_writes_per_hour', actor_kind), ',' ORDER BY at, id) FROM platform.audit_log WHERE action = 'agent_limits.set'`)
	if got != "600:2:300:user,600:30:2:user,10:30:2:user" {
		t.Errorf("the audit: %s", got)
	}
}
