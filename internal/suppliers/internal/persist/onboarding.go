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
	groups, err := groupIDs(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := projectKeyFailures(ctx, tx, id, w.Failed); err != nil {
		return err
	}
	for _, made := range w.Made {
		if err := projectMadeKey(ctx, tx, id, groups, made, keys); err != nil {
			return err
		}
	}
	for _, bound := range w.Bound {
		if err := projectBoundKey(ctx, tx, id, bound, keys); err != nil {
			return err
		}
	}
	return nil
}

// groupIDs is the supplier's groups by their upstream reference, so a key write can find the row it belongs to.
func groupIDs(ctx context.Context, tx pg.Tx, id uuid.UUID) (map[string]uuid.UUID, error) {
	groups, err := store.New().GroupIDsOf(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("read the group ids: %w", err)
	}
	ids := make(map[string]uuid.UUID, len(groups))
	for _, g := range groups {
		ids[g.UpstreamRef] = g.ID
	}
	return ids, nil
}

// projectKeyFailures records, per group, the fixed reason a key could not be made and when to try again.
func projectKeyFailures(ctx context.Context, tx pg.Tx, id uuid.UUID, failed []syncer.KeyFailure) error {
	q := store.New()
	for _, f := range failed {
		reason, retry := f.Reason, f.RetryAt
		if err := q.SetGroupKeyError(ctx, tx, store.SetGroupKeyErrorParams{
			SupplierID: id, UpstreamRef: f.GroupRef, KeyError: &reason, RetryAt: &retry,
		}); err != nil {
			return fmt.Errorf("record the key error of group %s: %w", f.GroupRef, err)
		}
	}
	return nil
}

// projectMadeKey stores the text of a key the sync made. A row erased when the key was once lost is revived instead of
// inserting a second row for the same upstream key.
func projectMadeKey(ctx context.Context, tx pg.Tx, id uuid.UUID, groups map[string]uuid.UUID, made syncer.KeyMade, keys *secret.Keyring) error {
	group, ok := groups[made.GroupRef]
	if !ok {
		return fmt.Errorf("made a key for group %s, which is not in the tables", made.GroupRef)
	}
	credential, revive, err := erasedCredential(ctx, tx, id, made.GroupRef, made.Ref)
	if err != nil {
		return err
	}
	sealed := keys.Seal(made.Secret, secret.Location{
		Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key",
	})
	version := int64(sealed.KeyVersion)
	q := store.New()
	if revive {
		changed, err := q.ReviveCredential(ctx, tx, store.ReviveCredentialParams{
			ID: credential, SupplierID: id, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce,
			WrappedDek: sealed.WrappedDEK, KeyVersion: &version, Status: credentialStatus(made.Status),
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
		Status: credentialStatus(made.Status), MaxConcurrency: 4,
	}); err != nil {
		return fmt.Errorf("keep the key of group %s: %w", made.GroupRef, err)
	}
	if err := q.ClearGroupKeyError(ctx, tx, store.ClearGroupKeyErrorParams{SupplierID: id, UpstreamRef: made.GroupRef}); err != nil {
		return fmt.Errorf("clear the key error of group %s: %w", made.GroupRef, err)
	}
	return nil
}

// erasedCredential is the row of an erased key with this reference, or a new id when there is none.
func erasedCredential(ctx context.Context, tx pg.Tx, id uuid.UUID, groupRef string, ref adapter.KeyRef) (uuid.UUID, bool, error) {
	existing, err := store.New().ErasedCredentialID(ctx, tx, store.ErasedCredentialIDParams{
		SupplierID: id, UpstreamKeyRef: string(ref),
	})
	switch {
	case err == nil:
		return existing, true, nil
	case errors.Is(err, pg.ErrNoRows):
		return uuid.NewV7(), false, nil
	default:
		return uuid.Nil(), false, fmt.Errorf("read the erased key of group %s: %w", groupRef, err)
	}
}

// projectBoundKey activates a credential a person bound by reference, now that its text has been read.
func projectBoundKey(ctx context.Context, tx pg.Tx, id uuid.UUID, bound syncer.KeyBound, keys *secret.Keyring) error {
	sealed := keys.Seal(bound.Secret, secret.Location{
		Table: "suppliers.credentials", RowID: bound.ID.String(), Purpose: "key",
	})
	version := int64(sealed.KeyVersion)
	q := store.New()
	if err := q.ActivateCredential(ctx, tx, store.ActivateCredentialParams{
		ID: bound.ID, SupplierID: id, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce,
		WrappedDek: sealed.WrappedDEK, KeyVersion: &version,
	}); err != nil {
		return fmt.Errorf("activate the key of group %s: %w", bound.GroupRef, err)
	}
	if err := q.ClearGroupKeyError(ctx, tx, store.ClearGroupKeyErrorParams{SupplierID: id, UpstreamRef: bound.GroupRef}); err != nil {
		return fmt.Errorf("clear the key error of group %s: %w", bound.GroupRef, err)
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
	case adapter.KeyActive:
		return "active"
	case adapter.KeyDisabled:
		return "upstream_disabled"
	case adapter.KeyQuotaExhausted:
		return "quota_exhausted"
	case adapter.KeyExpired:
		return "expired"
	}
	return "active"
}
