package suppliers

import (
	"context"
	"errors"
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

func TestCredentialModelsAndExpiryUseTheSnapshot(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	other := w.anotherKey(4)
	expires := w.clock.now().Add(time.Second)
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE suppliers.credentials SET allowed_models = ARRAY['catalog-name'] WHERE id = $1", w.credential); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE suppliers.credentials SET allowed_models = ARRAY['gpt'], expires_at = $2 WHERE id = $1", other, expires)
		return err
	})
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Removing permission in storage cannot change an already published snapshot.
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.credentials SET allowed_models = ARRAY[]::text[] WHERE id = $1", other)
		return err
	})
	// Acquire must still work with the DB unavailable, including after the clock crosses expiry.
	w.db.Close()
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if a.cred.id != other {
		t.Fatal("selected a key whose allowed models do not include the upstream name")
	}
	a.Done()
	w.clock.advance(time.Second)
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("at expiry without reloading: %v", err)
	}
}

func TestCredentialModelPolicies(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	for _, tc := range []struct {
		name, models string
		want         bool
	}{
		{"unrestricted", "NULL", true},
		{"upstream name", "ARRAY['gpt']", true},
		{"case sensitive", "ARRAY['GPT']", false},
		{"another model", "ARRAY['other']", false},
		{"no models", "ARRAY[]::text[]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w.tx(func(ctx context.Context, tx pg.Tx) error {
				_, err := tx.Exec(ctx, "UPDATE suppliers.credentials SET allowed_models = "+tc.models+" WHERE id = $1", w.credential)
				return err
			})
			if err := w.svc.Reload(t.Context()); err != nil {
				t.Fatal(err)
			}
			a, err := w.svc.Acquire(w.endpoint)
			if (err == nil) != tc.want {
				t.Fatalf("Acquire = %v, want available %v", err, tc.want)
			}
			if a != nil {
				a.Done()
			}
			w.tx(func(ctx context.Context, tx pg.Tx) error {
				costs, err := store.New().ListPriceCosts(ctx, tx)
				if err == nil && (len(costs) != 0) != tc.want {
					t.Errorf("price costs include ineligible credentials: %d", len(costs))
				}
				return err
			})
		})
	}
}
