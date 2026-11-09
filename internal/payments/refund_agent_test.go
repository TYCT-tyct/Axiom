package payments

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

// agentRefund is a request of credits of o by a fresh credential under key, approved by a fresh person.
func agentRefund(o Order, credits int64, key string) AgentRefund {
	return AgentRefund{Order: o.ID, Credits: credits, Reason: "an outage", Agent: uuid.NewV7(), Key: key, Approver: uuid.NewV7(), ApprovalReason: "checked"}
}

// requestAgentRefund runs request (QuoteAgentRefundTx or RequestAgentRefundTx) in a platform transaction of its own,
// as the operations API's proposal and approval do.
func (w *world) requestAgentRefund(request agentRequest, r AgentRefund) (Refund, error) {
	var refund Refund
	err := w.tx(w.p.platform, func(tx pg.Tx) (err error) {
		refund, err = request(w.p, w.t.Context(), tx, r)
		return err
	})
	return refund, err
}

type agentRequest = func(*Payments, context.Context, pg.Tx, AgentRefund) (Refund, error)

var quoteAgent, requestAgent agentRequest = (*Payments).QuoteAgentRefundTx, (*Payments).RequestAgentRefundTx

// TestAgentRefund: a quote freezes and records nothing; the request freezes what the quote said, names the credential
// as its requester and the approver as its approver, and is sent by the poller; a retry under the key finds it, and
// its outcome is read back by the key.
func TestAgentRefund(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: refundSucceeds})
	r := agentRefund(o, o.Credits/2, "op-1")
	quote := w.quoted(o, r)
	refund, err := w.requestAgentRefund(requestAgent, r)
	if err != nil || refund.Credits != quote.Credits || refund.Amount != quote.Amount || !refund.Approved || !refund.ByAgent || refund.RequestedBy != r.Agent || *refund.ApprovedBy != r.Approver {
		t.Fatalf("the request: %+v %v", refund, err)
	}
	if again, err := w.requestAgentRefund(requestAgent, r); err != nil || again.ID != refund.ID {
		t.Errorf("the retry: %+v %v", again, err)
	}
	if got := w.count(w.p.platform, `SELECT count(*) FROM platform.audit_log WHERE (action = 'refund.request' AND actor_kind = 'operations_agent')
		OR (action = 'refund.approve' AND actor_kind = 'user')`); got != 2 {
		t.Errorf("%d of the request's and the approval's audit entries", got)
	}
	if _, err := w.requestAgentRefund(requestAgent, agentRefund(o, 1, "op-2")); !errors.Is(err, ErrRefundInFlight) {
		t.Errorf("a second refund while the first is in flight: %v", err)
	}
	w.dueRefunds()
	w.expectAgentRefundsOf(o)
}

// quoted checks whose order o is and quotes r of it, which must leave no refund and no audit entry.
func (w *world) quoted(o Order, r AgentRefund) Refund {
	t := w.t
	t.Helper()
	var owner OrderOwner
	if err := w.tx(w.p.platform, func(tx pg.Tx) (err error) {
		owner, err = OrderOwnerTx(t.Context(), tx, o.ID)
		return err
	}); err != nil || owner.Site != w.siteA || owner.Account != w.account {
		t.Fatalf("the order's owner: %+v %v", owner, err)
	}
	quote, err := w.requestAgentRefund(quoteAgent, r)
	if err != nil || quote.Credits != r.Credits || quote.Amount.Minor <= 0 {
		t.Fatalf("the quote: %+v %v", quote, err)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.refunds") + w.count(w.p.platform, "SELECT count(*) FROM platform.audit_log WHERE action LIKE 'refund.%'"); n != 0 {
		t.Fatalf("the quote left %d rows", n)
	}
	return quote
}

// expectAgentRefundsOf checks what AgentRefundOf reads of o after the agent's refund under op-1 succeeded: it, nothing
// under a key without a refund, and nothing for a user's refund.
func (w *world) expectAgentRefundsOf(o Order) {
	t := w.t
	t.Helper()
	if got, found, err := w.p.AgentRefundOf(t.Context(), o.ID, "op-1"); err != nil || !found || got.State != "succeeded" {
		t.Errorf("the outcome: %+v %v %v", got, found, err)
	}
	if _, found, err := w.p.AgentRefundOf(t.Context(), o.ID, "op-2"); err != nil || found {
		t.Errorf("a key without a refund: %v %v", found, err)
	}
	user := w.mustRefund(o, o.Credits/4, "user")
	if _, found, err := w.p.AgentRefundOf(t.Context(), o.ID, "user"); err != nil || found {
		t.Errorf("a user's refund %s read as the agent's: %v %v", user.ID.TypeID(), found, err)
	}
}

// TestAgentRefundRefusals: the payments rules of an administrator's channel refund refuse the agent's too, the quote
// and the request alike, with nothing written.
func TestAgentRefundRefusals(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: refundSucceeds})
	for _, request := range []agentRequest{quoteAgent, requestAgent} {
		if _, err := w.requestAgentRefund(request, agentRefund(o, o.Credits+1, "more")); !errors.Is(err, ErrRefundAmount) {
			t.Errorf("more than the order's credits: %v", err)
		}
		if _, err := w.requestAgentRefund(request, agentRefund(Order{ID: OrderID(uuid.NewV7())}, 1, "none")); !errors.Is(err, ErrNotFound) {
			t.Errorf("no such order: %v", err)
		}
	}
	w.exec("UPDATE payments.merchants SET refund_enabled = false")
	if _, err := w.requestAgentRefund(quoteAgent, agentRefund(o, 1, "off")); !errors.Is(err, ErrRefundForbidden) {
		t.Errorf("a merchant that does not refund: %v", err)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.refunds"); n != 0 {
		t.Errorf("the refusals left %d refunds", n)
	}
}

// TestAgentRefundAboveTheLimit: above the site's refund approval limit the approver is only the first of two, and the
// refund waits in the review for a second administrator, unsent.
func TestAgentRefundAboveTheLimit(t *testing.T) {
	w, o := refundWorldAmount(t, fakePlugin{caps: CapRefund, refund: refundSucceeds}, 150*yuan)
	r := agentRefund(o, o.Credits, "op-1")
	refund, err := w.requestAgentRefund(requestAgent, r)
	if err != nil || refund.Approved || refund.FirstApprovedBy == nil || *refund.FirstApprovedBy != r.Approver {
		t.Fatalf("the request: %+v %v", refund, err)
	}
	w.dueRefunds()
	if got := w.mustGetRefund(refund.ID); got.State != "frozen" {
		t.Errorf("sent before the second approval: %+v", got)
	}
}

// TestAgentRefundPrepares: preparing advances the ledger of the order's account in transactions of its own, before
// the quote's and the request's, which hold the balance's lock; a quote alone keeps nothing it replayed, as its
// savepoint rolls the replay back with the freeze. An order that does not exist is left for the request to refuse.
func TestAgentRefundPrepares(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: refundSucceeds})
	allocated := func() int {
		return w.count(w.p.platform, "SELECT allocated_seq FROM billing.balances WHERE account_id = $1", o.AccountID)
	}
	before := allocated()
	r := agentRefund(o, o.Credits/2, "op-1")
	if _, err := w.requestAgentRefund(quoteAgent, r); err != nil {
		t.Fatal(err)
	}
	if got := allocated(); got != before {
		t.Errorf("a quote kept an advance of the ledger: %d, was %d", got, before)
	}
	if err := w.p.PrepareAgentRefund(t.Context(), o.ID); err != nil {
		t.Fatal(err)
	}
	prepared := allocated()
	if prepared <= before {
		t.Fatalf("preparing did not advance the ledger: %d, was %d", prepared, before)
	}
	if refund, err := w.requestAgentRefund(requestAgent, r); err != nil || refund.Credits != r.Credits {
		t.Errorf("the request after preparing: %+v %v", refund, err)
	}
	if err := w.p.PrepareAgentRefund(t.Context(), OrderID(uuid.NewV7())); err != nil {
		t.Errorf("preparing an order that does not exist: %v", err)
	}
}
