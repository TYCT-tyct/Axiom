package persist

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// ClaimHold is how long a claim may stay held, with nothing sent, before another may take it over: a holder only looks
// the key up by its name (one request, at most 30 seconds) and records that it sends the create. A holder taken over
// cannot record that, so it never sends.
const ClaimHold = 2 * time.Minute

var _ syncer.Claims = Claims{}

// Claims are one supplier's group claims (syncer.Claims), each change a transaction of its own over db.
type Claims struct {
	db       *pg.DB
	scope    pg.Scope
	supplier uuid.UUID
	now      func() time.Time
}

// NewClaims are the claims of the supplier's groups, read and written over db in scope, on the clock now.
func NewClaims(db *pg.DB, scope pg.Scope, supplier uuid.UUID, now func() time.Time) Claims {
	return Claims{db: db, scope: scope, supplier: supplier, now: now}
}

// Claims implements syncer.KeyStore.
func (st stateStore) Claims() syncer.Claims {
	cfg := st.s.node.cfg
	return NewClaims(cfg.DB, cfg.Scope, st.s.Supplier.ID, cfg.Now)
}

// Claim implements syncer.Claims. The supplier's row is taken first, as every write of a credential takes it, so a claim
// and a credential of the group are never written past each other.
func (c Claims) Claim(ctx context.Context, groupRef string) (syncer.Claim, error) {
	var out syncer.Claim
	err := c.db.Tx(ctx, c.scope, func(tx pg.Tx) error {
		row, found, err := lockClaim(ctx, tx, c.supplier, nil, &groupRef)
		switch {
		case err != nil:
			return err
		case !found || !row.Present:
			out.Gone = true
			return nil
		}
		out = claimOf(row)
		now := c.now()
		if out.Keyed || (out.ID != uuid.Nil() && (!out.SentAt.IsZero() || now.Before(out.At.Add(ClaimHold)))) {
			return nil
		}
		id := uuid.NewV7()
		if err := store.New().TakeKeyClaim(ctx, tx, store.TakeKeyClaimParams{ID: row.ID, ClaimID: &id, ClaimedAt: &now}); err != nil {
			return fmt.Errorf("claim the key of group %s: %w", groupRef, err)
		}
		out = syncer.Claim{Group: row.ID, ID: id, Mine: true, At: now}
		return nil
	})
	return out, err
}

// Send implements syncer.Claims.
func (c Claims) Send(ctx context.Context, cl syncer.Claim) (bool, error) {
	return c.changed(ctx, "record the create", func(q *store.Queries, tx pg.Tx) (int64, error) {
		return q.SendKeyClaim(ctx, tx, store.SendKeyClaimParams{ID: cl.Group, ClaimID: &cl.ID, SentAt: new(c.now())})
	})
}

// Resend implements syncer.Claims: the new holder keeps the time of the first create, which the replay window counts
// from.
func (c Claims) Resend(ctx context.Context, cl syncer.Claim) (syncer.Claim, bool, error) {
	next := syncer.Claim{Group: cl.Group, ID: uuid.NewV7(), Mine: true, At: c.now(), SentAt: cl.SentAt}
	taken, err := c.changed(ctx, "take the create over", func(q *store.Queries, tx pg.Tx) (int64, error) {
		return q.ResendKeyClaim(ctx, tx, store.ResendKeyClaimParams{ID: cl.Group, ClaimID: &cl.ID, NewID: &next.ID, ClaimedAt: &next.At})
	})
	if !taken {
		return cl, false, err
	}
	return next, true, err
}

// Know implements syncer.Claims.
func (c Claims) Know(ctx context.Context, cl syncer.Claim, ref adapter.KeyRef) (bool, error) {
	return c.changed(ctx, "record the key's id", func(q *store.Queries, tx pg.Tx) (int64, error) {
		return q.KnowKeyClaim(ctx, tx, store.KnowKeyClaimParams{ID: cl.Group, ClaimID: &cl.ID, Ref: new(string(ref))})
	})
}

// Drop implements syncer.Claims.
func (c Claims) Drop(ctx context.Context, cl syncer.Claim) error {
	_, err := c.changed(ctx, "end the claim", func(q *store.Queries, tx pg.Tx) (int64, error) {
		return q.DropKeyClaim(ctx, tx, store.DropKeyClaimParams{ID: cl.Group, ClaimID: &cl.ID})
	})
	return err
}

// changed runs one compare-and-set of a claim and answers whether it still was the claim compared.
func (c Claims) changed(ctx context.Context, what string, f func(*store.Queries, pg.Tx) (int64, error)) (bool, error) {
	var n int64
	err := c.db.Tx(ctx, c.scope, func(tx pg.Tx) (err error) {
		n, err = f(store.New(), tx)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("%s: %w", what, err)
	}
	return n == 1, nil
}

// lockClaim locks the supplier's row, then the group's, by its id or its ref, with its claim.
func lockClaim(ctx context.Context, tx pg.Tx, supplier uuid.UUID, group *uuid.UUID, groupRef *string) (store.LockGroupClaimRow, bool, error) {
	q := store.New()
	if _, err := q.LockSupplier(ctx, tx, supplier); err != nil {
		return store.LockGroupClaimRow{}, false, fmt.Errorf("lock the supplier: %w", err)
	}
	rows, err := q.LockGroupClaim(ctx, tx, store.LockGroupClaimParams{SupplierID: supplier, ID: group, UpstreamRef: groupRef})
	if err != nil {
		return store.LockGroupClaimRow{}, false, fmt.Errorf("lock the group's claim: %w", err)
	}
	if len(rows) == 0 {
		return store.LockGroupClaimRow{}, false, nil
	}
	return rows[0], true, nil
}

func claimOf(r store.LockGroupClaimRow) syncer.Claim {
	c := syncer.Claim{Group: r.ID, Keyed: r.Keyed, Stored: adapter.KeyRef(r.MadeRef)}
	if r.KeyClaimID != nil {
		c.ID, c.At = *r.KeyClaimID, *r.KeyClaimedAt
	}
	if r.KeyClaimSentAt != nil {
		c.SentAt = *r.KeyClaimSentAt
	}
	if r.KeyClaimRef != nil {
		c.Ref = adapter.KeyRef(*r.KeyClaimRef)
	}
	return c
}

// ErrClaimed is a credential written into a group whose claim may still lead to a key Axiom makes: the claim has to be
// adopted, or released by a person, first.
var ErrClaimed = errors.New("persist: the group's key is being made by Axiom")

// GroupClaim is a group's claim as a write of a credential into the group finds it, under the supplier's row, which the
// caller holds already, and the group's.
func GroupClaim(ctx context.Context, tx pg.Tx, supplier, group uuid.UUID) (syncer.Claim, error) {
	rows, err := store.New().LockGroupClaim(ctx, tx, store.LockGroupClaimParams{SupplierID: supplier, ID: &group})
	if err != nil {
		return syncer.Claim{}, fmt.Errorf("read the group's claim: %w", err)
	}
	if len(rows) == 0 {
		return syncer.Claim{}, nil
	}
	return claimOf(rows[0]), nil
}

// Unclaimed lets a credential that is no key Axiom made into the group: ErrClaimed when a create was sent, its key is
// known, or a holder may still send one; a claim held past ClaimHold sent nothing and is ended.
func Unclaimed(ctx context.Context, tx pg.Tx, supplier, group uuid.UUID, now time.Time) error {
	c, err := GroupClaim(ctx, tx, supplier, group)
	switch {
	case err != nil:
		return err
	case c.ID == uuid.Nil():
		return nil
	case !c.SentAt.IsZero() || now.Before(c.At.Add(ClaimHold)):
		return ErrClaimed
	}
	if _, err := store.New().DropKeyClaim(ctx, tx, store.DropKeyClaimParams{ID: group, ClaimID: &c.ID}); err != nil {
		return fmt.Errorf("end the group's claim: %w", err)
	}
	return nil
}

// MadeKey is a key Axiom made or adopted by its marker name, to store as the credential of its group: Status is the
// credential status it starts in, and Policy what the supplier lets it call (nil is every model, no expiry).
type MadeKey struct {
	Ref    string
	Text   secret.Secret
	Status string
	Policy *adapter.KeyPolicy
}

// StoreMadeKey stores a made key as the group's credential, sealed to its row, and settles the group's claim, in the
// caller's transaction under the supplier's row. A credential the key was stored as and erased is sealed again instead
// of a second row inserted. stored is false when the key is a credential of the group already, which is left as it
// is; a key stored under another group is ErrKeyBound.
func StoreMadeKey(ctx context.Context, tx pg.Tx, keys *secret.Keyring, supplier, group uuid.UUID, k MadeKey) (id uuid.UUID, stored bool, err error) {
	q := store.New()
	rows, err := q.MadeCredentialByRef(ctx, tx, store.MadeCredentialByRefParams{SupplierID: supplier, UpstreamKeyRef: k.Ref})
	if err != nil {
		return uuid.Nil(), false, fmt.Errorf("read the credential of key %s: %w", k.Ref, err)
	}
	id, revive := uuid.NewV7(), false
	if len(rows) == 1 {
		switch r := rows[0]; {
		case r.SupplierGroupID != group:
			return uuid.Nil(), false, ErrKeyBound
		case r.Status != "erased":
			return r.ID, false, q.SettleKeyClaim(ctx, tx, store.SettleKeyClaimParams{ID: group, Ref: new(k.Ref)})
		default:
			id, revive = r.ID, true
		}
	}
	sealed := keys.Seal(k.Text, secret.Location{Table: "suppliers.credentials", RowID: id.String(), Purpose: "key"})
	version := int64(sealed.KeyVersion)
	var models []string
	var expires *time.Time
	if k.Policy != nil {
		models, expires = k.Policy.Models, k.Policy.ExpiresAt
	}
	if revive {
		_, err = q.ReviveCredential(ctx, tx, store.ReviveCredentialParams{
			ID: id, SupplierID: supplier, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK,
			KeyVersion: &version, Status: k.Status, AllowedModels: models, ExpiresAt: expires,
		})
	} else {
		err = q.InsertCredential(ctx, tx, store.InsertCredentialParams{
			ID: id, SupplierID: supplier, SupplierGroupID: group, UpstreamKeyRef: k.Ref, Ciphertext: sealed.Ciphertext,
			Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: &version, Status: k.Status, MaxConcurrency: 4,
			AllowedModels: models, ExpiresAt: expires,
		})
	}
	if err != nil {
		return uuid.Nil(), false, fmt.Errorf("store key %s: %w", k.Ref, err)
	}
	return id, true, q.SettleKeyClaim(ctx, tx, store.SettleKeyClaimParams{ID: group, Ref: new(k.Ref)})
}

// ErrKeyBound is a key stored already as a credential of another group.
var ErrKeyBound = errors.New("persist: the key is stored for another group")
