package suppliers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
)

// adminOf is the admin side over the world's database.
func (w *world) adminOf() *Admin {
	w.t.Helper()
	reg, err := registry.New(fakeKind{})
	if err != nil {
		w.t.Fatal(err)
	}
	admin, err := NewAdmin(AdminConfig{Keys: w.keys, Egress: egress.NewForTesting(egresstest.Loopback(w.t)), Registry: reg})
	if err != nil {
		w.t.Fatal(err)
	}
	return admin
}

// withSecrets gives the supplier a sealed login, and its group a disabled credential and one still being bound, besides
// the active one the world has.
func (w *world) withSecrets() {
	w.t.Helper()
	second := uuid.NewV7()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		login := w.keys.Seal(secret.New(`{"token":"login-secret"}`), secret.Location{Table: "suppliers.supplier_logins", RowID: w.supplier.String(), Purpose: "login"})
		if err := store.New().InsertSupplierLogin(ctx, tx, store.InsertSupplierLoginParams{
			SupplierID: w.supplier, Kind: "token", Ciphertext: login.Ciphertext, Nonce: login.Nonce, WrappedDek: login.WrappedDEK, KeyVersion: int64(login.KeyVersion),
		}); err != nil {
			return err
		}
		key := w.keys.Seal(secret.New("sk-second"), secret.Location{Table: "suppliers.credentials", RowID: second.String(), Purpose: "key"})
		version := int64(key.KeyVersion)
		if err := store.New().InsertCredential(ctx, tx, store.InsertCredentialParams{
			ID: second, SupplierID: w.supplier, SupplierGroupID: w.group, UpstreamKeyRef: "8", Ciphertext: key.Ciphertext, Nonce: key.Nonce,
			WrappedDek: key.WrappedDEK, KeyVersion: &version, Status: "disabled", MaxConcurrency: 1,
		}); err != nil {
			return err
		}
		return store.New().InsertBindingCredential(ctx, tx, store.InsertBindingCredentialParams{
			ID: uuid.NewV7(), SupplierID: w.supplier, SupplierGroupID: w.group, UpstreamKeyRef: "9", MaxConcurrency: 1,
		})
	})
}

// removeAs removes the world's supplier and returns it as the removal answered.
func (w *world) removeAs(admin *Admin) Supplier {
	w.t.Helper()
	var s Supplier
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		s, err = admin.Remove(ctx, tx, audit.User(uuid.NewV7()), w.supplier, "no longer needed")
		return err
	})
	return s
}

// TestRemoveErasesWhatItHolds: removing a supplier ends it in one transaction. The sealed login is deleted, every key of
// its credentials is cleared (a bound one and one still being bound alike) and the credentials are `erased` but still
// there, the generation rises, and the audit entry counts the keys to revoke without a secret, and their ids stay on the credential rows. Removing again changes
// nothing.
func TestRemoveErasesWhatItHolds(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.withSecrets()
	admin := w.adminOf()
	for _, again := range []bool{false, true} {
		if s := w.removeAs(admin); s.Status != Removed {
			t.Fatalf("the status after removing (again %v) is %s", again, s.Status)
		}
		same(t, "the generation and the audit entries", [2]int{1, 1}, [2]int{
			w.count("SELECT config_gen FROM suppliers.suppliers"),
			w.count("SELECT count(*) FROM platform.audit_log WHERE action = 'supplier.remove'"),
		})
	}
	same(t, "logins left", 0, w.count("SELECT count(*) FROM suppliers.supplier_logins"))
	same(t, "credentials erased, with no key left", [2]int{3, 0}, [2]int{
		w.count("SELECT count(*) FROM suppliers.credentials WHERE status = 'erased'"),
		w.count("SELECT count(*) FROM suppliers.credentials WHERE ciphertext IS NOT NULL OR nonce IS NOT NULL OR wrapped_dek IS NOT NULL OR key_version IS NOT NULL"),
	})
	var detail string
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return tx.QueryRow(ctx, "SELECT detail::text FROM platform.audit_log WHERE action = 'supplier.remove'").Scan(&detail)
	})
	if !strings.Contains(detail, `"keys_to_revoke_count":3`) {
		t.Errorf("the audit entry %s does not count the 3 keys to revoke", detail)
	}
	same(t, "the keys to revoke stay on the erased credentials", 3,
		w.count("SELECT count(*) FROM suppliers.credentials WHERE status = 'erased' AND upstream_key_ref IN ('7', '8', '9')"))
	if strings.Contains(detail, "login-secret") || strings.Contains(detail, "sk-") {
		t.Errorf("the audit entry holds a secret: %s", detail)
	}
}

// TestARemovedSupplierLeavesRoutingAndItsAttemptsStillSettle: the supplier is out of the snapshot at the next reload,
// and an attempt made with one of its credentials before the removal can still be recorded, because the credential's
// row is kept.
func TestARemovedSupplierLeavesRoutingAndItsAttemptsStillSettle(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	if got := len(w.svc.Routable()); got != 1 {
		t.Fatalf("%d endpoints routable before the removal, want 1", got)
	}
	before := w.attempt(newRequest(), 1, "ref-before") // made with the credential before it is erased
	w.removeAs(w.adminOf())
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := len(w.svc.Routable()); got != 0 {
		t.Errorf("%d endpoints routable after the removal, want none", got)
	}
	w.record(before)
	same(t, "attempts recorded", 1, w.count("SELECT count(*) FROM suppliers.attempt_costs"))
}

// TestAnErasedCredentialHoldsNothing: the table refuses an erased credential that still has its key, which is what
// keeps a removal from leaving one behind.
func TestAnErasedCredentialHoldsNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	err := w.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "UPDATE suppliers.credentials SET status = 'erased'")
		return err
	})
	if pg.SQLState(err) != "23514" {
		t.Errorf("erasing a credential and keeping its key: %v, want a check violation", err)
	}
}

// TestARemovalDoesNotDependOnHowManyKeysItErases: a supplier with a hundred keys of the longest id the contract allows
// is removed, though their ids together would not fit the audit entry's 8192 bytes.
func TestARemovalDoesNotDependOnHowManyKeysItErases(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		for i := range 100 {
			ref := strings.Repeat("k", 190) + fmt.Sprintf("%010d", i)
			if err := store.New().InsertBindingCredential(ctx, tx, store.InsertBindingCredentialParams{
				ID: uuid.NewV7(), SupplierID: w.supplier, SupplierGroupID: w.group, UpstreamKeyRef: ref, MaxConcurrency: 1,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if s := w.removeAs(w.adminOf()); s.Status != Removed {
		t.Fatalf("the status after removing is %s", s.Status)
	}
	same(t, "credentials erased", 101, w.count("SELECT count(*) FROM suppliers.credentials WHERE status = 'erased'"))
}
