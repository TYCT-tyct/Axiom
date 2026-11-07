package main

import (
	"context"

	"axiom/internal/operations"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// GetOperationsAgentLimits reads the limits on the operations agent as they apply now.
func (a adminAPI) GetOperationsAgentLimits(ctx context.Context, _ adminapi.GetOperationsAgentLimitsRequestObject) (adminapi.GetOperationsAgentLimitsResponseObject, error) {
	var l operations.AgentLimits
	err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		l, err = operations.ReadAgentLimits(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.GetOperationsAgentLimits200JSONResponse(agentLimitsBody(l)), nil
}

// SetOperationsAgentLimits lowers the limits on the operations agent from the revision the owner read.
func (a adminAPI) SetOperationsAgentLimits(ctx context.Context, r adminapi.SetOperationsAgentLimitsRequestObject) (adminapi.SetOperationsAgentLimitsResponseObject, error) {
	b := r.Body
	body, replayed, err := idempotently(ctx, a.db, "setOperationsAgentLimits", r.Params.IdempotencyKey, r, func(tx pg.Tx) (adminapi.OperationsAgentLimits, error) {
		set, err := operations.SetAgentLimits(ctx, tx, actor(ctx), operations.AgentLimits{AlertsPerHour: b.AlertsPerHour}, b.Rev, b.Reason)
		return agentLimitsBody(set), err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetOperationsAgentLimits200JSONResponse{Body: body, Headers: adminapi.SetOperationsAgentLimits200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func agentLimitsBody(l operations.AgentLimits) adminapi.OperationsAgentLimits {
	return adminapi.OperationsAgentLimits{
		AlertsPerHour: &l.AlertsPerHour, AlertsPerHourMax: new(operations.MaxAlertsPerHour), Rev: &l.Rev, UpdatedAt: l.Updated,
	}
}
