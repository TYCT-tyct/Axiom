package syncer

import (
	"context"
	"errors"
	"time"
	"uuid"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// Axiom makes at most one key at the supplier for a group, across nodes, retries, the sync and a person's make
// (docs/domains/suppliers.md「一个分组只建一把」). A group's claim says who may send the one create and how far it got:
// held (nothing sent yet), sent (the result is unknown) or known (the supplier's id of the key is recorded). A sent claim
// is only ever adopted, once the supplier lists the key, or released by a person; it is never sent again.

// The ways a group's key cannot be obtained now, which are no failure of the group's.
var (
	// ErrKeyInProgress is a group whose key another holder is making, or whose create was sent with a result not known
	// yet: it is adopted once the supplier lists it, or after a person releases the claim.
	ErrKeyInProgress = errors.New("syncer: the group's key is being made, or its create was sent and its result is not known")
	// ErrGroupKeyed is a group with a credential already.
	ErrGroupKeyed = errors.New("syncer: the group has a credential")
	// ErrGroupUnlisted is a group the supplier no longer lists.
	ErrGroupUnlisted = errors.New("syncer: the supplier no longer lists the group")
)

// Claim is a group's claim as one read of it found it. ID is zero when there is none; Mine is a claim this read took,
// held with nothing sent; Listing is where a held claim's search by name stopped. Erased are the keys of the group's
// erased credentials, which may still be live at the supplier. Keyed is a group that holds a credential, and Stored is
// the key Axiom made that one of them is, when it is one.
type Claim struct {
	Group   uuid.UUID
	ID      uuid.UUID
	Mine    bool
	At      time.Time
	SentAt  time.Time
	Ref     adapter.KeyRef
	Listing adapter.Listing
	Erased  []adapter.KeyRef
	Gone    bool
	Keyed   bool
	Stored  adapter.KeyRef
}

// Claims is where the groups' claims are kept. Each call is a transaction of its own and none holds anything while the
// supplier is asked. Claim takes a claim when the group has none, or has one held for longer than a holder needs with
// nothing sent; every other call compares the claim's ID and answers false when it is no longer that claim.
type Claims interface {
	Claim(ctx context.Context, groupRef string) (Claim, error)
	// Send records that the holder is about to send the create, and when.
	Send(ctx context.Context, c Claim) (Claim, bool, error)
	// Know records the supplier's id of the key a sent claim made.
	Know(ctx context.Context, c Claim, ref adapter.KeyRef) (bool, error)
	// Park keeps where a held claim's search by name stopped, for whoever goes on with it.
	Park(ctx context.Context, c Claim, at adapter.Listing) error
	// Drop ends the claim: nothing was made, or the key it names is gone.
	Drop(ctx context.Context, c Claim) error
}

// Obtained is a group's key as ObtainKey got it: Key as the supplier lists it, with its status and what it may call,
// which the caller checks as it checks a pasted key. Claim is the claim the key is stored under.
type Obtained struct {
	Key   adapter.KeyState
	Text  secret.Secret
	Claim Claim
}

// ObtainKey gets the key of a group the sync or a person connects with a key Axiom makes, without ever making a second
// one: it adopts the key the claim knows, a key of the group's erased credentials the supplier still has, or one it
// lists under the marker name, and sends a create only under a claim it holds. A failure leaves the claim as far as it
// got, except where nothing was sent: a refusal of the create itself, or a create the rate buckets kept back.
func ObtainKey(ctx context.Context, claims Claims, m adapter.Manager, target KeyTarget) (Obtained, error) {
	maker, ok := m.(adapter.KeyMaker)
	if !ok {
		return Obtained{}, adapter.ErrNoKeyMaker
	}
	o := obtaining{claims: claims, maker: maker, target: target}
	// A recorded id the supplier answers it no longer has is forgotten, and the key obtained once more.
	for range 2 {
		c, err := claims.Claim(ctx, target.GroupRef)
		switch {
		case err != nil:
			return Obtained{}, err
		case c.Gone:
			return Obtained{}, ErrGroupUnlisted
		case c.Keyed:
			return Obtained{}, ErrGroupKeyed
		}
		key, listed, c, err := o.settle(ctx, c)
		if err == nil && !listed {
			key, listed, err = o.listedAs(ctx, key.Ref, c)
		}
		switch {
		case err != nil:
			return Obtained{}, err
		case !listed:
			continue
		}
		text, err := m.KeySecret(ctx, key.Ref)
		if err != nil {
			return Obtained{}, errors.Join(err, o.abandon(ctx, c))
		}
		return Obtained{Key: key, Text: text, Claim: c}, nil
	}
	return Obtained{}, ErrKeyInProgress
}

// listedAs is the key with the id as the supplier lists it now. found is false, and the claim ended, when the supplier
// answers it has no such key.
func (o obtaining) listedAs(ctx context.Context, ref adapter.KeyRef, c Claim) (adapter.KeyState, bool, error) {
	key, found, err := o.maker.KeyByRef(ctx, ref)
	switch {
	case err != nil:
		return adapter.KeyState{}, false, errors.Join(err, o.abandon(ctx, c))
	case !found:
		return adapter.KeyState{}, false, o.claims.Drop(ctx, c)
	}
	return key, true, nil
}

// Abandon ends a claim an Obtained holds with nothing sent, for a caller that will not store the key it found: the next
// one finds it again. A claim whose create was sent stays.
func Abandon(ctx context.Context, claims Claims, got Obtained) error {
	return obtaining{claims: claims}.abandon(ctx, got.Claim)
}

type obtaining struct {
	claims Claims
	maker  adapter.KeyMaker
	target KeyTarget
}

func (o obtaining) abandon(ctx context.Context, c Claim) error {
	if !c.Mine || !c.SentAt.IsZero() {
		return nil
	}
	return o.claims.Drop(ctx, c)
}

// settle is the key a claim leads to: the one it knows, or one found or made under it. listed is a key as the
// supplier lists it; any other is only its id.
func (o obtaining) settle(ctx context.Context, c Claim) (adapter.KeyState, bool, Claim, error) {
	switch {
	case c.Ref != "":
		return adapter.KeyState{Ref: c.Ref}, false, c, nil
	case c.Mine:
		return o.findOrMake(ctx, c)
	case c.SentAt.IsZero():
		return adapter.KeyState{}, false, c, ErrKeyInProgress
	}
	// The create was sent and may still land: only the key it made, once listed, ends the wait.
	found, listed, _, err := o.maker.KeyNamed(ctx, o.target.GroupRef, o.target.Name, nil)
	if err != nil || !listed {
		return adapter.KeyState{}, false, c, ErrKeyInProgress
	}
	known, err := o.claims.Know(ctx, c, found.Ref)
	if err != nil || !known {
		return adapter.KeyState{}, false, c, inProgress(err)
	}
	c.Ref = found.Ref
	return found, true, c, nil
}

// findOrMake is a held claim's key: one of the group's erased credentials the supplier still has, one it lists under
// the marker name, or one made now. Nothing has been sent while the claim is held, so a failure to look drops it, but a
// search the limits stopped keeps the claim and where it stopped.
func (o obtaining) findOrMake(ctx context.Context, c Claim) (adapter.KeyState, bool, Claim, error) {
	for _, ref := range c.Erased {
		key, found, err := o.maker.KeyByRef(ctx, ref)
		if err != nil {
			return adapter.KeyState{}, false, c, errors.Join(err, o.claims.Drop(ctx, c))
		}
		if found {
			return key, true, c, nil
		}
	}
	found, listed, next, err := o.maker.KeyNamed(ctx, o.target.GroupRef, o.target.Name, c.Listing)
	switch {
	case err != nil && next != nil:
		return adapter.KeyState{}, false, c, errors.Join(err, o.claims.Park(ctx, c, next))
	case err != nil:
		return adapter.KeyState{}, false, c, errors.Join(err, o.claims.Drop(ctx, c))
	case listed:
		return found, true, c, nil
	}
	c, sent, err := o.claims.Send(ctx, c)
	if err != nil || !sent {
		return adapter.KeyState{}, false, c, inProgress(err)
	}
	return o.send(ctx, c)
}

// send sends the create under a sent claim. Only the create's own refusal, or a create that never left, says nothing
// was made. A claim the other path settled meanwhile, with this very key, no longer records it: the write of the key
// finds the credential that path stored.
func (o obtaining) send(ctx context.Context, c Claim) (adapter.KeyState, bool, Claim, error) {
	ref, err := o.maker.CreateKey(ctx, o.target.GroupRef, o.target.Name)
	if err != nil {
		if unmade(err) {
			err = errors.Join(err, o.claims.Drop(ctx, c))
		}
		return adapter.KeyState{}, false, c, err
	}
	known, err := o.claims.Know(ctx, c, ref)
	if err != nil {
		return adapter.KeyState{}, false, c, err
	}
	if known {
		c.Ref = ref
	}
	return adapter.KeyState{Ref: ref}, false, c, nil
}

// unmade is a create that made nothing: refused by the supplier, or kept back before it was sent.
func unmade(err error) bool {
	if errors.Is(err, adapter.ErrKeyMade) {
		return false
	}
	for _, r := range []error{adapter.ErrNotSent, adapter.ErrKeyNotPermitted, adapter.ErrKeyGroupUnavailable, adapter.ErrKeyRefused, adapter.ErrNoKeyMaker} {
		if errors.Is(err, r) {
			return true
		}
	}
	return false
}

// inProgress is a claim another holder changed under this one, or the error that kept it from being read.
func inProgress(err error) error {
	if err != nil {
		return err
	}
	return ErrKeyInProgress
}
