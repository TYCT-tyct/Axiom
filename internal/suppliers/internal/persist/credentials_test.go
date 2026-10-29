package persist_test

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

func TestCredentialPoliciesCommitWithCompleteListing(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	db := c.open()
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		group, err := store.New().InsertGroup(t.Context(), tx, store.InsertGroupParams{SupplierID: c.supplier, UpstreamRef: "g", Name: "g", Ratio: "1"})
		if err != nil {
			return err
		}
		for _, ref := range []string{"limited", "unlimited", "unknown", "gone", "moved"} {
			if err := store.New().InsertBindingCredential(t.Context(), tx, store.InsertBindingCredentialParams{ID: uuid.NewV7(), SupplierID: c.supplier, SupplierGroupID: group, UpstreamKeyRef: ref, MaxConcurrency: 4}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s := c.acquire(c.node("one"))
	expires := t0.Add(time.Hour)
	keys := []adapter.KeyState{
		{Ref: "limited", GroupRef: "g", Policy: &adapter.KeyPolicy{Models: []string{"upstream"}, ExpiresAt: &expires}},
		{Ref: "unlimited", GroupRef: "g", Policy: &adapter.KeyPolicy{}},
		{Ref: "unknown", GroupRef: "g"},
		{Ref: "moved", GroupRef: "other", Policy: &adapter.KeyPolicy{}},
	}
	if err := s.Store().Commit(t.Context(), 0, syncer.Commit{State: syncer.State{Version: 1}, KeyPolicies: &keys}); err != nil {
		t.Fatal(err)
	}
	same(t, "policies", c.column("credentials", "coalesce(allowed_models::text, 'unlimited')", "upstream_key_ref"), []string{"{}", "{upstream}", "{}", "{}", "unlimited"})
	var got time.Time
	c.value(&got, "SELECT expires_at FROM suppliers.credentials WHERE upstream_key_ref = 'limited'")
	if !got.Equal(expires) {
		t.Fatalf("stored expiry: %v", got)
	}
	marks := c.marks("credentials", "id::text")
	if err := s.Store().Commit(t.Context(), 1, syncer.Commit{State: syncer.State{Version: 2}, KeyPolicies: &keys}); err != nil {
		t.Fatal(err)
	}
	same(t, "identical policies write nothing", c.marks("credentials", "id::text"), marks)
	// A round with an incomplete key listing has no policy payload and cannot revoke permission.
	if err := s.Store().Commit(t.Context(), 2, syncer.Commit{State: syncer.State{Version: 3}}); err != nil {
		t.Fatal(err)
	}
	same(t, "incomplete listing", c.marks("credentials", "id::text"), marks)
	empty := []adapter.KeyState{}
	if err := s.Store().Commit(t.Context(), 1, syncer.Commit{State: syncer.State{Version: 4}, KeyPolicies: &empty}); !errors.Is(err, syncer.ErrFenced) {
		t.Fatalf("stale commit: %v", err)
	}
	same(t, "fenced policies", c.marks("credentials", "id::text"), marks)
	if err := s.Store().Commit(t.Context(), 3, syncer.Commit{State: syncer.State{Version: 4}, KeyPolicies: &empty}); err != nil {
		t.Fatal(err)
	}
	same(t, "empty completed listing revokes all", c.column("credentials", "allowed_models::text", "upstream_key_ref"), []string{"{}", "{}", "{}", "{}", "{}"})
}
