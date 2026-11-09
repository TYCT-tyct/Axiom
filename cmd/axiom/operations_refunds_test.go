package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// refundLimits is the main site's refund caps in the tests, in billionths: the autonomous limits bind nothing, as a
// refund always waits for the owner; hard single 20, day 30, week 60.
const refundLimits = creditLimits

// refundFixture is a paid order of 10 USD of credit, bought for 20.30 USD with a fee of 0.30, on alice's account, through a merchant account that refunds through
// its channel, and an agent that may propose refunds.
type refundFixture struct {
	creditFixture
	order string // as the API writes it
}

func newRefundFixture(t *testing.T) refundFixture {
	t.Helper()
	f := refundFixture{grantee: newGrantee(t)}
	f.token = f.creditAgent(`"refunds.manage","operations.money"`, `"platform":true`)
	merchantBody := strings.NewReplacer(`"refund_enabled": false`, `"refund_enabled": true`, `"mode": "redirect"`, `"mode": "redirect", "refund": true`).
		Replace(merchantJSON(f.siteID(), "Gateway", false, 0, "0", true))
	rec := f.change(f.s, http.MethodPost, "/payments/merchants", merchantBody, newKey())
	expectAdmin(t, "the merchant", rec, http.StatusCreated, "")
	var merchant adminapi.Merchant
	decode(t, rec, &merchant)
	id := must(payments.ParseMerchantID(*merchant.Id))
	order := f.seedOrder(uuid.UUID(id), "alice@example.com", "paid", time.Now().UTC())
	f.order = payments.OrderID(order).TypeID()
	usd := must(platform.ParseCurrency("USD"))
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return billing.CreditPaid(t.Context(), tx, billing.PaidCredit{
			ID: order, Site: uuid.MustParse(f.siteUUID()), Account: uuid.MustParse(f.accountUUID("alice@example.com")),
			SiteCurrency: usd, Credited: 10_000_000_000, Payment: billing.Payment{Currency: usd, Paid: 2030, Fee: 30},
			Channel: billing.Channel{Merchant: uuid.UUID(id)}, Actor: audit.System("payments"), Reason: "confirmed payment",
		})
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// refundReady is newRefundFixture with the main site's USD refund caps.
func refundReady(t *testing.T) refundFixture {
	t.Helper()
	f := newRefundFixture(t)
	expectAdmin(t, "the policy", f.setRefundCaps(refundLimits, 0), http.StatusOK, "")
	return f
}

func (f refundFixture) setRefundCaps(limits string, rev int) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.change(f.s, http.MethodPut, "/operations/cap-policies/"+f.siteID()+"/money.refund",
		fmt.Sprintf(`{"currency":"USD",%s,"rev":%d,"reason":"the agent's refund budget"}`, limits, rev), newKey())
}

// proposeRefund has token propose refunding amount billionths of a USD of the order, with extra members in the body.
func (f refundFixture) proposeRefund(token, amount, key, extra string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.agentSend(token, http.MethodPost, "/orders/"+f.order+"/refunds",
		`{"amount":{"amount":"`+amount+`","currency":"USD"},"reason":"the credits went unused"`+extra+`}`, idempotent(key))
}

// askedRefund proposes refunding 5 of the 10 and returns the operation waiting for the owner.
func (f refundFixture) askedRefund() operationsapi.Operation {
	f.t.Helper()
	rec := f.proposeRefund(f.token, "5000000000", newKey(), "")
	expectAdmin(f.t, "the proposal", rec, http.StatusAccepted, "")
	return agentOperation(f.t, rec)
}

// refunds is what the database holds of the refunds: each money action's state and amount, the site's day counter,
// and each refund's state and whether the agent asked for it.
func (f refundFixture) refunds() string {
	f.t.Helper()
	return f.text(`SELECT coalesce((SELECT string_agg(state || ':' || amount, ',' ORDER BY operation_id) FROM operations.money_actions), '') || ' ' ||
		coalesce((SELECT reserved || '+' || committed || '#' || count FROM operations.cap_counters WHERE category = 'money.refund'
			AND window_kind = 'day' AND object_kind = ''), '-') || ' ' ||
		coalesce((SELECT string_agg(state || ':' || credits || ':' || requested_by_agent, ',' ORDER BY id) FROM payments.refunds), '-')`)
}

// conclude runs the outcome job once, as a node does every minute.
func (f refundFixture) conclude() {
	f.t.Helper()
	service, err := payments.Open(payments.Config{DB: f.db, Platform: pg.PlatformScope(), Keyring: masterKeys(f.t), Plugins: paymentPlugins})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := concludeRefunds(f.t.Context(), f.db, service); err != nil {
		f.t.Fatal(err)
	}
}

// TestOperationsRefund: a refund waits for the owner with its quoted cash reserved; the owner sees what it returns;
// the approval freezes and approves it as the owner's, with the credential as its requester, and leaves the operation
// executing; the refund's outcome ends it, committing its reservation or releasing it, once.
func TestOperationsRefund(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		outcome, want string
	}{
		{"succeeded", "committed:10000000000 0+10000000000#1 succeeded:5000000000:true succeeded"},
		{"failed", "released:10000000000 0+0#1 failed:5000000000:true failed"},
	} {
		t.Run(c.outcome, func(t *testing.T) {
			t.Parallel()
			f := refundReady(t)
			op := f.proposedRefund()
			var approved adminapi.OperationsApproval
			decode(t, f.decide(op, "approve"), &approved)
			if approved.OperationState != adminapi.OperationStateExecuting {
				t.Fatalf("the approval: %+v", approved)
			}
			f.expectRefundShown(op)
			f.run(fmt.Sprintf(`UPDATE payments.refunds SET state = '%s', sent_at = now(), finished_at = now()`, c.outcome))
			f.conclude()
			f.conclude()
			if got := f.refunds() + " " + f.text(`SELECT state FROM operations.operations WHERE id = '`+*op.OperationId+`'`); got != c.want {
				t.Errorf("after the outcome: %s, want %s", got, c.want)
			}
		})
	}
}

// proposedRefund proposes refunding 5 of the 10, checks the proposal, its retry, its reservation and what the owner
// is shown, and returns it.
func (f refundFixture) proposedRefund() operationsapi.Operation {
	t := f.t
	t.Helper()
	key := newKey()
	rec := f.proposeRefund(f.token, "5000000000", key, "")
	expectAdmin(t, "the proposal", rec, http.StatusAccepted, "")
	op := agentOperation(t, rec)
	if op.Risk != operationsapi.Refund || op.Approval == nil {
		t.Fatalf("the proposal: %s", rec.Body)
	}
	if rec := f.proposeRefund(f.token, "5000000000", key, ""); rec.Header().Get("Idempotency-Replayed") != "true" || *agentOperation(t, rec).OperationId != *op.OperationId {
		t.Errorf("the retry: %d %s", rec.Code, rec.Body)
	}
	// 5 of the 10 credits bought with 20.00 net of the fee return 10.00, which the caps count.
	if got := f.refunds(); got != "reserved:10000000000 10000000000+0#1 -" {
		t.Errorf("while it waits: %s", got)
	}
	rec = f.do(http.MethodGet, "/operations/approvals/"+op.Approval.ApprovalId, "", as(f.s))
	for _, want := range []string{`"order_id":"` + f.order + `"`, `"email":"alice@example.com"`, `"cash":"1000"`, `"credits":"5000000000"`, `"hard_single_max":"20000000000"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the owner does not see %s in %s", want, rec.Body)
		}
	}
	return op
}

// expectRefundShown checks the refund op made: approved by the owner as the payments review, requested by the
// credential, recorded in the audit as each of theirs, and the operation answers it.
func (f refundFixture) expectRefundShown(op operationsapi.Operation) {
	t := f.t
	t.Helper()
	if got := f.refunds(); got != "reserved:10000000000 10000000000+0#1 frozen:5000000000:true" {
		t.Errorf("after the approval: %s", got)
	}
	rec := f.do(http.MethodGet, "/payments/refunds?site_id="+f.siteID(), "", as(f.s))
	var list adminapi.PaymentRefundList
	decode(t, rec, &list)
	if len(list.Data) != 1 || list.Data[0].RequestedBy != nil || list.Data[0].RequestedByAgent == nil ||
		!strings.HasPrefix(*list.Data[0].RequestedByAgent, "opcred_") || valueOf(list.Data[0].ApprovedBy) != f.userOf(ownerEmail) {
		t.Errorf("the operator's list: %s", rec.Body)
	}
	f.expectAudit("refund.request", map[string]string{"actor_kind": "operations_agent", "reason": "the credits went unused"})
	f.expectAudit("refund.approve", map[string]string{"actor_kind": "user"})
	rec = f.agent(f.token, because("/operations/"+*op.OperationId, "check"))
	for _, want := range []string{`"state":"executing"`, `"refund_id":"rfd_`, `"refund_approved":true`, `"reservation":"reserved"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the operation does not answer %s: %s", want, rec.Body)
		}
	}
}

// TestOperationsRefundRefusals: a refund needs refunds.manage and operations.money, a policy, an amount within the hard
// caps and the order's unused credits, a merchant that refunds through its channel, and an order in the credential's
// sites; none of them, nor a preview, leaves a reservation or a refund.
func TestOperationsRefundRefusals(t *testing.T) {
	t.Parallel()
	f := newRefundFixture(t)
	expectAdmin(t, "without a policy", f.proposeRefund(f.token, "1000000000", newKey(), ""), http.StatusUnprocessableEntity, codeCapExceeded)
	expectAdmin(t, "the policy", f.setRefundCaps(strings.Replace(refundLimits, `"hard_single_max":"20000000000"`, `"hard_single_max":"4000000000"`, 1), 0), http.StatusOK, "")
	elsewhere := f.creditAgent(`"refunds.manage","operations.money"`, `"platform":true,"sites":["`+accounts.SiteID(f.childSite()).TypeID()+`"]`)
	for _, c := range []struct {
		name, token, amount string
		status              int
		code                errorCode
	}{
		{"without operations.money", f.creditAgent(`"refunds.manage"`, `"platform":true`), "1000000000", http.StatusForbidden, codeForbidden},
		{"without refunds.manage", f.creditAgent(`"operations.money"`, `"platform":true`), "1000000000", http.StatusForbidden, codeForbidden},
		{"an order outside its sites", elsewhere, "1000000000", http.StatusNotFound, codeNotFound},
		{"over the hard single cap", f.token, "5000000000", http.StatusUnprocessableEntity, codeCapExceeded},
		{"more than the order's credits", f.token, "10000000001", http.StatusUnprocessableEntity, codeUnprocessable},
		{"nothing", f.token, "0", http.StatusUnprocessableEntity, codeUnprocessable},
	} {
		expectAdmin(t, c.name, f.proposeRefund(c.token, c.amount, newKey(), ""), c.status, c.code)
	}
	rec := f.agentSend(f.token, http.MethodPost, "/orders/"+f.order+"/refunds", `{"amount":{"amount":"1000000000","currency":"CNY"},"reason":"r"}`, idempotent(newKey()))
	expectAdmin(t, "another currency", rec, http.StatusBadRequest, codeInvalidRequest)
	rec = f.agentSend(f.token, http.MethodPost, "/orders/ord_00000000000000000000000000/refunds", `{"amount":{"amount":"1000000000","currency":"USD"},"reason":"r"}`, idempotent(newKey()))
	expectAdmin(t, "no such order", rec, http.StatusNotFound, codeNotFound)
	rec = f.proposeRefund(f.token, "1000000000", newKey(), `,"dry_run":true`)
	if preview := agentOperation(t, rec); rec.Code != http.StatusOK || !preview.DryRun || preview.State != operationsapi.OperationStatePendingApproval {
		t.Errorf("the preview: %d %s", rec.Code, rec.Body)
	}
	f.run(`UPDATE payments.merchants SET refund_enabled = false`)
	expectAdmin(t, "a merchant that does not refund", f.proposeRefund(f.token, "1000000000", newKey(), ""), http.StatusConflict, codeConflict)
	if got := f.refunds(); got != " - -" {
		t.Errorf("after the refusals and the preview: %s", got)
	}
}

// TestOperationsRefundOutcomes: a refund the owner rejects, whose order has another refund in flight by the approval,
// whose quote is not what the approval would return (as when the order's credits were spent meanwhile), or whose hard
// cap was lowered under it, freezes nothing and releases its reservation; the day's count keeps each.
func TestOperationsRefundOutcomes(t *testing.T) {
	t.Parallel()
	f := refundReady(t)
	rejected := f.askedRefund()
	expectAdmin(t, "the rejection", f.decide(rejected, "reject"), http.StatusOK, "")
	inFlight := f.askedRefund()
	f.seedRefund(uuid.UUID(must(payments.ParseOrderID(f.order))), "alice@example.com")
	f.expectRefused(inFlight, codeConflict)
	f.run(`DELETE FROM payments.refunds`)
	spent := f.askedRefund()
	// The quote the owner saw is not what the order returns now.
	f.run(`UPDATE operations.money_actions SET basis = json_build_object('credits', '5000000000', 'cash', '999', 'pay_currency', 'USD')
		WHERE operation_id = '` + *spent.OperationId + `'`)
	f.expectRefused(spent, codeStaleRevision)
	lowered := f.askedRefund()
	expectAdmin(t, "the policy lowered", f.setRefundCaps(strings.Replace(refundLimits, `"hard_daily_max":"30000000000"`, `"hard_daily_max":"4000000000"`, 1), 1), http.StatusOK, "")
	f.expectRefused(lowered, codeStaleRevision)
	if got := f.refunds(); got != "released:10000000000,released:10000000000,released:10000000000,released:10000000000 0+0#4 -" {
		t.Errorf("after the outcomes: %s", got)
	}
}

// must is v, failing the program on err; for values a test built itself.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
