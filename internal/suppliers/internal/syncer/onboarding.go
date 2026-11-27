package syncer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// onboard makes the keys a supplier needs (docs/domains/suppliers.md「接入流程」) and reads the text of the ones a
// person bound by reference. It runs on a complete listing whose pages all carry their names, never inside a
// transaction: a key exists at the supplier before the state says so, and the marker name is what lets the next round
// adopt it instead of making a second one.
//
// policies is the key listing that will back the credentials' policies. A made key is added to it with an empty policy,
// because that is how it was made; a bound key the listing does not have is left out, so its policy stays "nothing
// allowed" until a complete listing reports it. The returned keys are for the state only.
func (e *Engine) onboard(ctx context.Context, c *Commit, policies *[]adapter.KeyState) ([]adapter.KeyState, error) {
	store, ok := e.store.(KeyStore)
	if !ok {
		return nil, nil
	}
	targets, err := store.KeysToMake(ctx)
	if err != nil {
		return nil, fmt.Errorf("syncer: the groups to make keys for: %w", err)
	}
	bind, err := store.KeysToBind(ctx, e.now())
	if err != nil {
		return nil, fmt.Errorf("syncer: the keys to bind: %w", err)
	}
	now := e.now()
	run := &keyOnboarding{
		e: e, c: c, policies: policies, retry: now.Add(e.set.KeyRetry), claims: store.Claims(),
		byName: make(map[string]adapter.KeyState, len(*policies)),
		byRef:  make(map[adapter.KeyRef]bool, len(*policies)),
	}
	for _, k := range *policies {
		run.byRef[k.Ref] = true
		if k.Name != "" {
			run.byName[k.Name] = k
		}
	}
	_, run.canMake = e.mgr.(adapter.KeyMaker)
	for _, target := range targets {
		if err := run.makeKey(ctx, target); err != nil {
			return nil, err
		}
	}
	return run.bindKeys(ctx, bind)
}

// keyOnboarding is one keys step: the listing that backs the policies, the keys it already names, and the maker the
// adapter has. A group that fails a fixed way is left for the retry; a task failure stops the step.
type keyOnboarding struct {
	e        *Engine
	c        *Commit
	policies *[]adapter.KeyState
	byName   map[string]adapter.KeyState
	byRef    map[adapter.KeyRef]bool
	claims   Claims
	canMake  bool
	retry    time.Time
}

// makeKey adopts the key the listing has under the group's marker name, or obtains one under the group's claim, reads
// its text and adds it to the state and the policies. A name that belongs to another group is not evidence of this key.
func (o *keyOnboarding) makeKey(ctx context.Context, target KeyTarget) error {
	key, adopted := o.find(target)
	var text secret.Secret
	var err error
	if adopted {
		text, err = o.e.mgr.KeySecret(ctx, key.Ref)
	} else {
		var got Obtained
		if got, err = o.obtain(ctx, target); err != nil || got.Key.Ref == "" {
			return err
		}
		key, text = got.Key, got.Text
	}
	if err != nil {
		if reason, failed := keyFailure(err); failed {
			o.fail(target.GroupRef, reason)
			return nil
		}
		return err
	}
	if !o.byRef[key.Ref] {
		*o.policies = append(*o.policies, key)
		o.byRef[key.Ref] = true
	}
	o.c.KeyWrites.Made = append(o.c.KeyWrites.Made, KeyMade{Ref: key.Ref, GroupRef: target.GroupRef, Status: key.Status, Secret: text})
	return nil
}

// find is the listed key with this group's marker name, if one belongs to the group. A key of another group with the
// same name is not adopted.
func (o *keyOnboarding) find(target KeyTarget) (adapter.KeyState, bool) {
	found, adopted := o.byName[target.Name]
	if adopted && found.GroupRef != target.GroupRef {
		return adapter.KeyState{}, false
	}
	return found, adopted
}

// obtain gets the group's key under its claim. A group whose key someone else is making, or that has a key by now, is
// left alone this round, and so is every group once the adapter turns out to make none; a group that failed a fixed
// way waits for the retry. The key is zero then, and the error is a task failure's.
func (o *keyOnboarding) obtain(ctx context.Context, target KeyTarget) (Obtained, error) {
	if !o.canMake {
		return Obtained{}, nil
	}
	got, err := ObtainKey(ctx, o.claims, o.e.mgr, target, o.e.now())
	switch {
	case err == nil:
		return got, nil
	case errors.Is(err, adapter.ErrNoKeyMaker):
		o.canMake = false
	case errors.Is(err, ErrKeyInProgress), errors.Is(err, ErrGroupKeyed), errors.Is(err, ErrGroupUnlisted):
	default:
		reason, failed := keyFailure(err)
		if !failed {
			return Obtained{}, err
		}
		o.fail(target.GroupRef, reason)
	}
	return Obtained{}, nil
}

// bindKeys reads the text of the credentials people bound by reference. A key the listing does not have is kept in the
// state only, so its policy stays "nothing allowed" until a listing reports it.
func (o *keyOnboarding) bindKeys(ctx context.Context, targets []BindTarget) ([]adapter.KeyState, error) {
	var extra []adapter.KeyState
	for _, target := range targets {
		text, err := o.e.mgr.KeySecret(ctx, target.Ref)
		if err != nil {
			if reason, failed := keyFailure(err); failed {
				o.fail(target.GroupRef, reason)
				continue
			}
			return nil, err
		}
		o.c.KeyWrites.Bound = append(o.c.KeyWrites.Bound, KeyBound{ID: target.ID, Ref: target.Ref, GroupRef: target.GroupRef, Secret: text})
		if !o.byRef[target.Ref] {
			extra = append(extra, adapter.KeyState{Ref: target.Ref, GroupRef: target.GroupRef, Status: adapter.KeyActive})
		}
	}
	return extra, nil
}

// fail records a fixed key failure for a group, to be tried again after the retry interval.
func (o *keyOnboarding) fail(groupRef, reason string) {
	o.c.KeyWrites.Failed = append(o.c.KeyWrites.Failed, KeyFailure{GroupRef: groupRef, Reason: reason, RetryAt: o.retry})
}

// keyFailure names the fixed reason of a key error the group can be left waiting on, or says it is a task failure.
func keyFailure(err error) (string, bool) {
	switch {
	case errors.Is(err, adapter.ErrKeyNotPermitted):
		return "not_permitted", true
	case errors.Is(err, adapter.ErrKeyGroupUnavailable):
		return "group_unavailable", true
	case errors.Is(err, adapter.ErrKeyRefused):
		return "refused", true
	}
	return "", false
}
