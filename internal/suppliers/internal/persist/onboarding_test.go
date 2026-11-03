package persist_test

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

func TestOnboardingWritesKeysAndFailures(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	groups := []listedGroup{
		{ref: "g", name: "G", ratio: "1", facts: "{}"},
		{ref: "vip", name: "VIP", ratio: "1", facts: "{}"},
	}
	s := c.acquire(c.node("one"))
	c.commitListing(s, 1, groups)
	var group, vip uuid.UUID
	c.value(&group, "SELECT id FROM suppliers.supplier_groups WHERE supplier_id = $1 AND upstream_ref = 'g'", c.supplier)
	c.value(&vip, "SELECT id FROM suppliers.supplier_groups WHERE supplier_id = $1 AND upstream_ref = 'vip'", c.supplier)

	// A key a person bound by reference, waiting for its text; and one that has aged out of the listing.
	binding := uuid.NewV7()
	lost := uuid.NewV7()
	erased := uuid.NewV7()
	err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		if err := store.New().InsertBindingCredential(t.Context(), tx, store.InsertBindingCredentialParams{
			ID: binding, SupplierID: c.supplier, SupplierGroupID: group, UpstreamKeyRef: "8", MaxConcurrency: 4,
		}); err != nil {
			return err
		}
		sealed := c.keys.Seal(secret.New("old"), secret.Location{Table: "suppliers.credentials", RowID: lost.String(), Purpose: "key"})
		version := int64(sealed.KeyVersion)
		if err := store.New().InsertCredential(t.Context(), tx, store.InsertCredentialParams{
			ID: lost, SupplierID: c.supplier, SupplierGroupID: group, UpstreamKeyRef: "7",
			Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: &version,
			Status: "active", MaxConcurrency: 4,
		}); err != nil {
			return err
		}
		if err := store.New().InsertCredential(t.Context(), tx, store.InsertCredentialParams{
			ID: erased, SupplierID: c.supplier, SupplierGroupID: group, UpstreamKeyRef: "6", Status: "erased", MaxConcurrency: 4,
		}); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), "UPDATE suppliers.credentials SET last_seen_at = $2 WHERE id = $1", lost, t0.Add(-time.Hour))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "UPDATE suppliers.supplier_groups SET key_error = 'not_permitted', key_retry_at = now() WHERE id = $1", group)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	c2 := listing(2, groups)
	c2.KeyWrites = syncer.KeyWrites{
		Made: []syncer.KeyMade{
			{Ref: "9", GroupRef: "g", Status: adapter.KeyActive, Secret: secret.New("s3")},
			{Ref: "6", GroupRef: "g", Status: adapter.KeyActive, Secret: secret.New("s6")},
		},
		Bound: []syncer.KeyBound{{
			ID: binding, Ref: "8", GroupRef: "g", Secret: secret.New("s8"),
		}},
		Failed: []syncer.KeyFailure{{
			GroupRef: "vip", Reason: "refused", RetryAt: t0.Add(time.Hour),
		}},
		Lost:        []adapter.KeyRef{"7"},
		StaleBefore: t0,
	}
	if err := s.Store().Commit(t.Context(), 1, c2); err != nil {
		t.Fatal(err)
	}

	var status, reason string
	var hasText bool
	c.value(&status, "SELECT status FROM suppliers.credentials WHERE upstream_key_ref = '9'")
	c.value(&hasText, "SELECT ciphertext IS NOT NULL FROM suppliers.credentials WHERE upstream_key_ref = '9'")
	if status != "active" || !hasText {
		t.Fatalf("made key: status %q, text %v", status, hasText)
	}
	c.value(&status, "SELECT status FROM suppliers.credentials WHERE upstream_key_ref = '8'")
	c.value(&hasText, "SELECT ciphertext IS NOT NULL FROM suppliers.credentials WHERE upstream_key_ref = '8'")
	if status != "active" || !hasText {
		t.Fatalf("bound key: status %q, text %v", status, hasText)
	}
	var count int
	c.value(&count, "SELECT count(*) FROM suppliers.credentials WHERE upstream_key_ref = '6'")
	c.value(&status, "SELECT status FROM suppliers.credentials WHERE upstream_key_ref = '6'")
	c.value(&hasText, "SELECT ciphertext IS NOT NULL FROM suppliers.credentials WHERE upstream_key_ref = '6'")
	if count != 1 || status != "active" || !hasText {
		t.Fatalf("revived key: %d rows, status %q, text %v", count, status, hasText)
	}
	c.value(&status, "SELECT status FROM suppliers.credentials WHERE upstream_key_ref = '7'")
	if status != "erased" {
		t.Fatalf("lost key: status %q, want erased", status)
	}
	c.value(&reason, "SELECT coalesce(key_error, '-') FROM suppliers.supplier_groups WHERE id = $1", group)
	if reason != "-" {
		t.Fatalf("group %s still has key error %q", group, reason)
	}
	c.value(&reason, "SELECT key_error FROM suppliers.supplier_groups WHERE id = $1", vip)
	if reason != "refused" {
		t.Fatalf("group %s has key error %q, want refused", vip, reason)
	}
}
