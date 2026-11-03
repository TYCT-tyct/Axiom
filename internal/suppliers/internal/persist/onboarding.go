package persist

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// projectOnboarding writes the keys the keys task made or bound and the groups whose key failed, in the transaction that
// stores the state that knows them. The texts arrive in memory only; each is sealed at its own credential row here and
// never written elsewhere.
func projectOnboarding(ctx context.Context, tx pg.Tx, id uuid.UUID, w syncer.KeyWrites, keys *secret.Keyring) error {
	q := store.New()
	groups, err := q.GroupIDsOf(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("read the group ids: %w", err)
	}
	groupIDs := make(map[string]uuid.UUID, len(groups))
	for _, g := range groups {
		groupIDs[g.UpstreamRef] = g.ID
	}
	for _, failed := range w.Failed {
		reason, retry := failed.Reason, failed.RetryAt
		if err := q.SetGroupKeyError(ctx, tx, store.SetGroupKeyErrorParams{
			SupplierID: id, UpstreamRef: failed.GroupRef, KeyError: &reason, RetryAt: &retry,
		}); err != nil {
			return fmt.Errorf("record the key error of group %s: %w", failed.GroupRef, err)
		}
	}

	for _, made := range w.Made {
		group, ok := groupIDs[made.GroupRef]
		if !ok {
			return fmt.Errorf("made a key for group %s, which is not in the tables", made.GroupRef)
		}
		credential := uuid.NewV7()
		revive := false
		existing, err := q.ErasedCredentialID(ctx, tx, store.ErasedCredentialIDParams{
			SupplierID: id, UpstreamKeyRef: string(made.Ref),
		})
		switch {
		case err == nil:
			credential, revive = existing, true
		case !errors.Is(err, pg.ErrNoRows):
			return fmt.Errorf("read the erased key of group %s: %w", made.GroupRef, err)
		}
		sealed := keys.Seal(made.Secret, secret.Location{
			Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key",
		})
		version := int64(sealed.KeyVersion)
		status := credentialStatus(made.Status)
		if revive {
			changed, err := q.ReviveCredential(ctx, tx, store.ReviveCredentialParams{
				ID: credential, SupplierID: id, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce,
				WrappedDek: sealed.WrappedDEK, KeyVersion: &version, Status: status,
			})
			if err != nil {
				return fmt.Errorf("revive the key of group %s: %w", made.GroupRef, err)
			}
			if changed != 1 {
				return fmt.Errorf("revive the key of group %s: the credential is no longer erased", made.GroupRef)
			}
		} else if err := q.InsertCredential(ctx, tx, store.InsertCredentialParams{
			ID: credential, SupplierID: id, SupplierGroupID: group, UpstreamKeyRef: string(made.Ref),
			Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: &version,
			Status: status, MaxConcurrency: 4,
		}); err != nil {
			return fmt.Errorf("keep the key of group %s: %w", made.GroupRef, err)
		}
		if err := q.ClearGroupKeyError(ctx, tx, store.ClearGroupKeyErrorParams{SupplierID: id, UpstreamRef: made.GroupRef}); err != nil {
			return fmt.Errorf("clear the key error of group %s: %w", made.GroupRef, err)
		}
	}
	for _, bound := range w.Bound {
		sealed := keys.Seal(bound.Secret, secret.Location{
			Table: "suppliers.credentials", RowID: bound.ID.String(), Purpose: "key",
		})
		version := int64(sealed.KeyVersion)
		if err := q.ActivateCredential(ctx, tx, store.ActivateCredentialParams{
			ID: bound.ID, SupplierID: id, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce,
			WrappedDek: sealed.WrappedDEK, KeyVersion: &version,
		}); err != nil {
			return fmt.Errorf("activate the key of group %s: %w", bound.GroupRef, err)
		}
		if err := q.ClearGroupKeyError(ctx, tx, store.ClearGroupKeyErrorParams{SupplierID: id, UpstreamRef: bound.GroupRef}); err != nil {
			return fmt.Errorf("clear the key error of group %s: %w", bound.GroupRef, err)
		}
	}
	return nil
}

// projectLostKeys erases the credentials of keys a complete listing no longer has, after the policy projection has
// touched the ones it did have: a key the listing saw this round is not erased for having been away too long. A listing
// that lags must not erase a key just made, so the key must also be older than the sync's grace.
func projectLostKeys(ctx context.Context, tx pg.Tx, id uuid.UUID, w syncer.KeyWrites) error {
	if len(w.Lost) == 0 {
		return nil
	}
	refs := make([]string, len(w.Lost))
	for i, ref := range w.Lost {
		refs[i] = string(ref)
	}
	if err := store.New().EraseCredentialsByRef(ctx, tx, store.EraseCredentialsByRefParams{
		SupplierID: id, Refs: refs, StaleBefore: w.StaleBefore,
	}); err != nil {
		return fmt.Errorf("erase the lost keys: %w", err)
	}
	return nil
}

// credentialStatus is the credential status a key's own status stands for.
func credentialStatus(s adapter.KeyStatus) string {
	switch s {
	case adapter.KeyDisabled:
		return "upstream_disabled"
	case adapter.KeyQuotaExhausted:
		return "quota_exhausted"
	case adapter.KeyExpired:
		return "expired"
	}
	return "active"
}
