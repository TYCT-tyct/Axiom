package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/operations"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/idem"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// The operations API's writes (docs/domains/operations.md「运营接口」「风险分级与审批」): each is an operation of
// internal/operations under the credential's Idempotency-Key. One that needs nobody runs in the transaction that
// proposes it; one that needs the owner runs in the transaction of the owner's approval (ApproveOperationsApproval).
// Either way the change runs as the agent's, through the same module function the admin calls.

// recheckSeconds is when an agent waiting on the owner is told to read the operation again.
const recheckSeconds = 60

// ModelSetStatus lists a draft model at once, or asks the owner to approve retiring a listed one.
func (o operationsAPI) ModelSetStatus(ctx context.Context, r operationsapi.ModelSetStatusRequestObject) (operationsapi.ModelSetStatusResponseObject, error) {
	key, err := keyOf(r.Params.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	id, err := modelIDOf(r.ModelId)
	if err != nil {
		return nil, err
	}
	c, b := callOf(ctx), r.Body
	p := operations.Proposal{
		Credential: uuid.UUID(c.agent.ID()), Key: key, Tool: "model_set_status", Target: operations.Target{Kind: "model", ID: r.ModelId},
		Params: map[string]any{"status": string(b.Status), "rev": b.Rev}, Risk: operations.RiskRoutine, Reason: b.Reason,
	}
	if r.Params.XAxiomRunID != nil {
		p.Run = *r.Params.XAxiomRunID
	}
	if b.Status == operationsapi.ModelStatusChangeStatusRetired {
		p.Risk = operations.RiskDelisting
	}
	var op operations.Operation
	var replayed bool
	if err := o.db.Tx(ctx, c.scope, func(tx pg.Tx) (err error) {
		op, replayed, err = o.setModelStatus(ctx, tx, id, p, b)
		return err
	}); err != nil {
		return nil, err
	}
	o.admin.refreshCatalog(ctx, replayed || op.State != operations.StateSucceeded)
	headers := operationsapi.ModelSetStatus200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}
	if op.State == operations.StatePending && op.ID != (uuid.UUID{}) {
		return operationsapi.ModelSetStatus202JSONResponse{Body: operationBody(op), Headers: operationsapi.ModelSetStatus202ResponseHeaders(headers)}, nil
	}
	return operationsapi.ModelSetStatus200JSONResponse{Body: operationBody(op), Headers: headers}, nil
}

// setModelStatus proposes p in tx after reading the model: retiring waits for the owner with what the server read of
// it; listing runs at once. A preview writes nothing.
func (o operationsAPI) setModelStatus(ctx context.Context, tx pg.Tx, id catalog.ModelID, p operations.Proposal, b *operationsapi.ModelStatusChange) (operations.Operation, bool, error) {
	model, err := o.admin.readModelIn(ctx, tx, id)
	if err != nil {
		return operations.Operation{}, false, err
	}
	retiring := p.Risk == operations.RiskDelisting
	if retiring {
		p.Summary = map[string]any{"slug": model.Slug, "status": string(model.Status), "rev": model.Rev}
	}
	if b.DryRun != nil && *b.DryRun {
		return preview(p), false, refuseRetiring(model, b.Rev, retiring)
	}
	op, replayed, err := o.propose(ctx, tx, p)
	if err != nil || replayed {
		return op, replayed, err
	}
	if err := refuseRetiring(model, b.Rev, retiring); err != nil {
		return operations.Operation{}, false, err
	}
	if op.State != operations.StateApproved {
		return op, false, nil
	}
	result, err := o.admin.runOperation(ctx, tx, op)
	if err != nil {
		return operations.Operation{}, false, err
	}
	op, err = operations.Settle(ctx, tx, op, result, "")
	return op, false, err
}

// refuseRetiring refuses at once what the catalog would refuse when the change runs: a request as of a revision that is
// no longer the model's, and retiring a model that is not on sale, which would otherwise wait for the owner only to be
// refused when approved. Listing runs at once, so the catalog's own review gate answers it.
func refuseRetiring(model adminapi.Model, rev int64, retiring bool) error {
	switch {
	case model.Rev != rev:
		return &apiError{status: http.StatusConflict, code: codeStaleRevision, message: "the model changed since that revision was read"}
	case retiring && model.Status != adminapi.ModelStatusListed:
		return &apiError{status: http.StatusConflict, code: codeConflict, message: "only a model on sale can be retired"}
	}
	return nil
}

// propose records p with the credential held, so a revocation or the emergency stop either refuses it or cancels it.
func (o operationsAPI) propose(ctx context.Context, tx pg.Tx, p operations.Proposal) (operations.Operation, bool, error) {
	if err := o.admin.accounts.HoldAgent(ctx, tx, accounts.AgentID(p.Credential)); err != nil {
		return operations.Operation{}, false, err
	}
	return operations.Propose(ctx, tx, p)
}

// preview is where p would start, without writing anything. The target's change is not run, so a preview that would
// start approved can still be refused when it runs.
func preview(p operations.Proposal) operations.Operation {
	op := operations.Operation{Tool: p.Tool, Target: p.Target, Risk: p.Risk, State: operations.StateApproved}
	if p.Summary != nil {
		op.State = operations.StatePending
	}
	return op
}

// runOperation makes the change op asks for in tx, as the agent's, and answers what it left. A top-up proposal changes
// nothing yet: its reservation, rechecked here, holds until the agent confirms the money. A sale price is published and
// counted in the day's envelope. A credit is given and its reservation committed. An endpoint is switched. An alert
// is raised or resolved.
func (a adminAPI) runOperation(ctx context.Context, tx pg.Tx, op operations.Operation) (map[string]any, error) {
	switch op.Tool {
	case "model_set_status":
		return a.runModelStatus(ctx, tx, op)
	case "price_publish":
		return a.runPricePublish(ctx, tx, op)
	case "credit_grant":
		return a.runCreditGrant(ctx, tx, op)
	case "endpoint_set_enabled":
		return a.runEndpointEnabled(ctx, tx, op)
	case "alert_raise", "alert_resolve":
		return runAlert(ctx, tx, op)
	case "supplier_top_up_propose":
		r, err := operations.CheckReservation(ctx, tx, op)
		if err != nil {
			return nil, err
		}
		return moneyResult(r)
	}
	return nil, fmt.Errorf("no way to run %s", op.Tool) // every tool that proposes is listed here, so this is a bug
}

// runModelStatus lists or retires the model of op. The catalog takes the operation's ID as its own key, so the change
// is applied once for an operation.
func (a adminAPI) runModelStatus(ctx context.Context, tx pg.Tx, op operations.Operation) (map[string]any, error) {
	var params struct {
		Status string `json:"status"`
		Rev    int64  `json:"rev"`
	}
	if err := jsonx.Unmarshal(op.Params, &params); err != nil {
		return nil, fmt.Errorf("the parameters of operation %s: %w", op.ID, err)
	}
	id, err := modelIDOf(op.Target.ID)
	if err != nil {
		return nil, err
	}
	change := catalog.Op{Actor: audit.OperationsAgent(op.Credential), Key: op.ID, Reason: op.Reason, Tx: &tx}
	r := catalog.ModelRequest{Op: change, Model: id, Rev: params.Rev}
	if params.Status == string(adminapi.ModelStatusRetired) {
		_, err = a.catalog.Retire(ctx, r)
	} else {
		_, err = a.catalog.ListModel(ctx, r)
	}
	if err != nil {
		return nil, err
	}
	model, err := a.readModelIn(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": string(model.Status), "rev": model.Rev}, nil
}

// runApproved runs op, which the owner has just approved in tx: it succeeds, or ends rejected with the code of the
// target's refusal when the target changed since the agent asked; the refused change is undone to a savepoint, so the
// decision and the operation's end are still committed. Any other failure fails the approval.
func (a adminAPI) runApproved(ctx context.Context, tx pg.Tx, op operations.Operation) (operations.Operation, error) {
	if _, err := tx.Exec(ctx, "SAVEPOINT operation_run"); err != nil {
		return op, fmt.Errorf("run operation %s: %w", op.ID, err)
	}
	result, err := a.runOperation(ctx, tx, op)
	if refused := failure(err); err != nil && refused != nil && refused.status < http.StatusInternalServerError {
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT operation_run"); err != nil {
			return op, fmt.Errorf("undo operation %s: %w", op.ID, err)
		}
		return operations.Settle(ctx, tx, op, nil, string(refused.code))
	}
	if err != nil {
		return op, err
	}
	return operations.Settle(ctx, tx, op, result, "")
}

// errPreviewed rolls back the transaction of a previewed write, which ran to its end so that every check ran.
var errPreviewed = errors.New("previewed")

// previewWithdrawal runs the withdrawal of approval id in a transaction it then rolls back: the checks are the
// withdrawal's own, and nothing it wrote stays, the audit entry and the signal included. Its key is not claimed.
func (o operationsAPI) previewWithdrawal(ctx context.Context, credential, id uuid.UUID, reason string) (operationsapi.ApprovalWithdrawResponseObject, error) {
	var op operations.Operation
	err := o.db.Tx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		if op, err = operations.Withdraw(ctx, tx, credential, id, reason); err != nil {
			return err
		}
		return errPreviewed
	})
	if !errors.Is(err, errPreviewed) {
		return nil, err
	}
	body := operationBody(operations.Operation{Tool: op.Tool, Target: op.Target, Risk: op.Risk, State: op.State})
	return operationsapi.ApprovalWithdraw200JSONResponse{Body: body}, nil
}

// operationBody is an operation as the operations contract answers it.
func operationBody(op operations.Operation) operationsapi.Operation {
	body := operationsapi.Operation{
		Tool: op.Tool, Target: operationsapi.OperationTarget{Kind: op.Target.Kind, Id: op.Target.ID},
		Risk: operationsapi.OperationRisk(op.Risk), State: operationsapi.OperationState(op.State),
		DryRun: op.ID == (uuid.UUID{}),
	}
	body.ErrorCode = optionalText(op.Error)
	if body.DryRun {
		return body
	}
	body.OperationId, body.CreatedAt, body.UpdatedAt = new(op.ID.String()), &op.Created, &op.Updated
	if op.Result != nil {
		body.Result = new(jsonx.Raw(op.Result))
	}
	if op.State == operations.StatePending {
		body.NextCheckAfterSeconds = new(recheckSeconds)
	}
	if a := op.Approval; a != nil {
		body.Approval = &operationsapi.OperationApproval{
			ApprovalId: a.ID.String(), State: operationsapi.OperationsApprovalState(a.State),
			Tier: operationsapi.OperationsApprovalTier("human"), ExpiresAt: a.Expires, DecidedAt: a.Decided,
		}
	}
	return body
}

// OperationsList is a page of the credential's own operations.
func (o operationsAPI) OperationsList(ctx context.Context, r operationsapi.OperationsListRequestObject) (operationsapi.OperationsListResponseObject, error) {
	after, err := cursorID(r.Params.Cursor)
	if err != nil {
		return nil, err
	}
	var state operations.State
	if r.Params.State != nil {
		state = operations.State(*r.Params.State)
	}
	c := callOf(ctx)
	var page []operations.Operation
	var more bool
	if err := o.db.ReadTx(ctx, c.scope, func(tx pg.Tx) (err error) {
		page, more, err = operations.Operations(ctx, tx, uuid.UUID(c.agent.ID()), state, after, pageSize(r.Params.Limit))
		return err
	}); err != nil {
		return nil, err
	}
	list := operationsapi.OperationList{Data: make([]operationsapi.Operation, len(page))}
	for i, op := range page {
		list.Data[i] = operationBody(op)
	}
	if more {
		list.NextCursor = new(page[len(page)-1].ID.String())
	}
	return operationsapi.OperationsList200JSONResponse(list), nil
}

// cursorID reads a cursor that is the ID of the last item of the previous page.
func cursorID(cursor *string) (uuid.UUID, error) {
	if cursor == nil {
		return uuid.UUID{}, nil
	}
	id, err := uuid.Parse(*cursor)
	if err != nil {
		return uuid.UUID{}, invalidField("cursor", "not a cursor this API gave out")
	}
	return id, nil
}

// OperationGet reads one of the credential's operations.
func (o operationsAPI) OperationGet(ctx context.Context, r operationsapi.OperationGetRequestObject) (operationsapi.OperationGetResponseObject, error) {
	op, err := o.ownOperation(ctx, func(tx pg.Tx) (operations.Operation, error) {
		id, err := uuid.Parse(r.OperationId)
		if err != nil {
			return operations.Operation{}, operations.ErrNoOperation
		}
		return operations.Get(ctx, tx, id)
	})
	if err != nil {
		return nil, err
	}
	return operationsapi.OperationGet200JSONResponse(operationBody(op)), nil
}

// ApprovalGet reads the credential's operation that waits or waited on an approval.
func (o operationsAPI) ApprovalGet(ctx context.Context, r operationsapi.ApprovalGetRequestObject) (operationsapi.ApprovalGetResponseObject, error) {
	op, err := o.ownOperation(ctx, func(tx pg.Tx) (operations.Operation, error) {
		id, err := uuid.Parse(r.ApprovalId)
		if err != nil {
			return operations.Operation{}, operations.ErrNoApproval
		}
		return operations.ApprovalOperation(ctx, tx, id)
	})
	if err != nil {
		return nil, err
	}
	return operationsapi.ApprovalGet200JSONResponse(operationBody(op)), nil
}

// ownOperation reads an operation, which is not found unless it is the credential's own.
func (o operationsAPI) ownOperation(ctx context.Context, read func(tx pg.Tx) (operations.Operation, error)) (operations.Operation, error) {
	c := callOf(ctx)
	var op operations.Operation
	if err := o.db.ReadTx(ctx, c.scope, func(tx pg.Tx) (err error) {
		op, err = read(tx)
		return err
	}); err != nil {
		return operations.Operation{}, err
	}
	if op.Credential != uuid.UUID(c.agent.ID()) {
		return operations.Operation{}, operations.ErrNoOperation
	}
	return op, nil
}

// ApprovalWithdraw takes back one of the credential's own pending proposals.
func (o operationsAPI) ApprovalWithdraw(ctx context.Context, r operationsapi.ApprovalWithdrawRequestObject) (operationsapi.ApprovalWithdrawResponseObject, error) {
	key, err := keyOf(r.Params.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(r.ApprovalId)
	if err != nil {
		return nil, operations.ErrNoApproval
	}
	credential := uuid.UUID(callOf(ctx).agent.ID())
	if r.Body.DryRun != nil && *r.Body.DryRun {
		return o.previewWithdrawal(ctx, credential, id, r.Body.Reason)
	}
	fingerprint, err := idem.Fingerprint(r.ApprovalId, r.Body)
	if err != nil {
		return nil, err
	}
	claim := idem.Claim{Key: key, Actor: audit.OperationsAgent(credential), Operation: "approvalWithdraw", Fingerprint: fingerprint}
	body, replayed, err := idempotentlyClaimed(ctx, o.db, claim, func(tx pg.Tx) (operationsapi.Operation, error) {
		op, err := operations.Withdraw(ctx, tx, credential, id, r.Body.Reason)
		if err != nil {
			return operationsapi.Operation{}, err
		}
		return operationBody(op), nil
	})
	if err != nil {
		return nil, err
	}
	return operationsapi.ApprovalWithdraw200JSONResponse{Body: body, Headers: operationsapi.ApprovalWithdraw200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}
