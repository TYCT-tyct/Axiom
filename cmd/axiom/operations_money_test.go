package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"axiom/internal/platform/adminapi"
)

// moneyLimits is the platform's top-up caps in the tests, in billionths: autonomous single 10, day 25, week 40,
// supplier day 15 and 10 a day; hard single 20, day 50, week 60.
const moneyLimits = `"limits":{"single_max":"10000000000","daily_max":"25000000000","weekly_max":"40000000000",` +
	`"object_daily_max":"15000000000","daily_count_max":10,"hard_single_max":"20000000000","hard_daily_max":"50000000000",` +
	`"hard_weekly_max":"60000000000"}`

// setCaps sets the platform's top-up policy in currency from revision rev.
func (f supplierFixture) setCaps(currency string, rev int, limits string) *httptest.ResponseRecorder {
	f.t.Helper()
	body := `{"currency":"` + currency + `",` + limits + `,"rev":` + strconv.Itoa(rev) + `,"reason":"the agent's budget"}`
	return f.change(f.s, http.MethodPut, "/operations/cap-policies/platform/money.top_up", body, newKey())
}

// moneyAgent makes a credential that may top suppliers up, and one with the same powers but no money.
func (f supplierFixture) moneyAgent(money bool) string {
	f.t.Helper()
	permissions := `"suppliers.view","suppliers.manage"`
	if money {
		permissions += `,"operations.money"`
	}
	_, token := f.makeCredential(f.s, `{"name":"hermes","permissions":[`+permissions+`],"platform":true,
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"top suppliers up"}`)
	return token
}

// unitValue confirms what one unit of the supplier's balance is worth.
func (f supplierFixture) unitValue(supplier, value string) {
	f.t.Helper()
	rec := f.change(f.s, http.MethodPut, "/suppliers/"+supplier+"/unit-value", `{"value":"`+value+`","reason":"checked the price page"}`, newKey())
	expectAdmin(f.t, "confirmSupplierUnitValue", rec, http.StatusOK, "")
}

func (f supplierFixture) proposeTopUp(token, supplier, amount, key, extra string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.agentSend(token, http.MethodPost, "/suppliers/"+supplier+"/top-ups", `{"amount_native":"`+amount+`","reason":"balance is low"`+extra+`}`, idempotent(key))
}

func (f supplierFixture) confirmTopUp(token, supplier, operation string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.agentSend(token, http.MethodPost, "/suppliers/"+supplier+"/top-ups/"+operation+"/confirm", `{"proof":"receipt 42","reason":"the balance rose"}`, idempotent(newKey()))
}

// topUpReady is a platform supplier worth 0.000002 USD a quota unit, the platform's USD policy and an agent that may
// top it up.
func topUpReady(t *testing.T) (f supplierFixture, supplier, token string) {
	t.Helper()
	f = newSupplierFixture(t)
	supplier = f.connect().Id
	token = f.moneyAgent(true)
	f.unitValue(supplier, "0.000002") // 500000 quota to the dollar
	expectAdmin(t, "the policy", f.setCaps("USD", 0, moneyLimits), http.StatusOK, "")
	return f, supplier, token
}

// approve is the owner's approval of an approval the agent's top-up waits on.
func (f supplierFixture) approve(approval string) adminapi.OperationsApproval {
	f.t.Helper()
	var approved adminapi.OperationsApproval
	decode(f.t, f.change(f.s, http.MethodPost, "/operations/approvals/"+approval+"/approve", `{"reason":"pay it"}`, newKey()), &approved)
	return approved
}

// TestOperationsTopUpRefusals: a top-up needs operations.money, a confirmed unit value and a policy, and one over a
// hard cap is refused; none of them, nor a preview, leaves a reservation.
func TestOperationsTopUpRefusals(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	expectAdmin(t, "without operations.money", f.proposeTopUp(f.moneyAgent(false), supplier, "1", newKey(), ""), http.StatusForbidden, codeForbidden)
	token := f.moneyAgent(true)
	expectAdmin(t, "without a unit value", f.proposeTopUp(token, supplier, "1", newKey(), ""), http.StatusUnprocessableEntity, codeUnprocessable)
	f.unitValue(supplier, "0.000002")
	expectAdmin(t, "without a policy", f.proposeTopUp(token, supplier, "1", newKey(), ""), http.StatusUnprocessableEntity, codeCapExceeded)
	expectAdmin(t, "the policy", f.setCaps("USD", 0, moneyLimits), http.StatusOK, "")
	expectAdmin(t, "over the hard single cap", f.proposeTopUp(token, supplier, "10500000", newKey(), ""), http.StatusUnprocessableEntity, codeCapExceeded)
	rec := f.proposeTopUp(token, supplier, "6000000", newKey(), `,"dry_run":true`)
	if preview := agentOperation(t, rec); rec.Code != http.StatusOK || !preview.DryRun || preview.State != adminapi.OperationStatePendingApproval {
		t.Errorf("the preview: %d %s", rec.Code, rec.Body)
	}
	if got := f.text(`SELECT count(*)::text FROM operations.money_actions`); got != "0" {
		t.Errorf("%s reservations after the refusals and the preview", got)
	}
}

// TestOperationsTopUp: within every autonomous cap a top-up is reserved at once and found again under its key; over
// one it waits for the owner, and the owner's approval keeps it reserved.
func TestOperationsTopUp(t *testing.T) {
	t.Parallel()
	f, supplier, token := topUpReady(t)
	key := newKey()
	rec := f.proposeTopUp(token, supplier, "2500000", key, "")
	five := agentOperation(t, rec)
	if rec.Code != http.StatusOK || five.State != adminapi.OperationStateSucceeded || !strings.Contains(rec.Body.String(), `"amount":"5000000000"`) || !strings.Contains(rec.Body.String(), `"reservation":"reserved"`) {
		t.Errorf("five dollars: %d %s", rec.Code, rec.Body)
	}
	if rec := f.proposeTopUp(token, supplier, "2500000", key, ""); rec.Header().Get("Idempotency-Replayed") != "true" || *agentOperation(t, rec).OperationId != *five.OperationId {
		t.Errorf("the retry: %d %s", rec.Code, rec.Body)
	}
	rec = f.proposeTopUp(token, supplier, "6000000", newKey(), "")
	expectAdmin(t, "twelve dollars", rec, http.StatusAccepted, "")
	twelve := agentOperation(t, rec)
	rec = f.do(http.MethodGet, "/operations/approvals/"+twelve.Approval.ApprovalId, "", as(f.s))
	// Twelve is over the single limit of ten and, after five, the supplier's daily fifteen: both are why it waits.
	if !strings.Contains(rec.Body.String(), `"amount":"12000000000"`) || !strings.Contains(rec.Body.String(), `"single_max":"10000000000"`) ||
		!strings.Contains(rec.Body.String(), `"reasons":[{"code":"cap_single"},{"code":"cap_object_daily"}]`) {
		t.Errorf("the owner sees %s", rec.Body)
	}
	if approved := f.approve(twelve.Approval.ApprovalId); approved.OperationState != adminapi.OperationStateSucceeded {
		t.Errorf("the approval: %+v", approved)
	}
	if got := f.text(`SELECT string_agg(state || ':' || amount, ',' ORDER BY operation_id) FROM operations.money_actions`); got != "reserved:5000000000,reserved:12000000000" {
		t.Errorf("the reservations are %s", got)
	}
}

// TestOperationsTopUpConfirm: the agent confirms its own reservation once with proof; another credential cannot.
func TestOperationsTopUpConfirm(t *testing.T) {
	t.Parallel()
	f, supplier, token := topUpReady(t)
	five := *agentOperation(t, f.proposeTopUp(token, supplier, "2500000", newKey(), "")).OperationId
	expectAdmin(t, "another credential's", f.confirmTopUp(f.moneyAgent(true), supplier, five), http.StatusNotFound, codeNotFound)
	rec := f.confirmTopUp(token, supplier, five)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reservation":"committed"`) {
		t.Errorf("the confirmation: %d %s", rec.Code, rec.Body)
	}
	expectAdmin(t, "confirming twice", f.confirmTopUp(token, supplier, five), http.StatusConflict, codeConflict)
	if got := f.text(`SELECT reserved || '+' || committed FROM operations.cap_counters WHERE window_kind = 'day' AND object_kind = ''`); got != "0+5000000000" {
		t.Errorf("the day's counter is %s", got)
	}
	f.expectAudit("reservation.commit", map[string]string{"actor_kind": "operations_agent", "reason": "the balance rose"})
}

// TestOperationsTopUpPolicy: an approval after the policy was lowered under the proposal ends rejected and releases
// it; a policy keeps its currency and is changed from the revision read.
func TestOperationsTopUpPolicy(t *testing.T) {
	t.Parallel()
	f, supplier, token := topUpReady(t)
	stale := agentOperation(t, f.proposeTopUp(token, supplier, "6000000", newKey(), ""))
	lowered := strings.NewReplacer(`"daily_max":"25000000000"`, `"daily_max":"5000000000"`, `"hard_daily_max":"50000000000"`, `"hard_daily_max":"11000000000"`).Replace(moneyLimits)
	expectAdmin(t, "the policy lowered", f.setCaps("USD", 1, lowered), http.StatusOK, "")
	if approved := f.approve(stale.Approval.ApprovalId); approved.OperationState != adminapi.OperationStateRejected || approved.ErrorCode == nil || *approved.ErrorCode != string(codeStaleRevision) {
		t.Errorf("the approval after the policy was lowered: %+v", approved)
	}
	if got := f.text(`SELECT m.state || ' ' || c.reserved || '#' || c.count FROM operations.money_actions m, operations.cap_counters c WHERE c.window_kind = 'day' AND c.object_kind = ''`); got != "released 0#1" {
		t.Errorf("after the refused approval: %s", got)
	}
	var list adminapi.OperationsCapPolicyList
	decode(t, f.do(http.MethodGet, "/operations/cap-policies", "", as(f.s)), &list)
	if len(list.Data) != 1 || list.Data[0].Rev != 2 || list.Data[0].Scope != "platform" || list.Data[0].Limits.HardDailyMax != "11000000000" {
		t.Errorf("the policies: %+v", list)
	}
	expectAdmin(t, "another currency", f.setCaps("CNY", 2, moneyLimits), http.StatusConflict, codeConflict)
	expectAdmin(t, "a stale policy", f.setCaps("USD", 1, moneyLimits), http.StatusConflict, codeStaleRevision)
}

// TestOperationsTopUpExchange: a supplier that settles in another currency than the policy's is valued at the current
// rate, rounded up to the billionth, so the caps never count less than the money.
func TestOperationsTopUpExchange(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	token := f.moneyAgent(true)
	f.unitValue(supplier, "0.000002")
	expectAdmin(t, "the policy", f.setCaps("CNY", 0, moneyLimits), http.StatusOK, "")
	expectAdmin(t, "without a rate", f.proposeTopUp(token, supplier, "1", newKey(), ""), http.StatusUnprocessableEntity, codeUnprocessable)
	expectAdmin(t, "the rate", f.change(f.s, http.MethodPost, "/pricing/exchange-rates", initialExchange, newKey()), http.StatusCreated, "")
	for _, c := range []struct{ unit, native, want string }{
		{"0.000002", "500000", `"amount":"7100000000"`}, // 1 dollar at 7.1
		{"0.0000000001", "1", `"amount":"1"`},           // 0.71 billionths, rounded up
	} {
		f.unitValue(supplier, c.unit)
		if rec := f.proposeTopUp(token, supplier, c.native, newKey(), ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s at %s: %d %s", c.native, c.unit, rec.Code, rec.Body)
		}
	}
}

// TestOperationsTopUpReplay: a retry under the key of a top-up that succeeded finds it after the supplier was removed,
// where a new proposal is refused; the key's first answer was lost, not the operation.
func TestOperationsTopUpReplay(t *testing.T) {
	t.Parallel()
	f, supplier, token := topUpReady(t)
	key := newKey()
	first := agentOperation(t, f.proposeTopUp(token, supplier, "2500000", key, ""))
	expectAdmin(t, "removeSupplier", f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/removal", `{"reason":"closed"}`, newKey()), http.StatusOK, "")
	rec := f.proposeTopUp(token, supplier, "2500000", key, "")
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotency-Replayed") != "true" || *agentOperation(t, rec).OperationId != *first.OperationId {
		t.Errorf("the retry after the removal: %d %s", rec.Code, rec.Body)
	}
	expectAdmin(t, "a new proposal after the removal", f.proposeTopUp(token, supplier, "2500000", newKey(), ""), http.StatusConflict, codeConflict)
}
