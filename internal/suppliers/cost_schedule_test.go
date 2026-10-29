package suppliers

import (
	"bytes"
	"context"
	"testing"

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
	report := w.reconcile(w.clock.now())
	if report.Matched != 1 || len(report.Delisted) != 0 {
		t.Fatalf("report %+v", report)
	}
	var logs bytes.Buffer
	w.reconciliation(&logs).report(t.Context(), report)
	if logs.Len() != 0 {
		t.Errorf("an old schedule emitted an alert: %s", logs.String())
	}
	known := w.count("SELECT count(*) FROM suppliers.endpoints WHERE id = $1 AND cost_state = 'known'", w.endpoint) == 1
	if !known {
		t.Errorf("endpoint known %v", known)
	}
	if n := w.count("SELECT count(*) FROM suppliers.charge_events WHERE payload->>'verdict' = 'under' AND payload->>'cost_schedule_id' = $1 AND (payload->>'endpoint_delisted')::boolean = $2", w.sc.String(), false); n != 1 {
		t.Errorf("under verdict events %d, want 1", n)
	}
}
