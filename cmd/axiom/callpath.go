package main

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// callPath is billing.Suppliers as the suppliers module implements it: the writes billing makes in the transaction of
// its own settlement and cleanup, which is how a call's supplier side commits exactly when the ledger's cost does
// (docs/architecture.md 规则 5). billing names suppliers' types, so this is all that joins the two: cmd/ is the only
// package that may hold both, and neither module knows the other's wiring.
type callPath struct{}

var _ billing.Suppliers = callPath{}

func (callPath) RecordAttempts(ctx context.Context, tx pg.Tx, attempts []suppliers.AttemptRecord) error {
	return suppliers.RecordAttempts(ctx, tx, attempts)
}

func (callPath) RecordLostRequest(ctx context.Context, tx pg.Tx, request, site, account uuid.UUID, route []uuid.UUID) error {
	return suppliers.RecordLostRequest(ctx, tx, platform.RequestID(request), site, account, route)
}

func (callPath) ResolveLostRequest(ctx context.Context, tx pg.Tx, request uuid.UUID) error {
	return suppliers.ResolveLostRequest(ctx, tx, platform.RequestID(request))
}

func (callPath) EndRequest(ctx context.Context, tx pg.Tx, request, site, account uuid.UUID, at time.Time) error {
	return suppliers.EndRequest(ctx, tx, platform.RequestID(request), site, account, at)
}
