package suppliers

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/syncer"
)

func setSyncAlertState(w *world, version int64, state syncer.State) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		raw, err := jsonx.MarshalCanonical(state)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO suppliers.sync_states(supplier_id,version,state,next_due) VALUES($1,$2,$3,'infinity') ON CONFLICT(supplier_id) DO UPDATE SET version=EXCLUDED.version,state=EXCLUDED.state`, w.supplier, version, raw)
		return err
	})
}

func consumeSupplierSignals(w *world) []alert.Signal {
	w.t.Helper()
	var signals []alert.Signal
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return alert.Consume(ctx, tx, func(s alert.Signal) error { signals = append(signals, s); return nil })
	})
	return signals
}

func TestSyncAlertStormCheckpointRollbackAndRecovery(t *testing.T) {
	w := newWorld(t, 2)
	setSyncAlertState(w, 0, syncer.State{})
	forward := func() {
		t.Helper()
		w.tx(func(ctx context.Context, tx pg.Tx) error { return forwardSyncAlerts(ctx, tx, w.supplier) })
	}
	forward()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO suppliers.sync_events(supplier_id,version,seq,kind,subject,old_value,new_value,why,created_at) SELECT $1,1,n,'balance_low','','','sensitive upstream text','', $2 FROM generate_series(0,999) n`, w.supplier, w.clock.now())
		return err
	})
	setSyncAlertState(w, 1, syncer.State{LowNotified: true})
	rollback := errors.New("rollback")
	err := w.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		if err := forwardSyncAlerts(t.Context(), tx, w.supplier); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if got := consumeSupplierSignals(w); len(got) != 0 {
		t.Fatal("rolled-back bridge leaked signals")
	}
	forward()
	signals := consumeSupplierSignals(w)
	if len(signals) != 1000 {
		t.Fatalf("got %d signals, want journal page", len(signals))
	}
	assertSanitizedSupplierStorm(t, w.supplier, signals)
	forward()
	if len(consumeSupplierSignals(w)) != 0 {
		t.Fatal("checkpoint replayed storm")
	}
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO suppliers.sync_events(supplier_id,version,seq,kind,subject,old_value,new_value,why) VALUES($1,2,0,'balance_recovered','','','','')`, w.supplier)
		return err
	})
	setSyncAlertState(w, 2, syncer.State{})
	forward()
	forward()
	signals = consumeSupplierSignals(w)
	if len(signals) != 1 || !signals[0].Resolved || signals[0].Key != w.supplier.String() {
		t.Fatalf("recovery: %+v", signals)
	}
}

func TestSyncAlertsStartAtCurrentStateAndRequireSuccessfulRecovery(t *testing.T) {
	w := newWorld(t, 2)
	at := w.clock.now()
	state := syncer.State{Status: syncer.NeedsAttention, Reason: "the supplier rejected the account credentials"}
	state.Tasks[syncer.TaskKeys].Halted = true
	setSyncAlertState(w, 1, state)
	forward := func() {
		t.Helper()
		w.tx(func(ctx context.Context, tx pg.Tx) error { return forwardSyncAlerts(ctx, tx, w.supplier) })
	}
	forward()
	signals := consumeSupplierSignals(w)
	if len(signals) != 2 {
		t.Fatalf("current faults: %+v", signals)
	}
	state.Status = syncer.Normal
	state.Tasks[syncer.TaskKeys].Halted = false
	setSyncAlertState(w, 2, state)
	forward()
	if got := consumeSupplierSignals(w); len(got) != 0 {
		t.Fatalf("credential replacement claimed recovery: %+v", got)
	}
	state.Tasks[syncer.TaskBalance].LastOK = at.Add(time.Minute)
	state.Tasks[syncer.TaskKeys].LastOK = at.Add(time.Minute)
	setSyncAlertState(w, 3, state)
	forward()
	forward()
	signals = consumeSupplierSignals(w)
	if len(signals) != 2 || !signals[0].Resolved || !signals[1].Resolved {
		t.Fatalf("successful recovery: %+v", signals)
	}
}

func TestSyncAlertBootstrapSkipsRecoveredHistory(t *testing.T) {
	w := newWorld(t, 2)
	setSyncAlertState(w, 1, syncer.State{})
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO suppliers.sync_events(supplier_id,version,seq,kind,subject,old_value,new_value,why) VALUES($1,1,0,'balance_low','','','',''),($1,1,1,'balance_recovered','','','','')`, w.supplier)
		return err
	})
	w.tx(func(ctx context.Context, tx pg.Tx) error { return forwardSyncAlerts(ctx, tx, w.supplier) })
	if got := consumeSupplierSignals(w); len(got) != 0 {
		t.Fatalf("replayed history: %+v", got)
	}
}

func TestSupplierAvailabilityIncludesCredentialExpiryAndRecovery(t *testing.T) {
	w := newWorld(t, 2)
	sample := func() alert.Signal {
		t.Helper()
		w.tx(func(ctx context.Context, tx pg.Tx) error { return emitSupplierAvailability(ctx, tx, w.clock.now()) })
		signals := consumeSupplierSignals(w)
		if len(signals) != 2 {
			t.Fatalf("signals: %+v", signals)
		}
		if signals[0].Kind != "suppliers.sources_unavailable" || signals[0].Resolved != signals[1].Resolved {
			t.Fatalf("per-supplier availability: %+v", signals)
		}
		return signals[1]
	}
	if s := sample(); !s.Resolved {
		t.Fatal("healthy supplier exhausted")
	}
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE suppliers.credentials SET expires_at=$2 WHERE id=$1`, w.credential, w.clock.now().Add(-time.Second))
		return err
	})
	if s := sample(); s.Kind != "suppliers.exhausted" || s.Resolved {
		t.Fatalf("missing exhaustion: %+v", s)
	}
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE suppliers.credentials SET expires_at=NULL WHERE id=$1`, w.credential)
		return err
	})
	if s := sample(); !s.Resolved {
		t.Fatal("no availability recovery")
	}
}

func TestSupplierReconciliationPublishesSanitizedCategories(t *testing.T) {
	w := newWorld(t, 2)
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return persistReconcileAlerts(ctx, tx, ReconcileReport{Delisted: []uuid.UUID{uuid.NewV7()}, Over: []uuid.UUID{uuid.NewV7()}, Held: []uuid.UUID{uuid.NewV7()}, Violations: []string{"sensitive details"}})
	})
	signals := consumeSupplierSignals(w)
	if len(signals) != 4 {
		t.Fatalf("categories: %+v", signals)
	}
	for _, s := range signals {
		if s.Kind != "suppliers.reconciliation_difference" || len(s.Fields) != 1 || s.Fields["value"] != "1" {
			t.Fatalf("unexpected details: %+v", s)
		}
	}
}

func assertSanitizedSupplierStorm(t *testing.T, supplier uuid.UUID, signals []alert.Signal) {
	t.Helper()
	for _, s := range signals {
		if s.Kind != "suppliers.low_balance" || s.Key != supplier.String() || s.Resolved || len(s.Fields) != 0 {
			t.Fatalf("unsanitized signal: %+v", s)
		}
	}
}

func TestSyncRecoveryUsesObservationsAcrossClockSkew(t *testing.T) {
	w := newWorld(t, 2)
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	state := syncer.State{}
	state.Tasks[syncer.TaskBalance].LastOK = at
	state.Tasks[syncer.TaskKeys].LastOK = at
	setSyncAlertState(w, 0, state)
	forward := func() {
		t.Helper()
		w.tx(func(ctx context.Context, tx pg.Tx) error { return forwardSyncAlerts(ctx, tx, w.supplier) })
	}
	forward()
	state.Status = syncer.NeedsAttention
	state.Tasks[syncer.TaskKeys].Halted = true
	setSyncAlertState(w, 1, state)
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO suppliers.sync_events(supplier_id,version,seq,kind,subject,old_value,new_value,why,created_at) VALUES
 ($1,1,0,'needs_attention','',$2,'','the supplier rejected the account credentials','2000-01-01'),
 ($1,1,1,'task_halted','keys',$2,'','','2000-01-01')`, w.supplier, at.Format(time.RFC3339Nano))
		return err
	})
	forward()
	if signals := consumeSupplierSignals(w); len(signals) != 2 {
		t.Fatalf("faults: %+v", signals)
	}
	state.Status = syncer.Normal
	state.Tasks[syncer.TaskKeys].Halted = false
	setSyncAlertState(w, 2, state)
	forward()
	if signals := consumeSupplierSignals(w); len(signals) != 0 {
		t.Fatalf("clock skew turned a reset into recovery: %+v", signals)
	}
	state.Tasks[syncer.TaskBalance].LastOK = at.Add(-time.Hour)
	state.Tasks[syncer.TaskKeys].LastOK = at.Add(-time.Hour)
	setSyncAlertState(w, 3, state)
	forward()
	signals := consumeSupplierSignals(w)
	if len(signals) != 2 || !signals[0].Resolved || !signals[1].Resolved {
		t.Fatalf("clock rollback hid genuine recovery: %+v", signals)
	}
}
