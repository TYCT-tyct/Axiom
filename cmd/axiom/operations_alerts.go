package main

import (
	"context"
	"fmt"

	"axiom/internal/observability"
	"axiom/internal/operations"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// maxAlertSummary is the most bytes an alert's summary may take: a signal's field holds 512 bytes of UTF-8, and the
// contract can only count characters.
const maxAlertSummary = 512

// AlertsList is a page of the platform's alert incidents, newest first, the unresolved only when asked.
func (o operationsAPI) AlertsList(ctx context.Context, r operationsapi.AlertsListRequestObject) (operationsapi.AlertsListResponseObject, error) {
	before, err := cursorID(r.Params.Cursor)
	if err != nil {
		return nil, err
	}
	size := pageSize(r.Params.Limit)
	page := observability.AlertEventPage{Before: before, Limit: size + 1, Open: r.Params.Open != nil && *r.Params.Open}
	var events []observability.AlertEvent
	if err := o.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		events, err = observability.ListAlertEvents(ctx, tx, page)
		return err
	}); err != nil {
		return nil, err
	}
	list := map[string]any{"data": events}
	if len(events) > size {
		list["data"], list["next_cursor"] = events[:size], events[size-1].ID.String()
	}
	return adminJSON[operationsapi.AlertsList200JSONResponse](list)
}

// AlertRaise raises the agent's alert under its key at once: a routine write, within the alert limit the owner set.
func (o operationsAPI) AlertRaise(ctx context.Context, r operationsapi.AlertRaiseRequestObject) (operationsapi.AlertRaiseResponseObject, error) {
	b := r.Body
	if len(b.Summary) > maxAlertSummary {
		return nil, invalidField("/summary", fmt.Sprintf("is more than %d bytes of UTF-8", maxAlertSummary))
	}
	var limits operations.AgentLimits
	if err := o.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		limits, err = operations.ReadAgentLimits(ctx, tx)
		return err
	}); err != nil {
		return nil, err
	}
	if err := o.admin.accounts.SpendAgentAlert(callOf(ctx).agent, limits.AlertsPerHour); err != nil {
		return nil, err
	}
	params := map[string]any{"severity": string(b.Severity), "summary": b.Summary}
	p := operations.Proposal{Tool: "alert_raise", Target: operations.Target{Kind: "alert", ID: b.Key}, Params: params, Reason: b.Reason}
	op, replayed, err := o.runRoutine(ctx, r.Params.IdempotencyKey, r.Params.XAxiomRunID, p, b.DryRun)
	if err != nil {
		return nil, err
	}
	return operationsapi.AlertRaise200JSONResponse{Body: operationBody(op), Headers: operationsapi.AlertRaise200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// AlertResolve resolves the agent's alert under its key at once.
func (o operationsAPI) AlertResolve(ctx context.Context, r operationsapi.AlertResolveRequestObject) (operationsapi.AlertResolveResponseObject, error) {
	b := r.Body
	p := operations.Proposal{Tool: "alert_resolve", Target: operations.Target{Kind: "alert", ID: b.Key}, Params: map[string]any{}, Reason: b.Reason}
	op, replayed, err := o.runRoutine(ctx, r.Params.IdempotencyKey, r.Params.XAxiomRunID, p, b.DryRun)
	if err != nil {
		return nil, err
	}
	return operationsapi.AlertResolve200JSONResponse{Body: operationBody(op), Headers: operationsapi.AlertResolve200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// runAlert raises or resolves the credential's alert op names, and answers the incident's kind and key as alerts_list
// shows them.
func runAlert(ctx context.Context, tx pg.Tx, op operations.Operation) (map[string]any, error) {
	if op.Tool == "alert_resolve" {
		if err := operations.ResolveAlert(ctx, tx, op); err != nil {
			return nil, err
		}
		return map[string]any{"kind": operations.AgentAlert, "key": operations.AlertKey(op), "resolved": true}, nil
	}
	var params struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
	}
	if err := jsonx.Unmarshal(op.Params, &params); err != nil {
		return nil, fmt.Errorf("the parameters of operation %s: %w", op.ID, err)
	}
	if err := operations.RaiseAlert(ctx, tx, op, params.Severity, params.Summary); err != nil {
		return nil, err
	}
	return map[string]any{"kind": operations.AgentAlert, "key": operations.AlertKey(op), "resolved": false}, nil
}
