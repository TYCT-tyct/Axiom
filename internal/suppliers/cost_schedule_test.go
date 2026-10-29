package suppliers

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

func TestALateChargeOnlyDelistsItsOwnCostSchedule(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.record(w.attempt(newRequest(), 1, newAPIRef))
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		q := store.New()
		id, err := q.InsertCostSchedule(ctx, tx, store.InsertCostScheduleParams{
			SupplierID: w.supplier, EndpointID: w.endpoint, NativeUnit: "quota",
			Schedule: []byte(`{"v":1,"currency":"XXX","rates":{"input":"0.15","output":"0.6"}}`),
			Basis:    "published", Fingerprint: "corrected",
		})
		if err != nil {
			return err
		}
		return q.SetEndpointCost(ctx, tx, store.SetEndpointCostParams{ID: w.endpoint, CostScheduleID: &id})
	})
	w.ingest(w.charge(newAPIRef, "45", w.clock.now()))
	var logs bytes.Buffer
	r := w.reconciliation(&logs)
	for range 2 {
		w.runReconciliation(r)
	}
	if strings.Count(logs.String(), "level=WARN msg=suppliers.reconcile.under_estimated_unchanged") != 1 || strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), w.chargeID(newAPIRef).String()) {
		t.Errorf("want one warning for the old charge and no delisting log: %s", logs.String())
	}
	same(t, "under-estimation signal", []alert.Signal{{Kind: "suppliers.reconciliation_difference", Key: "under_estimated_unchanged", Severity: "warning", Fields: map[string]string{"value": "1"}}}, consumeSupplierSignals(w))
	known := w.count("SELECT count(*) FROM suppliers.endpoints WHERE id = $1 AND cost_state = 'known'", w.endpoint) == 1
	if !known {
		t.Errorf("endpoint known %v", known)
	}
	if n := w.count("SELECT count(*) FROM suppliers.charge_events WHERE payload->>'verdict' = 'under' AND payload->>'cost_schedule_id' = $1 AND (payload->>'endpoint_delisted')::boolean = $2", w.sc.String(), false); n != 1 {
		t.Errorf("under verdict events %d, want 1", n)
	}
}

func TestUnderestimatedChargesWarnWhenTheEndpointIsAlreadyUnknown(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.record(w.attempt(newRequest(), 1, newAPIRef))
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.endpoints SET cost_state = 'unknown', cost_state_reason = 'under_estimated' WHERE id = $1", w.endpoint)
		return err
	})
	w.ingest(w.charge(newAPIRef, "45", w.clock.now()))
	var logs bytes.Buffer
	r := w.reconciliation(&logs)
	for range 2 {
		w.runReconciliation(r)
	}
	if strings.Count(logs.String(), "level=WARN msg=suppliers.reconcile.under_estimated_unchanged") != 1 || strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("want one warning and no new delisting log: %s", logs.String())
	}
	same(t, "under-estimation signal", []alert.Signal{{Kind: "suppliers.reconciliation_difference", Key: "under_estimated_unchanged", Severity: "warning", Fields: map[string]string{"value": "1"}}}, consumeSupplierSignals(w))
}
