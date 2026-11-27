package syncer

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// TestKeysTaskReconcilesTheKeysOfOurs: on a complete listing a group keeps at most one live key under its marker name.
// A stored key is kept and never revoked; a group that needs one adopts the oldest active key of its name, or makes one
// when none is usable; every other key of that name and group is revoked and recorded, and one the supplier will not
// delete waits for the next reconcile. A key of the name in another group is not the group's. An incomplete listing
// reconciles nothing.
func TestKeysTaskReconcilesTheKeysOfOurs(t *testing.T) {
	const name = "axiom-1-2"
	key := func(ref adapter.KeyRef, group string, status adapter.KeyStatus) adapter.KeyState {
		return adapter.KeyState{Ref: ref, GroupRef: group, Name: name, Status: status, Policy: &adapter.KeyPolicy{}}
	}
	cases := []struct {
		name    string
		group   KeyGroup
		listed  []adapter.KeyState
		refuse  adapter.KeyRef
		blocked bool
		stored  []adapter.KeyRef // the keys whose text the commit stores
		revoked []adapter.KeyRef
		creates int
	}{
		{
			name:  "a stored key is kept and the others revoked",
			group: KeyGroup{GroupRef: "1", Name: name, Stored: []adapter.KeyRef{"12"}},
			listed: []adapter.KeyState{
				key("9", "1", adapter.KeyActive), key("12", "1", adapter.KeyActive), key("30", "1", adapter.KeyDisabled),
			},
			revoked: []adapter.KeyRef{"9", "30"},
		},
		{
			name:  "a group without a key adopts the oldest active one",
			group: KeyGroup{GroupRef: "1", Name: name, Make: true},
			listed: []adapter.KeyState{
				key("12", "1", adapter.KeyActive), key("5", "1", adapter.KeyDisabled), key("9", "1", adapter.KeyActive),
			},
			stored: []adapter.KeyRef{"9"}, revoked: []adapter.KeyRef{"5", "12"},
		},
		{
			name:   "a group whose keys are all unusable revokes them and makes one",
			group:  KeyGroup{GroupRef: "1", Name: name, Make: true},
			listed: []adapter.KeyState{key("5", "1", adapter.KeyExpired)},
			stored: []adapter.KeyRef{"40"}, revoked: []adapter.KeyRef{"5"}, creates: 1,
		},
		{
			name:   "a key of the name in another group is left alone",
			group:  KeyGroup{GroupRef: "1", Name: name, Make: true},
			listed: []adapter.KeyState{key("5", "2", adapter.KeyActive)},
			stored: []adapter.KeyRef{"40"}, creates: 1,
		},
		{
			name:  "a key the supplier will not delete is left for the next reconcile",
			group: KeyGroup{GroupRef: "1", Name: name, Stored: []adapter.KeyRef{"12"}},
			listed: []adapter.KeyState{
				key("9", "1", adapter.KeyActive), key("12", "1", adapter.KeyActive), key("30", "1", adapter.KeyActive),
			},
			refuse: "9", revoked: []adapter.KeyRef{"30"},
		},
		{
			name:    "an incomplete listing does nothing",
			group:   KeyGroup{GroupRef: "1", Name: name, Make: true},
			listed:  []adapter.KeyState{key("9", "1", adapter.KeyActive), key("12", "1", adapter.KeyActive)},
			blocked: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reconcileOnce(t, c.group, c.listed, c.refuse, c.blocked)
			if !slices.Equal(got.stored, c.stored) || !slices.Equal(got.revoked, c.revoked) || got.creates != c.creates {
				t.Errorf("stored %v, revoked %v, made %d; want %v, %v, %d", got.stored, got.revoked, got.creates, c.stored, c.revoked, c.creates)
			}
			if c.blocked && (got.groupReads != 0 || got.deletes != 0) {
				t.Errorf("an incomplete listing read the groups %d times and asked %d deletes", got.groupReads, got.deletes)
			}
		})
	}
}

// reconciled is what one keys step did: the refs whose text it stores, those it revoked and recorded, how many keys it
// made, and how often it read the groups and asked for a delete.
type reconciled struct {
	stored, revoked              []adapter.KeyRef
	creates, groupReads, deletes int
}

// reconcileOnce runs one keys step over the listing (one a limit stopped when blocked) for one group, at a supplier
// that refuses to delete refuse and makes key 40.
func reconcileOnce(t *testing.T, g KeyGroup, listed []adapter.KeyState, refuse adapter.KeyRef, blocked bool) reconciled {
	t.Helper()
	base := healthy()
	base.keys = func(adapter.Listing) (adapter.Page[adapter.KeyState], error) {
		if blocked {
			return adapter.Page[adapter.KeyState]{Next: adapter.Listing("page 2"), Until: t0.Add(time.Minute)}, nil
		}
		return adapter.Page[adapter.KeyState]{Done: true, Items: slices.Clone(listed)}, nil
	}
	m := &maker{
		supplier: base, refuse: map[adapter.KeyRef]bool{refuse: true},
		create: func(_, _ string) (adapter.KeyRef, error) { return "40", nil },
		secret: func(ref adapter.KeyRef) (secret.Secret, error) { return secret.New("sk-" + string(ref)), nil },
	}
	asked := &deleting{maker: m}
	st := &keyStore{memStore: newStore(), targets: []KeyGroup{g}}
	commit := Commit{State: State{Version: 1}}
	if err := keyEngine(asked, st).keys(t.Context(), &commit); blocked != errors.Is(err, adapter.ErrBucketsFull) || (!blocked && err != nil) {
		t.Fatalf("keys = %v", err)
	}
	out := reconciled{revoked: st.revoked, creates: len(m.names), groupReads: st.askedGroups, deletes: len(asked.refs)}
	for _, k := range commit.KeyWrites.Made {
		out.stored = append(out.stored, k.Ref)
	}
	return out
}

// deleting is a maker that also notes every delete it is asked for, refused or not.
type deleting struct {
	*maker
	refs []adapter.KeyRef
}

func (d *deleting) DeleteKey(ctx context.Context, ref adapter.KeyRef) error {
	d.refs = append(d.refs, ref)
	return d.maker.DeleteKey(ctx, ref)
}
