package suppliers

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
)

// A group's claim on making its key (docs/domains/suppliers.md「一个分组只建一把」) as the writes of a group's credentials
// honour it: a made key is stored under its claim, any other credential waits for it, and a person can release it.

// claimAllows checks the group's credentials and claim for a key: the group may hold no credential but a made key
// itself, whose claim StoreMadeKey checks; a pasted key needs a group with no credential and no claim that may still
// lead to a made key.
func (a *Admin) claimAllows(ctx context.Context, tx pg.Tx, supplier, group uuid.UUID, key GroupKey) error {
	if key.Source != "made" {
		has, err := store.New().GroupHasCredential(ctx, tx, group)
		switch {
		case err != nil:
			return fmt.Errorf("read the group's keys: %w", err)
		case has:
			return ErrGroupHasKey
		}
		return unclaimed(persist.Unclaimed(ctx, tx, supplier, group, a.now()))
	}
	c, err := persist.GroupClaim(ctx, tx, supplier, group)
	if err == nil && c.Keyed && string(c.Stored) != key.Ref {
		return ErrGroupHasKey
	}
	return err
}

// unclaimed is persist.Unclaimed's refusal as the admin names it.
func unclaimed(err error) error {
	if errors.Is(err, persist.ErrClaimed) {
		return ErrKeyInProgress
	}
	return err
}

// storeConnection stores the key a group is connected with. A made key is kept the way the sync keeps the keys it
// makes, which settles the group's claim, seals a credential the key was erased from again rather than a second row,
// and leaves one the sync stored first as it is.
func (a *Admin) storeConnection(ctx context.Context, tx pg.Tx, supplier, group uuid.UUID, key GroupKey) (Credential, error) {
	if key.Source != "made" {
		return a.storeKey(ctx, tx, supplier, group, storedKey{
			source: key.Source, ref: key.Ref, text: key.Text, policy: key.Policy, limits: Limits{MaxConcurrency: defaultMaxConcurrency},
		})
	}
	id, _, err := persist.StoreMadeKey(ctx, tx, a.keys, supplier, group, persist.MadeKey{
		Ref: key.Ref, Text: key.Text, Status: "active", Claim: key.Claim,
		Policy: &adapter.KeyPolicy{Models: key.Policy.Models, ExpiresAt: key.Policy.ExpiresAt},
	})
	switch {
	case errors.Is(err, persist.ErrKeyBound):
		return Credential{}, ErrKeyBound
	case errors.Is(err, persist.ErrClaimLost):
		return Credential{}, ErrKeyInProgress
	}
	if err != nil {
		return Credential{}, err
	}
	return a.credential(ctx, tx, supplier, id)
}

// ReleaseKeyClaim ends a group's claim on making its key, for a person who has checked the supplier: a create whose
// result is not known that made nothing, or a key made that is to be revoked there. The next connection or sync then
// makes or finds the key afresh. claim is the claim the person read: one that is no longer the group's (adopted,
// released or taken over since) is ErrClaimChanged, so a release never ends a later attempt. The audit names the key's
// id when one is known, for revoking it.
func (a *Admin) ReleaseKeyClaim(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, group, claim uuid.UUID, reason string) (Group, error) {
	if _, err := lockedChild(ctx, a, tx, supplier, ErrNoGroup, "the group", func() ([]store.LockGroupRow, error) {
		return store.New().LockGroup(ctx, tx, store.LockGroupParams{ID: group, SupplierID: supplier})
	}); err != nil {
		return Group{}, err
	}
	c, err := persist.GroupClaim(ctx, tx, supplier, group)
	switch {
	case err != nil:
		return Group{}, err
	case c.ID == uuid.Nil() || c.ID != claim:
		return Group{}, ErrClaimChanged
	}
	if err := persist.DropClaim(ctx, tx, group, claim); err != nil {
		return Group{}, err
	}
	if err := a.record(ctx, tx, actor, "supplier_group.release_key_claim", supplier, reason, map[string]any{
		"group": group.String(), "claim": claim.String(), "sent": !c.SentAt.IsZero(), "upstream_key_ref": string(c.Ref),
	}); err != nil {
		return Group{}, err
	}
	return a.group(ctx, tx, supplier, group)
}
