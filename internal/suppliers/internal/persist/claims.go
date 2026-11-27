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
// the key up (a few requests of at most 30 seconds each) and records that it sends the create. A holder taken over
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
		q := store.New()
		if _, err := q.LockSupplier(ctx, tx, c.supplier); err != nil {
			return fmt.Errorf("lock the supplier: %w", err)
		}
		row, found, err := groupClaim(ctx, tx, store.LockGroupClaimParams{SupplierID: c.supplier, UpstreamRef: &groupRef})
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
		if err := q.TakeKeyClaim(ctx, tx, store.TakeKeyClaimParams{ID: row.ID, ClaimID: &id, ClaimedAt: &now}); err != nil {
			return fmt.Errorf("claim the key of group %s: %w", groupRef, err)
		}
		out.ID, out.At, out.Mine = id, now, true
		return nil
	})
	return out, err
}

// Send implements syncer.Claims.
func (c Claims) Send(ctx context.Context, cl syncer.Claim) (syncer.Claim, bool, error) {
	at := c.now()
	sent, err := c.changed(ctx, "record the create", func(q *store.Queries, tx pg.Tx) (int64, error) {
		return q.SendKeyClaim(ctx, tx, store.SendKeyClaimParams{ID: cl.Group, ClaimID: &cl.ID, SentAt: &at})
	})
	if sent {
		cl.SentAt, cl.Listing = at, nil
	}
	return cl, sent, err
}

// Park implements syncer.Claims.
func (c Claims) Park(ctx context.Context, cl syncer.Claim, at adapter.Listing) error {
	_, err := c.changed(ctx, "keep where the search stopped", func(q *store.Queries, tx pg.Tx) (int64, error) {
		return q.ParkKeyClaim(ctx, tx, store.ParkKeyClaimParams{ID: cl.Group, ClaimID: &cl.ID, Listing: at})
	})
	return err
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

// groupClaim locks the group's row, by its id or its ref, with its claim. The caller holds the supplier's row.
func groupClaim(ctx context.Context, tx pg.Tx, by store.LockGroupClaimParams) (store.LockGroupClaimRow, bool, error) {
	rows, err := store.New().LockGroupClaim(ctx, tx, by)
	if err != nil {
		return store.LockGroupClaimRow{}, false, fmt.Errorf("lock the group's claim: %w", err)
	}
	if len(rows) == 0 {
		return store.LockGroupClaimRow{}, false, nil
	}
	return rows[0], true, nil
}

func claimOf(r store.LockGroupClaimRow) syncer.Claim {
	c := syncer.Claim{Group: r.ID, Keyed: r.Keyed, Stored: adapter.KeyRef(r.MadeRef), Listing: r.KeyClaimListing}
	for _, ref := range r.ErasedRefs {
		c.Erased = append(c.Erased, adapter.KeyRef(ref))
	}
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

// The ways a write meets a group's claim.
var (
	// ErrClaimed is a credential written into a group whose claim may still lead to a key Axiom makes: the claim has to
	// be adopted, or released by a person, first.
	ErrClaimed = errors.New("persist: the group's key is being made by Axiom")
	// ErrClaimLost is a made key whose claim is no longer the group's: another holder took it over, or a person
	// released it, so the key is not stored.
	ErrClaimLost = errors.New("persist: the claim the key was obtained under is no longer the group's")
	// ErrKeyBound is a key stored already as a credential of another group.
	ErrKeyBound = errors.New("persist: the key is stored for another group")
)

// GroupClaim is a group's claim as a write of a credential into the group finds it, under the supplier's row, which the
// caller holds already, and the group's.
func GroupClaim(ctx context.Context, tx pg.Tx, supplier, group uuid.UUID) (syncer.Claim, error) {
	row, found, err := groupClaim(ctx, tx, store.LockGroupClaimParams{SupplierID: supplier, ID: &group})
	if err != nil || !found {
		return syncer.Claim{}, err
	}
	return claimOf(row), nil
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
	return dropClaim(ctx, tx, group, c.ID)
}

// DropClaim ends the group's claim if it is still claim, in the caller's transaction: ErrClaimLost when it is not.
func DropClaim(ctx context.Context, tx pg.Tx, group, claim uuid.UUID) error {
	n, err := store.New().DropKeyClaim(ctx, tx, store.DropKeyClaimParams{ID: group, ClaimID: &claim})
	switch {
	case err != nil:
		return fmt.Errorf("end the group's claim: %w", err)
	case n != 1:
		return ErrClaimLost
	}
	return nil
}

func dropClaim(ctx context.Context, tx pg.Tx, group, claim uuid.UUID) error {
	_, err := store.New().DropKeyClaim(ctx, tx, store.DropKeyClaimParams{ID: group, ClaimID: &claim})
	if err != nil {
		return fmt.Errorf("end the group's claim: %w", err)
	}
	return nil
}

// MadeKey is a key Axiom made or adopted, to store as the credential of its group: Status is the credential status it
// starts in, Policy what the supplier lets it call (nil is every model, no expiry), and Claim the group's claim it was
// obtained under (zero for one the sync's listing had, with no claim).
type MadeKey struct {
	Ref    string
	Text   secret.Secret
	Status string
	Policy *adapter.KeyPolicy
	Claim  uuid.UUID
}

// StoreMadeKey stores a made key as the group's credential, sealed to its row, in the caller's transaction under the
// supplier's row, and ends the claim it was obtained under. A key the group stores already is left as it is (stored
// false), the other path having stored it first; one stored under another group is ErrKeyBound. Otherwise the key is
// stored only while its claim is still the group's: ErrClaimLost when another holder or a person's release has replaced
// it, because that holder's key is the group's now. A credential the key was erased from is sealed again in place.
func StoreMadeKey(ctx context.Context, tx pg.Tx, keys *secret.Keyring, supplier, group uuid.UUID, k MadeKey) (id uuid.UUID, stored bool, err error) {
	current, err := GroupClaim(ctx, tx, supplier, group)
	if err != nil {
		return uuid.Nil(), false, err
	}
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
			return r.ID, false, endClaim(ctx, tx, group, current.ID, k.Claim)
		default:
			id, revive = r.ID, true
		}
	}
	if current.ID != k.Claim {
		return uuid.Nil(), false, ErrClaimLost
	}
	if err := sealMade(ctx, tx, keys, supplier, group, id, revive, k); err != nil {
		return uuid.Nil(), false, err
	}
	return id, true, endClaim(ctx, tx, group, current.ID, k.Claim)
}

// endClaim ends the claim a stored key was obtained under, and no other.
func endClaim(ctx context.Context, tx pg.Tx, group, current, claim uuid.UUID) error {
	if claim == uuid.Nil() || current != claim {
		return nil
	}
	return DropClaim(ctx, tx, group, claim)
}

// sealMade writes the made key into a new credential, or into the erased one of the same key.
func sealMade(ctx context.Context, tx pg.Tx, keys *secret.Keyring, supplier, group, id uuid.UUID, revive bool, k MadeKey) error {
	sealed := keys.Seal(k.Text, secret.Location{Table: "suppliers.credentials", RowID: id.String(), Purpose: "key"})
	version := int64(sealed.KeyVersion)
	var models []string
	var expires *time.Time
	if k.Policy != nil {
		models, expires = k.Policy.Models, k.Policy.ExpiresAt
	}
	q := store.New()
	var err error
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
		return fmt.Errorf("store key %s: %w", k.Ref, err)
	}
	return nil
}
