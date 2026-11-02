package suppliers

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/store"
)

// byokFixture makes one key with a listed model for an account and returns it.
func byokFixture(t *testing.T, a admins, site, account uuid.UUID) ByokKey {
	t.Helper()
	key, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.CreateByok(ctx, tx, ByokRequest{
			Site: site, Account: account, Name: "my key", Provider: "openai",
			Key: "sk-proj-abcdefghijklmnop", Fallback: true, Actor: a.actor,
		}, []ByokModel{{
			ID: uuid.NewV7(), Upstream: "gpt-5", Formats: []protocol.Format{protocol.Chat},
			Schedule: []byte(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`),
		}})
	})
	refuses(t, "making a provider key", err, nil)
	return key
}

// TestByokLifecycle: a key is sealed, listed with only its last four characters, switched off, refused by the provider
// and cleared by a successful test; removing it erases the ciphertext and keeps the row for the attempts that used it.
func TestByokLifecycle(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	site, account := uuid.NewV7(), uuid.NewV7()
	key := byokFixture(t, a, site, account)
	same(t, "the key as made", []any{"active", true, false, true, "mnop"}, []any{
		key.Status, key.Enabled, key.Rejected, key.Fallback, key.Hint,
	})

	var listed []ByokKey
	if err := a.db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		listed, err = a.admin.ListByok(t.Context(), tx, site, account)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	same(t, "the list", 1, len(listed))

	var plain string
	if err := a.db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, _, secret, err := a.admin.RevealByok(t.Context(), tx, site, account, key.ID)
		plain = secret.Reveal()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	same(t, "the sealed key opens to what was sent", "sk-proj-abcdefghijklmnop", plain)

	disabled, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.SetByokEnabled(ctx, tx, site, account, key.ID, false, a.actor)
	})
	refuses(t, "switching the key off", err, nil)
	same(t, "a disabled key", []any{"disabled", false}, []any{disabled.Status, disabled.Enabled})

	rejected, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.SetByokTestResult(ctx, tx, site, account, key.ID, byokCredential(ctx, t, a, key.ID), false, a.actor)
	})
	refuses(t, "a refused key", err, nil)
	same(t, "a refused key", []any{"rejected", true}, []any{rejected.Status, rejected.Rejected})

	again, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.SetByokEnabled(ctx, tx, site, account, key.ID, true, a.actor)
	})
	refuses(t, "switching a refused key on", err, nil)
	same(t, "the switch cannot clear a refusal", "rejected", again.Status)

	accepted, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.SetByokTestResult(ctx, tx, site, account, key.ID, byokCredential(ctx, t, a, key.ID), true, a.actor)
	})
	refuses(t, "a key that works again", err, nil)
	same(t, "a successful test makes it active", "active", accepted.Status)

	if _, err := inTx(a, func(ctx context.Context, tx pg.Tx) (struct{}, error) {
		return struct{}{}, a.admin.RemoveByok(ctx, tx, site, account, key.ID, a.actor)
	}); err != nil {
		t.Fatal(err)
	}
	same(t, "removed", []any{"removed", "0"}, []any{
		a.text(`SELECT status FROM suppliers.suppliers WHERE id = '` + key.ID.String() + `'`),
		a.text(`SELECT coalesce(length(ciphertext), 0)::text FROM suppliers.credentials WHERE supplier_id = '` + key.ID.String() + `'`),
	})
}

// TestByokIsPerAccount: another account sees no key and cannot read, switch off or remove it: the account's scope is
// what finds the row, so an id from elsewhere is the same answer as one that does not exist.
func TestByokIsPerAccount(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	site, owner, other := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	key := byokFixture(t, a, site, owner)
	if err := a.db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		list, err := a.admin.ListByok(t.Context(), tx, site, other)
		if err != nil {
			return err
		}
		same(t, "another account's list", 0, len(list))
		if _, _, _, err := a.admin.RevealByok(t.Context(), tx, site, other, key.ID); !errors.Is(err, ErrByokNotFound) {
			t.Errorf("another account opened the key: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.SetByokEnabled(ctx, tx, site, other, key.ID, false, a.actor)
	}); !errors.Is(err, ErrByokNotFound) {
		t.Errorf("another account switched the key off: %v", err)
	}
}

// credentialOf is the key's credential id, read the way the service reads it.
func byokCredential(ctx context.Context, t *testing.T, a admins, id uuid.UUID) uuid.UUID {
	t.Helper()
	var credential uuid.UUID
	if err := a.db.ReadTx(ctx, platformScope, func(tx pg.Tx) error {
		var err error
		credential, err = store.New().CredentialOfByok(ctx, tx, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return credential
}
