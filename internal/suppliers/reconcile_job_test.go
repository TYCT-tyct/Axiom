package suppliers

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform/pg"
)

func (w *world) reconciliation(logs *bytes.Buffer) *reconcileJob {
	return &reconcileJob{
		deps:   ReconcileJobConfig{DB: w.db, Scope: platformScope, Logger: slog.New(slog.NewTextHandler(logs, nil)), Clock: w.clock.now},
		policy: DefaultReconcileConfig(),
	}
}

func (w *world) runReconciliation(r *reconcileJob) {
	w.t.Helper()
	if err := r.work(w.t.Context(), &river.Job[reconcileArgs]{}); err != nil {
		w.t.Fatal(err)
	}
}

// The real worker writes one revision, emits the right signal and changes the
// routing input only for a complete estimate below the supplier's actual cost.
func TestReconcileJobJudgesOnlyCompleteCostsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, unit, actual, verdict, alert string
		flag                               CostFlag
	}{
		{"quota boundary", "quota", "23.725", "within", "", CostComplete},
		{"usd boundary", "usd", "22.7250000001", "within", "", CostComplete},
		{"quota under", "quota", "23.7250000001", "under", "under_estimated", CostComplete},
		{"usd under", "usd", "22.7250000002", "under", "under_estimated", CostComplete},
		{"quota over", "quota", "21", "over", "over_estimated", CostComplete},
		{"usd over", "usd", "22", "over", "over_estimated", CostComplete},
		{"unpriced", "quota", "400", "unjudged", "", CostUnpriced},
		{"no usage", "quota", "400", "unjudged", "", CostNoUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, 1)
			w.tx(func(ctx context.Context, tx pg.Tx) error {
				_, err := tx.Exec(ctx, "UPDATE suppliers.suppliers SET native_unit = $1 WHERE id = $2", tc.unit, w.supplier)
				return err
			})
			w.addValue("2")
			a := w.attempt(newRequest(), 1, newAPIRef)
			a.Cost.Flag = tc.flag
			w.record(a)
			w.ingest(w.charge(newAPIRef, tc.actual, w.clock.now()))
			var logs bytes.Buffer
			r := w.reconciliation(&logs)
			for range 2 {
				w.runReconciliation(r)
				w.checkReconciled(tc.verdict, tc.alert, logs.String())
			}
		})
	}
}

func (w *world) checkReconciled(verdict, alert, logs string) {
	w.t.Helper()
	w.wantCharge(newAPIRef, chargeState{State: "matched", Attribution: "attempt", Revision: 2, Attempt: true}, 1)
	if n := w.count("SELECT count(*) FROM suppliers.charge_events WHERE payload->>'verdict' = $1", verdict); n != 1 {
		w.t.Fatalf("events with verdict %s = %d", verdict, n)
	}
	if err := w.svc.Reload(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	routes, err := w.svc.Routable()
	if err != nil {
		w.t.Fatal(err)
	}
	if routed := len(routes) > 0; routed != (verdict != "under") {
		w.t.Fatalf("verdict %s: routable = %v", verdict, routed)
	}
	if alert == "" {
		if logs != "" {
			w.t.Fatalf("unexpected alert: %s", logs)
		}
		return
	}
	if strings.Count(logs, "suppliers.reconcile."+alert) != 1 {
		w.t.Fatalf("want one %s alert: %s", alert, logs)
	}
	if verdict == "under" && (!strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, w.endpoint.String())) {
		w.t.Fatalf("delisting alert lacks severity or endpoint: %s", logs)
	}
}

func (w *world) wantCharge(ref string, want chargeState, events int) {
	w.t.Helper()
	if got := w.chargeOf(ref); got != want {
		w.t.Fatalf("%s: charge = %+v, want %+v", ref, got, want)
	}
	if got := w.events(ref); got != events {
		w.t.Fatalf("%s: %d events, want %d", ref, got, events)
	}
}

// Delayed runs use the execution time for grace. Held charges remain unassigned
// until a person decides; a late real attempt supersedes that decision once.
func TestReconcileJobHoldsLostChargesWithoutGuessingTheirOwner(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	lost := newRequest()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return RecordLostRequest(ctx, tx, lost, siteA, accountA, []uuid.UUID{w.endpoint})
	})
	w.ingest(w.charge(newAPIRef, "23", w.clock.now()))
	w.clock.advance(time.Hour)
	w.ingest(w.charge("recent", "23", w.clock.now()))
	var logs bytes.Buffer
	r := w.reconciliation(&logs)
	for range 2 {
		w.runReconciliation(r)
		w.wantCharge(newAPIRef, chargeState{State: "held", Revision: 2}, 1)
		w.wantCharge("recent", chargeState{State: "new", Revision: 1}, 0)
	}
	if strings.Count(logs.String(), "suppliers.reconcile.held") != 1 {
		t.Fatalf("held alert: %s", logs.String())
	}
	if !w.assignToLost(newAPIRef, lost) || w.assignToLost(newAPIRef, lost) {
		t.Fatal("manual assignment must apply exactly once")
	}
	w.record(w.attempt(lost, 1, newAPIRef))
	for range 2 {
		w.runReconciliation(r)
		w.wantCharge(newAPIRef, chargeState{State: "matched", Attribution: "attempt", Revision: 4, Attempt: true}, 3)
	}
}

func TestClosedUnmatchedFindingDoesNotEmitAgain(t *testing.T) {
	w := newWorld(t, 1)
	w.record(w.attempt(newRequest(), 1, newAPIRef))
	w.clock.advance(time.Hour)
	var logs bytes.Buffer
	r := w.reconciliation(&logs)
	w.runReconciliation(r)
	if got := consumeSupplierSignals(w); len(got) != 1 || got[0].Key != "unmatched" {
		t.Fatalf("first overdue attempt: %+v", got)
	}
	// Once that incident is closed, another scan of the same still-pending row cannot reopen it.
	w.runReconciliation(r)
	if got := consumeSupplierSignals(w); len(got) != 0 {
		t.Fatalf("closed finding was emitted again: %+v", got)
	}
	// A later commit may carry an older finished_at; a time watermark must not hide it.
	late := w.attempt(newRequest(), 1, "late")
	late.FinishedAt = w.clock.now().Add(-2 * time.Hour)
	w.record(late)
	w.runReconciliation(r)
	if got := consumeSupplierSignals(w); len(got) != 1 || got[0].Fields["value"] != "1" {
		t.Fatalf("late overdue attempt was skipped: %+v", got)
	}
}
