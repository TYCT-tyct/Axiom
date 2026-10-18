package main

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

// TestTheCallPathIsWhatBillingWritesSuppliersThrough: the adapter cmd/ hands billing is the suppliers module's own
// functions, so what billing writes in its cleanup transaction lands in suppliers' tables: the reservation goes back,
// the request is recorded as lost with the route the reservation froze — both entries of it, in the order the call would
// have tried them — and its end is written too, with no attempt's cost row, since nothing settled it. The same
// transaction is one commit, whichever module's rows it holds (docs/architecture.md 规则 5).
func TestTheCallPathIsWhatBillingWritesSuppliersThrough(t *testing.T) {
	f := newChainFixtureWith(t, billing.Config{
		HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond,
	}, callPath{})
	ctx := t.Context()
	request, first, second := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	route := []billing.Quotable{
		{Price: f.schedule, Model: billing.Model{ContextLength: 1e12}, MaxOutput: 100, Endpoint: first},
		{Price: f.schedule, Model: billing.Model{ContextLength: 1e12}, MaxOutput: 100, Endpoint: second},
	}
	admitted := billing.Admitted{Scope: f.scope, Site: f.site, Account: f.account, Workspace: f.workspace, Key: f.key}
	if err := f.app.Tx(ctx, f.scope, func(tx pg.Tx) error {
		_, _, err := f.billing.Reserve(ctx, tx, admitted, request, billing.Demand{TextBytes: 1000}, route)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond) // past the reservation's deadline
	if n, err := f.billing.Cleanup(ctx, 10); err != nil || n != 1 {
		t.Fatalf("Cleanup = %d, %v; want the one reservation", n, err)
	}
	var state string
	var frozen, lost, ended bool
	var attempts int
	if err := f.app.Tx(ctx, f.platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(ctx, `SELECT r.state, r.endpoints = ARRAY[$2, $3]::uuid[],
			(SELECT coalesce(route_endpoint_ids = ARRAY[$2, $3]::uuid[], false)
				FROM suppliers.lost_requests WHERE request_id = $1),
			EXISTS (SELECT 1 FROM suppliers.ended_requests WHERE request_id = $1),
			(SELECT count(*) FROM suppliers.attempt_costs WHERE request_id = $1)
			FROM billing.reservations r WHERE r.request_id = $1`, request, first, second).
			Scan(&state, &frozen, &lost, &ended, &attempts)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "cleanup_released" || !frozen || !lost || !ended || attempts != 0 {
		t.Errorf("the reservation is %s, froze its route %v, lost route %v, ended %v, %d cost rows; "+
			"want cleanup_released, both endpoints in order, ended, none", state, frozen, lost, ended, attempts)
	}
}
