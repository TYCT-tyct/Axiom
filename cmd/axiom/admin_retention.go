package main

import (
	"context"

	"axiom/internal/observability"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// GetRetention returns the call log's hot window: the saved one, or the code default before the first save.
func (a adminAPI) GetRetention(ctx context.Context, _ adminapi.GetRetentionRequestObject) (adminapi.GetRetentionResponseObject, error) {
	var retention observability.Retention
	err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		retention, err = observability.GetRetention(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return adminJSON[adminapi.GetRetention200JSONResponse](retention)
}

// SetRetention validates and saves the window, and audits it with the reason the operator gave.
func (a adminAPI) SetRetention(ctx context.Context, r adminapi.SetRetentionRequestObject) (adminapi.SetRetentionResponseObject, error) {
	body, replayed, err := idempotently(ctx, a.db, "setRetention", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.Retention, error) {
		retention := observability.Retention{HotDays: r.Body.HotDays}
		if err := observability.SetRetention(ctx, tx, retention); err != nil {
			return adminapi.Retention{}, err
		}
		if err := audit.Record(ctx, tx, audit.Event{
			Actor:  actor(ctx),
			Action: "retention.update",
			Target: audit.Target{Module: "observability", Kind: "retention", ID: "call-log"},
			Reason: r.Body.Reason,
			Detail: map[string]any{"hot_days": retention.HotDays},
		}); err != nil {
			return adminapi.Retention{}, err
		}
		return adminJSON[adminapi.Retention](retention)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetRetention201JSONResponse{Body: body, Headers: adminapi.SetRetention201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}
