package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// writer has the owner make a credential that may change the catalog and withdraw its own proposals.
func (f catalogFixture) writer() (id, token string) {
	f.t.Helper()
	return f.makeCredential(f.s, `{"name":"hermes","permissions":["catalog.view","catalog.manage","operations.write"],
		"platform":true,"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"run the catalog"}`)
}

// setStatusAs asks, as the agent, for a model's status as of rev under key.
func (f catalogFixture) setStatusAs(token, model, status string, rev int64, key, extra string) *httptest.ResponseRecorder {
	f.t.Helper()
	body := `{"status":"` + status + `","rev":` + strconv.FormatInt(rev, 10) + `,"reason":"its sales stopped"` + extra + `}`
	return f.agentSend(token, http.MethodPut, "/models/"+model+"/status", body, idempotent(key), header("X-Axiom-Run-ID", "run-7"))
}

// agentOperation reads an answer of the operations API as an operation.
func agentOperation(t *testing.T, rec *httptest.ResponseRecorder) operationsapi.Operation {
	t.Helper()
	var op operationsapi.Operation
	decode(t, rec, &op)
	return op
}

// listedAndAsked puts a model on sale and has the agent ask to retire it, which waits for the owner.
func (f catalogFixture) listedAndAsked(token, slug string) (adminapi.Model, operationsapi.Operation) {
	f.t.Helper()
	rec := f.setStatus(f.ready(slug), "listed")
	var listed adminapi.Model
	decode(f.t, rec, &listed)
	rec = f.setStatusAs(token, listed.Id, "retired", listed.Rev, newKey(), "")
	expectAdmin(f.t, "asking to retire "+slug, rec, http.StatusAccepted, "")
	return listed, agentOperation(f.t, rec)
}

// decide is the owner's decision on an approval.
func (f catalogFixture) decide(approval, verb string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.change(f.s, http.MethodPost, "/operations/approvals/"+approval+"/"+verb, `{"reason":"checked the sales"}`, newKey())
}

// TestOperationsAPIWrites: the agent lists a draft model at once; a retry under the key finds the operation as it is
// now, the key with another request is refused, a preview and a stale revision write nothing; the write is in the log
// with its reason apart from its input and the operation it made.
func TestOperationsAPIWrites(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	_, token := f.writer()
	draft := f.ready("acme/m1")
	key := newKey()
	rec := f.setStatusAs(token, draft.Id, "listed", draft.Rev, key, "")
	expectAdmin(t, "listing", rec, http.StatusOK, "")
	listed := agentOperation(t, rec)
	if listed.State != operationsapi.OperationStateSucceeded || listed.Approval != nil || !strings.Contains(rec.Body.String(), `"status":"listed"`) {
		t.Errorf("the listing: %s", rec.Body)
	}
	rec = f.setStatusAs(token, draft.Id, "listed", draft.Rev, key, "")
	if again := agentOperation(t, rec); rec.Header().Get("Idempotency-Replayed") != "true" || *again.OperationId != *listed.OperationId {
		t.Errorf("the retry: %d %s", rec.Code, rec.Body)
	}
	expectAdmin(t, "the key with another request", f.setStatusAs(token, draft.Id, "retired", draft.Rev, key, ""), http.StatusConflict, codeIdempotencyReuse)

	model := f.send(http.MethodGet, "/models/"+draft.Id, "", http.StatusOK)
	rec = f.setStatusAs(token, model.Id, "retired", model.Rev, newKey(), `,"dry_run":true`)
	if preview := agentOperation(t, rec); rec.Code != http.StatusOK || !preview.DryRun || preview.OperationId != nil || preview.State != operationsapi.OperationStatePendingApproval {
		t.Errorf("the preview: %d %s", rec.Code, rec.Body)
	}
	expectAdmin(t, "a stale revision", f.setStatusAs(token, model.Id, "retired", model.Rev-1, newKey(), ""), http.StatusConflict, codeStaleRevision)
	if got := f.text(`SELECT count(*)::text FROM operations.operations`); got != "1" {
		t.Errorf("%s operations after the preview and the stale one", got)
	}
	expectWriteLog(t, f.agent(token, because("/log?limit=20", "replay")), *listed.OperationId)
}

// TestOperationsAPIApproval: retiring waits for the owner, who sees what the server read apart from the agent's reason,
// and approves it; the change runs as the agent's in that transaction, once.
func TestOperationsAPIApproval(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	_, token := f.writer()
	model, asked := f.listedAndAsked(token, "acme/m1")
	if asked.State != operationsapi.OperationStatePendingApproval || asked.Approval == nil || asked.NextCheckAfterSeconds == nil {
		t.Fatalf("the proposal: %+v", asked)
	}
	if got := f.send(http.MethodGet, "/models/"+model.Id, "", http.StatusOK); got.Status != adminapi.ModelStatusListed {
		t.Errorf("the model is %s before the approval", got.Status)
	}
	f.expectOwnerSees(asked.Approval.ApprovalId, "acme/m1")

	rec := f.decide(asked.Approval.ApprovalId, "approve")
	var approved adminapi.OperationsApproval
	decode(t, rec, &approved)
	if rec.Code != http.StatusOK || approved.State != adminapi.OperationsApprovalStateApproved || approved.OperationState != adminapi.OperationStateSucceeded {
		t.Errorf("the approval: %s", rec.Body)
	}
	if got := f.send(http.MethodGet, "/models/"+model.Id, "", http.StatusOK); got.Status != adminapi.ModelStatusRetired {
		t.Errorf("the model is %s after the approval", got.Status)
	}
	expectAdmin(t, "approving twice", f.decide(asked.Approval.ApprovalId, "approve"), http.StatusConflict, codeConflict)
	f.expectAudit("approval.approve", map[string]string{"actor_kind": "user", "reason": "checked the sales"})
	f.expectAudit("model.retire", map[string]string{"actor_kind": "operations_agent"})
	if got := f.text(`SELECT string_agg(action, ',' ORDER BY at, id) FROM platform.audit_log WHERE actor_kind = 'operations_agent'`); got != "approval.request,model.retire,operation.succeed" {
		t.Errorf("the agent's audit: %s", got)
	}
	expectOperation(t, f.agent(token, because("/operations/"+*asked.OperationId, "did it run")), "succeeded", "approved")
}

// expectOwnerSees checks the owner's queue: the pending approval, with what the server read of the model apart from
// the agent's reason and run.
func (f catalogFixture) expectOwnerSees(approval, slug string) {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/operations/approvals?state=pending", "", as(f.s))
	var queue adminapi.OperationsApprovalList
	decode(f.t, rec, &queue)
	if len(queue.Data) != 1 {
		f.t.Fatalf("the owner's queue: %s", rec.Body)
	}
	if a := queue.Data[0]; a.Id != approval || a.AgentReason != "its sales stopped" || !strings.Contains(string(a.Summary), `"slug":"`+slug+`"`) || deref(a.RunId) != "run-7" {
		f.t.Errorf("the owner sees %s", rec.Body)
	}
}

// expectWriteLog checks the log TestOperationsAPIWrites leaves for its first write: the reason from the body, the rest
// of the body as its input, and the operation it made.
func expectWriteLog(t *testing.T, rec *httptest.ResponseRecorder, operation string) {
	t.Helper()
	var log operationsapi.OperationLogList
	decode(t, rec, &log)
	last := log.Data[len(log.Data)-1]
	input := recordedInput(t, last)
	body, _ := input["body"].(map[string]any)
	if last.Tool != "model_set_status" || last.Reason != "its sales stopped" || body["status"] != "listed" || body["reason"] != nil ||
		last.OperationId == nil || *last.OperationId != operation {
		t.Errorf("the first write is recorded as %+v with %v", last, input)
	}
}

// TestOperationsApprovalOutcomes: a rejection, an approval of a model changed since the agent asked, a withdrawal, the
// emergency stop and a revocation each end the operation without changing the model; a credential without the
// permission is refused, and a model not on sale cannot be asked to retire.
func TestOperationsApprovalOutcomes(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	_, token := f.writer()
	read := func(id string) adminapi.Model { return f.send(http.MethodGet, "/models/"+id, "", http.StatusOK) }

	rejected, asked := f.listedAndAsked(token, "acme/r1")
	expectAdmin(t, "rejectOperationsApproval", f.decide(asked.Approval.ApprovalId, "reject"), http.StatusOK, "")
	expectOperation(t, f.agent(token, because("/approvals/"+asked.Approval.ApprovalId, "x")), "rejected", "")

	changed, asked := f.listedAndAsked(token, "acme/r2")
	f.send(http.MethodPut, "/models/"+changed.Id+"/spec", `{"rev":`+strconv.FormatInt(changed.Rev, 10)+`,"spec":`+specBody+`,"reason":"a longer context"}`, http.StatusOK)
	rec := f.decide(asked.Approval.ApprovalId, "approve")
	var late adminapi.OperationsApproval
	decode(t, rec, &late)
	if rec.Code != http.StatusOK || late.OperationState != adminapi.OperationStateRejected || deref(late.ErrorCode) != string(codeStaleRevision) {
		t.Errorf("approving a changed model: %d %s", rec.Code, rec.Body)
	}

	withdrawn, asked := f.listedAndAsked(token, "acme/r3")
	key := newKey()
	withdraw := func(key string) *httptest.ResponseRecorder {
		return f.agentSend(token, http.MethodPost, "/approvals/"+asked.Approval.ApprovalId+"/withdraw", `{"reason":"not needed"}`, idempotent(key))
	}
	expectOperation(t, withdraw(key), "canceled", "")
	if rec := withdraw(key); rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("the withdrawal's retry: %s %s", rec.Header(), rec.Body)
	}
	expectAdmin(t, "withdrawing again", withdraw(newKey()), http.StatusConflict, codeConflict)
	expectAdmin(t, "approving a withdrawn one", f.decide(asked.Approval.ApprovalId, "approve"), http.StatusConflict, codeConflict)

	_, reader := f.makeCredential(f.s, `{"name":"reader","permissions":["catalog.view"],"platform":true,"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	draft := f.ready("acme/r6")
	expectAdmin(t, "a write without its permission", f.setStatusAs(reader, draft.Id, "listed", draft.Rev, newKey(), ""), http.StatusForbidden, codeForbidden)
	expectAdmin(t, "a withdrawal without operations.write", f.agentSend(reader, http.MethodPost, "/approvals/"+asked.Approval.ApprovalId+"/withdraw", `{"reason":"r"}`, idempotent(newKey())), http.StatusForbidden, codeForbidden)
	expectAdmin(t, "another credential's approval", f.agent(reader, because("/approvals/"+asked.Approval.ApprovalId, "x")), http.StatusNotFound, codeNotFound)
	expectAdmin(t, "retiring a draft", f.setStatusAs(token, draft.Id, "retired", draft.Rev, newKey(), ""), http.StatusConflict, codeConflict)

	stopped, _ := f.listedAndAsked(token, "acme/r4")
	rec = f.change(f.s, http.MethodPost, "/operations/agent/stop", `{"reason":"pause"}`, newKey())
	var stop adminapi.OperationsAgentStopped
	decode(t, rec, &stop)
	if stop.Revoked != 2 || stop.Canceled == nil || *stop.Canceled != 1 {
		t.Errorf("the stop: %s", rec.Body)
	}
	revoking, again := f.writer()
	revoked, _ := f.listedAndAsked(again, "acme/r5")
	expectAdmin(t, "revokeOperationsCredential", f.change(f.s, http.MethodPost, "/operations/credentials/"+revoking+"/revoke", `{"reason":"leaked"}`, newKey()), http.StatusOK, "")
	for _, m := range []adminapi.Model{rejected, changed, withdrawn, stopped, revoked} {
		if got := read(m.Id); got.Status != adminapi.ModelStatusListed {
			t.Errorf("%s is %s", m.Slug, got.Status)
		}
	}
	if got := f.text(`SELECT string_agg(a.state || ':' || o.state, ',' ORDER BY a.id) FROM operations.approvals a JOIN operations.operations o ON o.id = a.operation_id`); got != "rejected:rejected,approved:rejected,canceled:canceled,canceled:canceled,canceled:canceled" {
		t.Errorf("the approvals are %s", got)
	}
}

// expectOperation checks an answer is the operation in state, with its approval in approval when that is not empty.
func expectOperation(t *testing.T, rec *httptest.ResponseRecorder, state, approval string) {
	t.Helper()
	op := agentOperation(t, rec)
	if rec.Code != http.StatusOK || string(op.State) != state || approval != "" && string(op.Approval.State) != approval {
		t.Errorf("want %s: %d %s", state, rec.Code, rec.Body)
	}
}

// TestOperationsApprovalAfterAnOldNodesStop: a node from before approvals stops the agent by revoking its credentials
// only, so a proposal stays pending; approving it then runs nothing and cancels it, and the decision's time is the
// one the owner reads back.
func TestOperationsApprovalAfterAnOldNodesStop(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	_, token := f.writer()
	model, asked := f.listedAndAsked(token, "acme/o1")
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := f.svc.StopAgents(t.Context(), tx, audit.System("test.old_node"), "stop on an old node")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rec := f.decide(asked.Approval.ApprovalId, "approve")
	var canceled adminapi.OperationsApproval
	decode(t, rec, &canceled)
	if rec.Code != http.StatusOK || canceled.State != adminapi.OperationsApprovalStateCanceled || canceled.OperationState != adminapi.OperationStateCanceled || canceled.DecidedAt == nil {
		t.Errorf("approving a stopped credential's proposal: %d %s", rec.Code, rec.Body)
	}
	if got := f.send(http.MethodGet, "/models/"+model.Id, "", http.StatusOK); got.Status != adminapi.ModelStatusListed {
		t.Errorf("the model is %s", got.Status)
	}
	var read adminapi.OperationsApproval
	decode(t, f.do(http.MethodGet, "/operations/approvals/"+asked.Approval.ApprovalId, "", as(f.s)), &read)
	if read.DecidedAt == nil || !read.DecidedAt.Equal(*canceled.DecidedAt) {
		t.Errorf("the decision's time: answered %v, read %v", canceled.DecidedAt, read.DecidedAt)
	}
}

// TestOperationsWithdrawalPreview: a previewed withdrawal runs the checks and leaves the proposal, its audit and its
// key untouched; the withdrawal then answers with the times the operation has, on its retry too.
func TestOperationsWithdrawalPreview(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	_, token := f.writer()
	_, asked := f.listedAndAsked(token, "acme/w1")
	withdraw := func(key, extra string) *httptest.ResponseRecorder {
		return f.agentSend(token, http.MethodPost, "/approvals/"+asked.Approval.ApprovalId+"/withdraw", `{"reason":"checking the impact"`+extra+`}`, idempotent(key))
	}
	key := newKey()
	rec := withdraw(key, `,"dry_run":true`)
	if preview := agentOperation(t, rec); rec.Code != http.StatusOK || !preview.DryRun || preview.State != operationsapi.OperationStateCanceled || preview.OperationId != nil {
		t.Errorf("the preview: %d %s", rec.Code, rec.Body)
	}
	if got := f.text(`SELECT a.state || ':' || o.state || ':' || (SELECT count(*) FROM platform.audit_log WHERE action = 'operation.cancel') || ':' ||
		(SELECT count(*) FROM platform.idempotency WHERE key = '` + key + `') FROM operations.approvals a JOIN operations.operations o ON o.id = a.operation_id`); got != "pending:pending_approval:0:0" {
		t.Errorf("after the preview: %s", got)
	}

	withdrawn := agentOperation(t, withdraw(key, ""))
	replayed := agentOperation(t, withdraw(key, ""))
	read := agentOperation(t, f.agent(token, because("/operations/"+*asked.OperationId, "x")))
	for what, op := range map[string]operationsapi.Operation{"the withdrawal": withdrawn, "its retry": replayed} {
		if op.State != operationsapi.OperationStateCanceled || !op.UpdatedAt.Equal(*read.UpdatedAt) || !op.UpdatedAt.After(*asked.UpdatedAt) ||
			op.Approval.DecidedAt == nil || !op.Approval.DecidedAt.Equal(*read.Approval.DecidedAt) {
			t.Errorf("%s answers %+v, the operation reads %+v", what, op, read)
		}
	}
}
