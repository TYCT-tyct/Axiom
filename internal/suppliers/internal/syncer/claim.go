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
// is only ever adopted, never sent again, unless the supplier answers a repeated create with the first key.

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
// held with nothing sent. Keyed is a group that holds a credential, and Stored is the key Axiom made that one of them
// is, when it is one: a write of the key this claim led to finds it stored by the other path.
type Claim struct {
	Group  uuid.UUID
	ID     uuid.UUID
	Mine   bool
	At     time.Time
	SentAt time.Time
	Ref    adapter.KeyRef
	Gone   bool
	Keyed  bool
	Stored adapter.KeyRef
}

// Claims is where the groups' claims are kept. Each call is a transaction of its own and none holds anything while the
// supplier is asked. Claim takes a claim when the group has none, or has one held for longer than a holder needs with
// nothing sent; every other call compares the claim's ID and answers false when it is no longer that claim.
type Claims interface {
	Claim(ctx context.Context, groupRef string) (Claim, error)
	// Send records that the holder is about to send the create.
	Send(ctx context.Context, c Claim) (bool, error)
	// Resend takes over a sent claim with no ID recorded, to send the create again.
	Resend(ctx context.Context, c Claim) (Claim, bool, error)
	// Know records the supplier's id of the key a sent claim made.
	Know(ctx context.Context, c Claim, ref adapter.KeyRef) (bool, error)
	// Drop ends the claim: nothing was made, or the key it names is gone.
	Drop(ctx context.Context, c Claim) error
}

// Obtained is a group's key as ObtainKey got it. Made is a key Axiom's own create made, unlimited and active; any
// other was found by its name and must pass the caller's checks. Claim is the claim to settle when the key is stored.
type Obtained struct {
	Key   adapter.KeyState
	Text  secret.Secret
	Made  bool
	Claim Claim
}

// ObtainKey gets the key of a group the sync or a person connects with a key Axiom makes, without ever making a second
// one: it adopts a key the claim knows or the supplier lists under the marker name, and sends a create only under a
// claim it holds. A failure to make or read the key leaves the claim as far as it got, except a refusal of the create
// itself, which made nothing. now is the caller's clock.
func ObtainKey(ctx context.Context, claims Claims, m adapter.Manager, target KeyTarget, now time.Time) (Obtained, error) {
	maker, ok := m.(adapter.KeyMaker)
	if !ok {
		return Obtained{}, adapter.ErrNoKeyMaker
	}
	o := obtaining{claims: claims, maker: maker, target: target, now: now}
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
		key, made, c, err := o.settle(ctx, c)
		if err != nil {
			return Obtained{}, err
		}
		text, err := m.KeySecret(ctx, key.Ref)
		if errors.Is(err, adapter.ErrKeyGone) {
			if err := claims.Drop(ctx, c); err != nil {
				return Obtained{}, err
			}
			continue
		}
		if err != nil {
			return Obtained{}, errors.Join(err, o.abandon(ctx, c))
		}
		return Obtained{Key: key, Text: text, Made: made, Claim: c}, nil
	}
	return Obtained{}, ErrKeyInProgress
}

// Abandon ends a claim an Obtained holds with nothing sent, for a caller that will not store the key it found by name:
// the next one finds it again. A claim whose create was sent stays.
func Abandon(ctx context.Context, claims Claims, got Obtained) error {
	return obtaining{claims: claims}.abandon(ctx, got.Claim)
}

type obtaining struct {
	claims Claims
	maker  adapter.KeyMaker
	target KeyTarget
	now    time.Time
}

func (o obtaining) abandon(ctx context.Context, c Claim) error {
	if !c.Mine || !c.SentAt.IsZero() {
		return nil
	}
	return o.claims.Drop(ctx, c)
}

// settle is the key a claim leads to: the one it knows, or one found or made under it.
func (o obtaining) settle(ctx context.Context, c Claim) (adapter.KeyState, bool, Claim, error) {
	switch {
	case c.Ref != "":
		return o.made(c.Ref), true, c, nil
	case c.Mine:
		return o.findOrMake(ctx, c)
	case c.SentAt.IsZero():
		return adapter.KeyState{}, false, c, ErrKeyInProgress
	}
	found, listed, err := o.maker.KeyNamed(ctx, o.target.GroupRef, o.target.Name)
	if err == nil && listed {
		known, err := o.claims.Know(ctx, c, found.Ref)
		if err != nil || !known {
			return adapter.KeyState{}, false, c, inProgress(err)
		}
		// The key under the marker name after the create was sent is the one it made.
		c.Ref = found.Ref
		return found, true, c, nil
	}
	// The create may still land: only a supplier that answers a repeat with the first key may be asked again.
	if replay := o.maker.CreateReplay(); replay <= 0 || !o.now.Before(c.SentAt.Add(replay)) {
		return adapter.KeyState{}, false, c, ErrKeyInProgress
	}
	c, taken, err := o.claims.Resend(ctx, c)
	if err != nil || !taken {
		return adapter.KeyState{}, false, c, inProgress(err)
	}
	return o.send(ctx, c)
}

// findOrMake is a held claim's key: one the supplier lists under the marker name already, or one made now. Nothing has
// been sent while the claim is held, so a failure to look drops it.
func (o obtaining) findOrMake(ctx context.Context, c Claim) (adapter.KeyState, bool, Claim, error) {
	found, listed, err := o.maker.KeyNamed(ctx, o.target.GroupRef, o.target.Name)
	if err != nil {
		return adapter.KeyState{}, false, c, errors.Join(err, o.claims.Drop(ctx, c))
	}
	if listed {
		return found, false, c, nil
	}
	sent, err := o.claims.Send(ctx, c)
	if err != nil || !sent {
		return adapter.KeyState{}, false, c, inProgress(err)
	}
	c.SentAt = o.now
	return o.send(ctx, c)
}

// send sends the create under a sent claim. Only the create's own refusal says nothing was made.
func (o obtaining) send(ctx context.Context, c Claim) (adapter.KeyState, bool, Claim, error) {
	ref, err := o.maker.CreateKey(ctx, o.target.GroupRef, o.target.Name)
	if err != nil {
		if refused(err) {
			err = errors.Join(err, o.claims.Drop(ctx, c))
		}
		return adapter.KeyState{}, false, c, err
	}
	known, err := o.claims.Know(ctx, c, ref)
	if err != nil || !known {
		return adapter.KeyState{}, false, c, inProgress(err)
	}
	c.Ref = ref
	return o.made(ref), true, c, nil
}

// made is a key Axiom's create made: no quota, expiry or model limit.
func (o obtaining) made(ref adapter.KeyRef) adapter.KeyState {
	return adapter.KeyState{Ref: ref, GroupRef: o.target.GroupRef, Name: o.target.Name, Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{}}
}

// refused is a create the supplier refused before making anything.
func refused(err error) bool {
	if errors.Is(err, adapter.ErrKeyMade) {
		return false
	}
	for _, r := range []error{adapter.ErrKeyNotPermitted, adapter.ErrKeyGroupUnavailable, adapter.ErrKeyRefused, adapter.ErrNoKeyMaker} {
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
