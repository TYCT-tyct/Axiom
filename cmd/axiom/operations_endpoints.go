package main

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// EndpointSetEnabled turns one of a supplier's endpoints on or off for routing, at once: a routine write the agent
// makes on what it saw in a patrol, which the owner can turn back in the admin (docs/domains/operations.md「运营接口」).
func (o operationsAPI) EndpointSetEnabled(ctx context.Context, r operationsapi.EndpointSetEnabledRequestObject) (operationsapi.EndpointSetEnabledResponseObject, error) {
	if _, _, err := endpointIDsOf(r.SupplierId, r.EndpointId); err != nil {
		return nil, err
	}
	b := r.Body
	p := operations.Proposal{
		Tool: "endpoint_set_enabled", Target: operations.Target{Kind: "endpoint", ID: r.EndpointId},
		Params: map[string]any{"supplier_id": r.SupplierId, "enabled": b.Enabled}, Reason: b.Reason,
	}
	op, replayed, err := o.runRoutine(ctx, r.Params.IdempotencyKey, r.Params.XAxiomRunID, p, b.DryRun)
	if err != nil {
		return nil, err
	}
	return operationsapi.EndpointSetEnabled200JSONResponse{Body: operationBody(op), Headers: operationsapi.EndpointSetEnabled200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// runRoutine proposes p, a routine write of the credential under the request's key and run, with the credential held,
// and runs it in the same transaction as the agent's; a retry under its key finds the operation as it is now.
func (o operationsAPI) runRoutine(ctx context.Context, idempotencyKey string, run *string, p operations.Proposal, dryRun *bool) (operations.Operation, bool, error) {
	key, err := keyOf(idempotencyKey)
	if err != nil {
		return operations.Operation{}, false, err
	}
	p.Credential, p.Key, p.Risk, p.Run = uuid.UUID(callOf(ctx).agent.ID()), key, operations.RiskRoutine, runOf(run)
	return o.runWrite(ctx, dryRun, func(tx pg.Tx) (operations.Operation, bool, error) {
		op, replayed, err := o.propose(ctx, tx, p)
		if err != nil || replayed {
			return op, replayed, err
		}
		result, err := o.admin.runOperation(ctx, tx, op)
		if err != nil {
			return operations.Operation{}, false, err
		}
		op, err = operations.Settle(ctx, tx, op, result, "")
		return op, false, err
	})
}

// runEndpointEnabled switches the endpoint of op as the agent's, and answers the switch as it now is.
func (a adminAPI) runEndpointEnabled(ctx context.Context, tx pg.Tx, op operations.Operation) (map[string]any, error) {
	var params struct {
		Supplier string `json:"supplier_id"`
		Enabled  bool   `json:"enabled"`
	}
	if err := jsonx.Unmarshal(op.Params, &params); err != nil {
		return nil, fmt.Errorf("the parameters of operation %s: %w", op.ID, err)
	}
	supplier, endpoint, err := endpointIDsOf(params.Supplier, op.Target.ID)
	if err != nil {
		return nil, err
	}
	e, err := a.suppliers.SetEndpointEnabled(ctx, tx, audit.OperationsAgent(op.Credential), supplier, endpoint, params.Enabled, op.Reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"supplier_id": params.Supplier, "enabled": e.Enabled}, nil
}
