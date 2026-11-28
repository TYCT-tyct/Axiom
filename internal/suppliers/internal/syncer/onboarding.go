package syncer

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// onboard reconciles the keys Axiom made at a supplier and reads the text of the ones a person bound by reference
// (docs/domains/suppliers.md「接入流程」). It runs on a complete listing whose pages all carry their names, never inside a
// transaction: a key exists at the supplier before the state says so, and the marker name is what lets the next round
// adopt it instead of making a second one.
//
// The sync is the only writer of keys at a supplier, one node at a time under the supplier's lease, and this is its one
// place that makes them, recording each as it is made. Its invariant: after a reconcile, a group has at most one live
// key on that record, and when Axiom stores one, that is the one; a key not on the record is never revoked. A listing
// that is not complete never reaches here, so a key it might not show is never made again.
//
// policies is the key listing that will back the credentials' policies. A made key is added to it with an empty policy,
// because that is how it was made; a bound key the listing does not have is left out, so its policy stays "nothing
// allowed" until a complete listing reports it. The returned keys are for the state only.
func (e *Engine) onboard(ctx context.Context, c *Commit, policies *[]adapter.KeyState) ([]adapter.KeyState, error) {
	store, ok := e.store.(KeyStore)
	if !ok {
		return nil, nil
	}
	groups, err := store.KeyGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("syncer: the groups to reconcile keys for: %w", err)
	}
	bind, err := store.KeysToBind(ctx, e.now())
	if err != nil {
		return nil, fmt.Errorf("syncer: the keys to bind: %w", err)
	}
	run := &keyOnboarding{
		e: e, c: c, store: store, policies: policies, retry: e.now().Add(e.set.KeyRetry),
		marked: markedKeys(*policies), byRef: make(map[adapter.KeyRef]bool, len(*policies)),
		stored: make(map[adapter.KeyRef]bool),
	}
	for _, k := range *policies {
		run.byRef[k.Ref] = true
	}
	for _, g := range groups {
		for _, ref := range g.Stored {
			run.stored[ref] = true
		}
	}
	run.maker, run.canMake = e.mgr.(adapter.KeyMaker)
	for _, g := range groups {
		if err := run.reconcile(ctx, g); err != nil {
			return nil, err
		}
	}
	return run.bindKeys(ctx, bind)
}

// keyOnboarding is one keys step: the listing that backs the policies, its keys by group and name, and the maker the
// adapter has. A group that fails a fixed way is left for the retry; a task failure stops the step.
type keyOnboarding struct {
	e        *Engine
	c        *Commit
	store    KeyStore
	policies *[]adapter.KeyState
	marked   map[groupName][]adapter.KeyState
	byRef    map[adapter.KeyRef]bool
	stored   map[adapter.KeyRef]bool // every key the supplier's credentials hold or wait for, in any group
	maker    adapter.KeyMaker
	canMake  bool
	retry    time.Time
}

// groupName is a listed key's group and name: a key of ours is one whose name is its own group's marker.
type groupName struct{ group, name string }

// markedKeys are the listed keys that have a name, by group and name, the oldest first. new-api's and sub2api's ids are
// decimal numbers without leading zeros, so the shorter is the older and of two as long the first in order; any other
// supplier's are only put in a fixed order.
func markedKeys(listed []adapter.KeyState) map[groupName][]adapter.KeyState {
	out := make(map[groupName][]adapter.KeyState)
	for _, k := range listed {
		if k.Name != "" {
			at := groupName{k.GroupRef, k.Name}
			out[at] = append(out[at], k)
		}
	}
	for _, keys := range out {
		slices.SortFunc(keys, func(a, b adapter.KeyState) int {
			return cmp.Or(cmp.Compare(len(a.Ref), len(b.Ref)), strings.Compare(string(a.Ref), string(b.Ref)))
		})
	}
	return out
}

// reconcile brings one group to its invariant. A group that needs a key adopts the oldest of its marked keys that the
// supplier lists as active, as a pasted key must be, or makes one when none is; every other key on the record of those
// Axiom made for it that the listing has is revoked. A name is enough to adopt a key, never to revoke one: a person can
// name a key of their own the same. A stored key is never revoked.
func (o *keyOnboarding) reconcile(ctx context.Context, g KeyGroup) error {
	keep, err := o.adopt(ctx, g, o.marked[groupName{g.GroupRef, g.Name}])
	if err != nil {
		return err
	}
	if done, err := o.revokeAll(ctx, g, keep); err != nil || !done {
		return err
	}
	if !g.Make || keep != "" {
		return nil
	}
	ref, made, err := o.create(ctx, g)
	if err != nil || !made {
		return err
	}
	return o.keep(ctx, g, ref, true)
}

// adopt keeps the oldest active marked key of a group that needs one, and answers its ref; none when the group needs no
// key or has no such key.
func (o *keyOnboarding) adopt(ctx context.Context, g KeyGroup, marked []adapter.KeyState) (adapter.KeyRef, error) {
	if !g.Make {
		return "", nil
	}
	i := slices.IndexFunc(marked, func(k adapter.KeyState) bool { return k.Status == adapter.KeyActive })
	if i < 0 {
		return "", nil
	}
	return marked[i].Ref, o.keep(ctx, g, marked[i].Ref, false)
}

// revokeAll revokes every key made for the group that the listing has, but the one kept and those a credential held when
// the step began; the store checks the last again under the supplier's lock. A made key the complete listing does not
// have is gone already. done is false when nothing more can be revoked this step.
func (o *keyOnboarding) revokeAll(ctx context.Context, g KeyGroup, keep adapter.KeyRef) (done bool, err error) {
	for _, ref := range g.Made {
		if ref == keep || o.stored[ref] || !o.byRef[ref] {
			continue
		}
		if done, err := o.revoke(ctx, g, ref); err != nil || !done {
			return false, err
		}
	}
	return true, nil
}

// keep reads the text of the group's key and adds it to the state, and a key made now to the policies too: a listed
// one is there with what the supplier lists for it.
func (o *keyOnboarding) keep(ctx context.Context, g KeyGroup, ref adapter.KeyRef, made bool) error {
	text, err := o.e.mgr.KeySecret(ctx, ref)
	if err != nil {
		if reason, failed := keyFailure(err); failed {
			o.fail(g.GroupRef, reason)
			return nil
		}
		return err
	}
	if made {
		key := adapter.KeyState{Ref: ref, GroupRef: g.GroupRef, Name: g.Name, Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{}}
		*o.policies = append(*o.policies, key)
		o.byRef[ref] = true
	}
	o.c.KeyWrites.Made = append(o.c.KeyWrites.Made, KeyMade{Ref: ref, GroupRef: g.GroupRef, Status: adapter.KeyActive, Secret: text})
	return nil
}

// revoke deletes a key Axiom made that no credential holds, through the store, which checks that under the supplier's
// lock and records it. done is false when the adapter has no maker, so nothing more is revoked this step; a key the
// supplier would not delete is left for the next reconcile.
func (o *keyOnboarding) revoke(ctx context.Context, g KeyGroup, ref adapter.KeyRef) (done bool, err error) {
	if !o.canMake {
		return false, nil
	}
	_, err = o.store.Revoke(ctx, g.GroupRef, ref, func(ctx context.Context) error { return o.maker.DeleteKey(ctx, ref) })
	if errors.Is(err, adapter.ErrNoKeyMaker) {
		o.canMake = false
		return false, nil
	}
	if _, refused := keyFailure(err); refused {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("syncer: revoke key %s: %w", ref, err)
	}
	return true, nil
}

// create makes the key. made is false when there is no maker or the group failed a fixed way; then the step continues
// with the next group. A task failure is returned.
func (o *keyOnboarding) create(ctx context.Context, g KeyGroup) (ref adapter.KeyRef, made bool, err error) {
	if !o.canMake {
		return "", false, nil
	}
	ref, err = o.maker.CreateKey(ctx, g.GroupRef, g.Name)
	if err == nil {
		if err := o.store.Made(ctx, g.GroupRef, ref); err != nil {
			return "", false, fmt.Errorf("syncer: record the key made for group %s: %w", g.GroupRef, err)
		}
		return ref, true, nil
	}
	if errors.Is(err, adapter.ErrNoKeyMaker) {
		o.canMake = false
		return "", false, nil
	}
	if reason, failed := keyFailure(err); failed {
		o.fail(g.GroupRef, reason)
		return "", false, nil
	}
	return "", false, err
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
