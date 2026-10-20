package suppliers

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestReloadedLimitsKeepExistingUsage(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 2)
	w.perMinute(w.credential, 2)
	first, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.credentials SET max_concurrency = 1, requests_per_minute = 1 WHERE id = $1", w.credential)
		return err
	})
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	first.Done()
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("lowered limits admitted a call with one still in flight: %v", err)
	}
	second.Done()
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("lowered limits forgot the minute's usage: %v", err)
	}
	w.perMinute(w.credential, 3)
	third, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("raised RPM with two already used: %v", err)
	}
	third.Done()
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("raising RPM reset the minute: %v", err)
	}
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.credentials SET max_concurrency = 3, requests_per_minute = NULL WHERE id = $1", w.credential)
		return err
	})
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	var held []*Attempt
	for range 3 {
		a, err := w.svc.Acquire(w.endpoint)
		if err != nil {
			t.Fatalf("cleared RPM and raised concurrency: %v", err)
		}
		held = append(held, a)
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("raised concurrency admitted a fourth call: %v", err)
	}
	for _, a := range held {
		a.Done()
	}
}

func TestEndpointsShareTheCredentialsMinute(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO suppliers.endpoints
			(id, supplier_id, supplier_group_id, model_id, upstream_name, formats, cost_state, cost_schedule_id)
			SELECT $1, supplier_id, supplier_group_id, $2, 'other', formats, cost_state, cost_schedule_id
			FROM suppliers.endpoints WHERE id = $3`, uuid.NewV7(), uuid.NewV7(), w.endpoint)
		return err
	})
	w.perMinute(w.credential, 1)
	first, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	first.Done()
	for id := range w.svc.snap.Load().endpoints {
		if _, err := w.svc.Acquire(id); !errors.Is(err, ErrNoSlot) {
			t.Fatalf("endpoint %s has an independent minute: %v", id, err)
		}
	}
}
