package syncer

import (
	"context"
	"errors"
	"fmt"

	"axiom/internal/suppliers/internal/adapter"
)

// onboard makes the keys a supplier needs (docs/domains/suppliers.md「接入流程」), reads the text of the ones a person
// bound by reference, and reports the keys a complete listing no longer has. It runs only on a complete listing whose
// pages all carry their names, never inside a transaction: a key exists at the supplier before the state says so, and
// the marker name is what lets the next round adopt it instead of making a second one.
//
// policies is the key listing that will back the credentials' policies. A made key is added to it with an empty policy,
// because that is how it was made; a bound key the listing does not have is left out, so its policy stays "nothing
// allowed" until a complete listing reports it. The returned keys are for the state only.
func (e *Engine) onboard(ctx context.Context, c *Commit, policies *[]adapter.KeyState, gone []adapter.KeyRef) ([]adapter.KeyState, error) {
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
	c.KeyWrites.Lost = gone
	c.KeyWrites.StaleBefore = e.now().Add(-e.set.KeyGrace)
	retry := e.now().Add(e.set.KeyRetry)
	byRef := make(map[adapter.KeyRef]bool, len(*policies))
	byName := make(map[string]adapter.KeyState, len(*policies))
	for _, k := range *policies {
		byRef[k.Ref] = true
		if k.Name != "" {
			byName[k.Name] = k
		}
	}
	maker, canMake := e.mgr.(adapter.KeyMaker)
	for _, target := range targets {
		found, adopted := byName[target.Name]
		if adopted && found.GroupRef != target.GroupRef {
			adopted = false // a name that belongs to another group is not evidence of this key
		}
		ref, status := found.Ref, found.Status
		if !adopted {
			if !canMake {
				continue
			}
			ref, err = maker.CreateKey(ctx, target.GroupRef, target.Name)
			if err != nil {
				if errors.Is(err, adapter.ErrNoKeyMaker) {
					canMake = false
					continue
				}
				if reason, failed := keyFailure(err); failed {
					c.KeyWrites.Failed = append(c.KeyWrites.Failed, KeyFailure{GroupRef: target.GroupRef, Reason: reason, RetryAt: retry})
					continue
				}
				return nil, err
			}
			status = adapter.KeyActive
		}
		text, err := e.mgr.KeySecret(ctx, ref)
		if err != nil {
			if reason, failed := keyFailure(err); failed {
				c.KeyWrites.Failed = append(c.KeyWrites.Failed, KeyFailure{GroupRef: target.GroupRef, Reason: reason, RetryAt: retry})
				continue
			}
			return nil, err
		}
		if !adopted {
			key := adapter.KeyState{Ref: ref, GroupRef: target.GroupRef, Name: target.Name, Status: status, Policy: &adapter.KeyPolicy{}}
			*policies = append(*policies, key)
			byRef[ref] = true
		}
		c.KeyWrites.Made = append(c.KeyWrites.Made, KeyMade{Ref: ref, GroupRef: target.GroupRef, Status: status, Secret: text})
	}
	var extra []adapter.KeyState
	for _, target := range bind {
		text, err := e.mgr.KeySecret(ctx, target.Ref)
		if err != nil {
			if reason, failed := keyFailure(err); failed {
				c.KeyWrites.Failed = append(c.KeyWrites.Failed, KeyFailure{GroupRef: target.GroupRef, Reason: reason, RetryAt: retry})
				continue
			}
			return nil, err
		}
		c.KeyWrites.Bound = append(c.KeyWrites.Bound, KeyBound{ID: target.ID, Ref: target.Ref, GroupRef: target.GroupRef, Secret: text})
		if !byRef[target.Ref] {
			extra = append(extra, adapter.KeyState{Ref: target.Ref, GroupRef: target.GroupRef, Status: adapter.KeyActive})
		}
	}
	return extra, nil
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
