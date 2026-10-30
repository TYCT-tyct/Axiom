package suppliers

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers/internal/store"
)

// TestChargeEventProducersCannotCommitOutOfOrder: producers and the consumer share one transaction lock. The second
// producer cannot pass the lock until the first commits, so it cannot commit an event whose created_at is older than
// a cursor the consumer has already advanced past.
func TestChargeEventProducersCannotCommitOutOfOrder(t *testing.T) {
	database := pgtest.New(t, Module())
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	q := store.New()
	first := make(chan struct{})
	unlock := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
			if err := q.LockChargeEvents(t.Context(), tx); err != nil {
				return err
			}
			close(first)
			<-unlock
			return nil
		})
	}()
	select {
	case <-first:
	case err := <-firstDone:
		t.Fatalf("the first producer did not take the lock: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the first producer did not take the lock")
	}
	second := make(chan struct {
		got bool
		err error
	}, 1)
	go func() {
		var got bool
		err := db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock(hashtext('axiom charge events'))`).Scan(&got)
		})
		second <- struct {
			got bool
			err error
		}{got, err}
	}()
	select {
	case res := <-second:
		if res.err != nil {
			t.Fatalf("the second producer: %v", res.err)
		}
		if res.got {
			t.Fatal("the second producer took the lock before the first committed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second producer's lock attempt did not return")
	}
	close(unlock)
	if err := <-firstDone; err != nil {
		t.Fatalf("the first producer: %v", err)
	}
}

// TestChargeEventTimeFollowsTheLock: created_at must be read at insertion, after the lock, not at BEGIN. Otherwise a
// producer that began before the consumer and waited for the lock could still insert an event older than the cursor.
func TestChargeEventTimeFollowsTheLock(t *testing.T) {
	database := pgtest.New(t, Module())
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := addSupplier(t, db, "event-clock", "newapi", "active"); err != nil {
		t.Fatal(err)
	}
	var supplier uuid.UUID
	if err := db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM suppliers.suppliers WHERE name = 'event-clock'`).Scan(&supplier)
	}); err != nil {
		t.Fatal(err)
	}
	q := store.New()
	var begun, at time.Time
	if err := db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT now()`).Scan(&begun); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `SELECT pg_sleep(0.2)`); err != nil {
			return err
		}
		var charge uuid.UUID
		if err := tx.QueryRow(t.Context(), `INSERT INTO suppliers.charge_records
			(supplier_id, charge_ref, occurred_at, amount_native, usage, funding, upstream_model, state)
			VALUES ($1, 'event-clock-ref', now(), 1, '{}'::jsonb, 'wallet', 'm', 'new') RETURNING id`, supplier).Scan(&charge); err != nil {
			return err
		}
		if err := q.LockChargeEvents(t.Context(), tx); err != nil {
			return err
		}
		if err := q.InsertChargeEvent(t.Context(), tx, store.InsertChargeEventParams{
			ChargeRecordID: charge, Revision: 1, Payload: []byte(`{}`),
		}); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT created_at FROM suppliers.charge_events WHERE charge_record_id = $1`, charge).Scan(&at)
	}); err != nil {
		t.Fatal(err)
	}
	if at.Sub(begun) < 100*time.Millisecond {
		t.Errorf("event time %v is only %v after BEGIN; want the insertion's clock", at, at.Sub(begun))
	}
}
