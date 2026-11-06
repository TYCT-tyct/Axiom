package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// creditLimits is the main site's credit caps in the tests, in billionths: the autonomous limits bind nothing, as a
// credit grant always waits for the owner; hard single 20, day 30, week 60.
const creditLimits = `"limits":{"single_max":"0","daily_max":"0","weekly_max":"0","object_daily_max":"0","daily_count_max":0,` +
	`"hard_single_max":"20000000000","hard_daily_max":"30000000000","hard_weekly_max":"60000000000"}`

// creditFixture is alice's account on the main site and an agent that may propose credit.
type creditFixture struct {
	grantee
	token string
}

func newCreditFixture(t *testing.T) creditFixture {
	t.Helper()
	f := creditFixture{grantee: newGrantee(t)}
	f.token = f.creditAgent(`"credits.grant","operations.money"`, `"platform":true`)
	return f
}

// creditReady is newCreditFixture with the main site's USD credit caps.
func creditReady(t *testing.T) creditFixture {
	t.Helper()
	f := newCreditFixture(t)
	expectAdmin(t, "the policy", f.setCreditCaps(creditLimits, 0), http.StatusOK, "")
	return f
}

// creditAgent makes a credential with permissions and scope.
func (f creditFixture) creditAgent(permissions, scope string) string {
	f.t.Helper()
	_, token := f.makeCredential(f.s, `{"name":"hermes","permissions":[`+permissions+`],`+scope+`,
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"make up for outages"}`)
	return token
}

func (f creditFixture) setCreditCaps(limits string, rev int) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.change(f.s, http.MethodPut, "/operations/cap-policies/"+f.siteID()+"/money.credit",
		fmt.Sprintf(`{"currency":"USD",%s,"rev":%d,"reason":"the agent's credit budget"}`, limits, rev), newKey())
}

// propose has token propose a gift of amount billionths of a USD to alice, with extra members in the body.
func (f creditFixture) propose(token, amount, key, extra string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.agentSend(token, http.MethodPost, "/accounts/"+f.account+"/credit-grants",
		`{"amount":{"amount":"`+amount+`","currency":"USD"},"reason":"made up for an outage"`+extra+`}`, idempotent(key))
}

// asked proposes a gift of 5 and returns the operation waiting for the owner.
func (f creditFixture) asked() operationsapi.Operation {
	f.t.Helper()
	rec := f.propose(f.token, "5000000000", newKey(), "")
	expectAdmin(f.t, "the proposal", rec, http.StatusAccepted, "")
	return agentOperation(f.t, rec)
}

func (f creditFixture) decide(op operationsapi.Operation, verb string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.change(f.s, http.MethodPost, "/operations/approvals/"+op.Approval.ApprovalId+"/"+verb, `{"reason":"looked at it"}`, newKey())
}

// money is what the database holds of the grants: each money action's state and amount, the site's day counter, and
// the gift batches.
func (f creditFixture) money() string {
	f.t.Helper()
	return f.text(`SELECT coalesce((SELECT string_agg(state || ':' || amount, ',' ORDER BY operation_id) FROM operations.money_actions), '') || ' ' ||
		coalesce((SELECT reserved || '+' || committed || '#' || count FROM operations.cap_counters WHERE category = 'money.credit'
			AND window_kind = 'day' AND object_kind = ''), '-') || ' ' ||
		coalesce((SELECT string_agg(amount::text, ',') FROM billing.credit_batches WHERE source = 'gift'), '-')`)
}

// TestOperationsCreditGrant: a gift always waits for the owner with its face value reserved; the owner sees what it
// gives, and the approval gives it as the agent's, once, with the batch named after the operation and the
// reservation committed.
func TestOperationsCreditGrant(t *testing.T) {
	t.Parallel()
	f := creditReady(t)
	key := newKey()
	rec := f.propose(f.token, "5000000000", key, "")
	expectAdmin(t, "within every limit", rec, http.StatusAccepted, "")
	op := agentOperation(t, rec)
	if op.Risk != operationsapi.Gift || op.Approval == nil {
		t.Fatalf("the proposal: %s", rec.Body)
	}
	if rec := f.propose(f.token, "5000000000", key, ""); rec.Header().Get("Idempotency-Replayed") != "true" || *agentOperation(t, rec).OperationId != *op.OperationId {
		t.Errorf("the retry: %d %s", rec.Code, rec.Body)
	}
	if got := f.money(); got != "reserved:5000000000 5000000000+0#1 -" {
		t.Errorf("while it waits: %s", got)
	}
	rec = f.do(http.MethodGet, "/operations/approvals/"+op.Approval.ApprovalId, "", as(f.s))
	for _, want := range []string{`"account_id":"` + f.account + `"`, `"amount":"5000000000"`, `"hard_single_max":"20000000000"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the owner does not see %s in %s", want, rec.Body)
		}
	}
	var approved adminapi.OperationsApproval
	decode(t, f.decide(op, "approve"), &approved)
	if approved.OperationState != adminapi.OperationStateSucceeded {
		t.Fatalf("the approval: %+v", approved)
	}
	f.expectGiven(*op.OperationId)
}

// expectGiven checks what the approval of operation, a gift of 5 to alice, left: its answer, the money, the batch
// named after it and the audit, and the caps the agent reads.
func (f creditFixture) expectGiven(operation string) {
	t := f.t
	t.Helper()
	rec := f.agent(f.token, because("/operations/"+operation, "check"))
	for _, want := range []string{`"credit_batch_id":"` + operation + `"`, `"reservation":"committed"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the operation does not answer %s: %s", want, rec.Body)
		}
	}
	if got := f.money() + " " + f.balanceOf(); got != "committed:5000000000 0+5000000000#1 5000000000 5000000000" {
		t.Errorf("after the approval: %s", got)
	}
	if got := f.text(`SELECT id::text FROM billing.credit_batches WHERE source = 'gift'`); got != operation {
		t.Errorf("the batch is %s, not the operation's ID", got)
	}
	f.expectAudit("credit.add", map[string]string{"actor_kind": "operations_agent", "reason": "made up for an outage"})
	f.expectAudit("reservation.commit", map[string]string{"actor_kind": "operations_agent"})
	var caps operationsapi.OperationsCapUsageList
	decode(t, f.agent(f.token, because("/caps", "check")), &caps)
	if len(caps.Data) != 1 || caps.Data[0].Category != operationsapi.MoneyCredit || caps.Data[0].Day.Committed != "5000000000" {
		t.Errorf("caps_get: %+v", caps.Data)
	}
}

// TestOperationsCreditGrantRefusals: a gift needs credits.grant and operations.money, a policy, an amount within the
// hard caps and the admin's rules for a grant, and an account in the credential's sites; none of them, nor a preview,
// leaves a reservation.
func TestOperationsCreditGrantRefusals(t *testing.T) {
	t.Parallel()
	f := newCreditFixture(t)
	expectAdmin(t, "without a policy", f.propose(f.token, "1000000000", newKey(), ""), http.StatusUnprocessableEntity, codeCapExceeded)
	expectAdmin(t, "the policy", f.setCreditCaps(creditLimits, 0), http.StatusOK, "")
	child, childTier := uuid.NewV7(), uuid.NewV7()
	f.run("BEGIN",
		fmt.Sprintf(`INSERT INTO accounts.sites (id,parent_site_id,owner_account_id,name,status,key_prefix,registration,default_tier_id)
			SELECT '%s',id,(SELECT a.id FROM accounts.accounts a WHERE a.site_id=s.id LIMIT 1),'Other','active','sk-other-','closed','%s'
			FROM accounts.sites s WHERE s.is_main`, child, childTier),
		fmt.Sprintf(`INSERT INTO accounts.tiers(id,site_id,name) VALUES ('%s','%s','Other')`, childTier, child), "COMMIT")
	if err := f.svc.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	elsewhere := f.creditAgent(`"credits.grant","operations.money"`, `"platform":true,"sites":["`+accounts.SiteID(child).TypeID()+`"]`)
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	for _, c := range []struct {
		name, token, amount, extra string
		status                     int
		code                       errorCode
	}{
		{"without operations.money", f.creditAgent(`"credits.grant"`, `"platform":true`), "1000000000", "", http.StatusForbidden, codeForbidden},
		{"without credits.grant", f.creditAgent(`"operations.money"`, `"platform":true`), "1000000000", "", http.StatusForbidden, codeForbidden},
		{"an account outside its sites", elsewhere, "1000000000", "", http.StatusNotFound, codeNotFound},
		{"over the hard single cap", f.token, "20000000001", "", http.StatusUnprocessableEntity, codeCapExceeded},
		{"nothing", f.token, "0", "", http.StatusUnprocessableEntity, codeUnprocessable},
		{"an expiry that passed", f.token, "1000000000", `,"expires_at":"` + past + `"`, http.StatusUnprocessableEntity, codeUnprocessable},
	} {
		expectAdmin(t, c.name, f.propose(c.token, c.amount, newKey(), c.extra), c.status, c.code)
	}
	rec := f.agentSend(f.token, http.MethodPost, "/accounts/"+f.account+"/credit-grants",
		`{"amount":{"amount":"1000000000","currency":"CNY"},"reason":"r"}`, idempotent(newKey()))
	expectAdmin(t, "another currency", rec, http.StatusBadRequest, codeInvalidRequest)
	rec = f.agentSend(f.token, http.MethodPost, "/accounts/acct_00000000000000000000000000/credit-grants",
		`{"amount":{"amount":"1000000000","currency":"USD"},"reason":"r"}`, idempotent(newKey()))
	expectAdmin(t, "no such account", rec, http.StatusNotFound, codeNotFound)
	rec = f.propose(f.token, "1000000000", newKey(), `,"dry_run":true`)
	if preview := agentOperation(t, rec); rec.Code != http.StatusOK || !preview.DryRun || preview.State != operationsapi.OperationStatePendingApproval {
		t.Errorf("the preview: %d %s", rec.Code, rec.Body)
	}
	if got := f.money(); got != " - -" {
		t.Errorf("after the refusals and the preview: %s", got)
	}
}

// TestOperationsCreditGrantOutcomes: a gift the owner rejects, whose expiry passed by the approval, or whose hard cap
// was lowered under it gives nothing and releases its reservation; the day's count keeps each.
func TestOperationsCreditGrantOutcomes(t *testing.T) {
	t.Parallel()
	f := creditReady(t)
	rejected := f.asked()
	expectAdmin(t, "the rejection", f.decide(rejected, "reject"), http.StatusOK, "")
	expired := f.asked()
	// The expiry passes as the clock would; the operation's parameters are what the approval runs.
	f.run(`UPDATE operations.operations SET params = json_build_object('amount', params->>'amount', 'currency', params->>'currency',
		'expires_at', '2000-01-01T00:00:00Z') WHERE id = '` + *expired.OperationId + `'`)
	f.expectRefused(expired, codeUnprocessable)
	lowered := f.asked()
	expectAdmin(t, "the policy lowered", f.setCreditCaps(strings.Replace(creditLimits, `"hard_daily_max":"30000000000"`, `"hard_daily_max":"4000000000"`, 1), 1), http.StatusOK, "")
	f.expectRefused(lowered, codeStaleRevision)
	if got := f.money() + " " + f.balanceOf(); got != "released:5000000000,released:5000000000,released:5000000000 0+0#3 - 0" {
		t.Errorf("after the outcomes: %s", got)
	}
}

// expectRefused approves op and expects it to end rejected with code.
func (f creditFixture) expectRefused(op operationsapi.Operation, code errorCode) {
	f.t.Helper()
	var approved adminapi.OperationsApproval
	decode(f.t, f.decide(op, "approve"), &approved)
	if approved.OperationState != adminapi.OperationStateRejected || approved.ErrorCode == nil || *approved.ErrorCode != string(code) {
		f.t.Errorf("approving %s: %+v", *op.OperationId, approved)
	}
}

// TestOperationsCreditGrantApprovalRace: two approvals of one gift sent together, held on the operation until both
// wait, give it once: one approves, the other finds it decided.
func TestOperationsCreditGrantApprovalRace(t *testing.T) {
	t.Parallel()
	f := creditReady(t)
	op := f.asked()
	answers := make(chan *httptest.ResponseRecorder, 2)
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT 1 FROM operations.operations WHERE id = $1 FOR UPDATE`, *op.OperationId); err != nil {
			return err
		}
		for range 2 {
			go func() { answers <- f.decide(op, "approve") }()
		}
		waitUntil(t, 10*time.Second, func() bool {
			return f.text(`SELECT count(*)::text FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`) == "2"
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	codes := []int{(<-answers).Code, (<-answers).Code}
	if slices.Sort(codes); !slices.Equal(codes, []int{http.StatusOK, http.StatusConflict}) {
		t.Errorf("the two approvals answered %v", codes)
	}
	if got := f.money() + " " + f.balanceOf(); got != "committed:5000000000 0+5000000000#1 5000000000 5000000000" {
		t.Errorf("after the two approvals: %s", got)
	}
}
