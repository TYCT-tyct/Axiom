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

// SetOperationsAgentLimits lowers the limits on the operations agent from the revision the owner read. A limit left out
// keeps what that revision holds: SetAgentLimits refuses any other revision, so what this reads is what it replaces.
func (a adminAPI) SetOperationsAgentLimits(ctx context.Context, r adminapi.SetOperationsAgentLimitsRequestObject) (adminapi.SetOperationsAgentLimitsResponseObject, error) {
	b := r.Body
	body, replayed, err := idempotently(ctx, a.db, "setOperationsAgentLimits", r.Params.IdempotencyKey, r, func(tx pg.Tx) (adminapi.OperationsAgentLimits, error) {
		l, err := operations.ReadAgentLimits(ctx, tx)
		if err != nil {
			return adminapi.OperationsAgentLimits{}, err
		}
		l.AlertsPerHour = b.AlertsPerHour
		for _, given := range []struct {
			to   *int
			from *int
		}{{&l.RequestsPerMinute, b.RequestsPerMinute}, {&l.MoneyWritesPerMinute, b.MoneyWritesPerMinute}, {&l.MoneyWritesPerHour, b.MoneyWritesPerHour}} {
			if given.from != nil {
				*given.to = *given.from
			}
		}
		set, err := operations.SetAgentLimits(ctx, tx, actor(ctx), l, b.Rev, b.Reason)
		return agentLimitsBody(set), err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetOperationsAgentLimits200JSONResponse{Body: body, Headers: adminapi.SetOperationsAgentLimits200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func agentLimitsBody(l operations.AgentLimits) adminapi.OperationsAgentLimits {
	return adminapi.OperationsAgentLimits{
		RequestsPerMinute: &l.RequestsPerMinute, RequestsPerMinuteMax: new(operations.MaxRequestsPerMinute),
		MoneyWritesPerMinute: &l.MoneyWritesPerMinute, MoneyWritesPerMinuteMax: new(operations.MaxMoneyWritesPerMinute),
		MoneyWritesPerHour: &l.MoneyWritesPerHour, MoneyWritesPerHourMax: new(operations.MaxMoneyWritesPerHour),
		AlertsPerHour: &l.AlertsPerHour, AlertsPerHourMax: new(operations.MaxAlertsPerHour), Rev: &l.Rev, UpdatedAt: l.Updated,
	}
}
