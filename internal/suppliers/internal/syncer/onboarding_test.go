package syncer

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// keyStore is a Store that also knows the groups to make keys for, which the real one reads from the tables.
type keyStore struct {
	*memStore
	bind      []BindTarget
	targets   []KeyTarget
	askedMake int
	askedBind int
}

func (s *keyStore) KeysToMake(context.Context) ([]KeyTarget, error) {
	s.askedMake++
	return s.targets, nil
}

func (s *keyStore) KeysToBind(context.Context, time.Time) ([]BindTarget, error) {
	s.askedBind++
	return s.bind, nil
}

// maker is a supplier that can make keys too, and records what it was asked for.
type maker struct {
	*supplier
	create func(groupRef, name string) (adapter.KeyRef, error)
	secret func(ref adapter.KeyRef) (secret.Secret, error)
	names  []string
}

func (m *maker) CreateKey(_ context.Context, groupRef, name string) (adapter.KeyRef, error) {
	m.names = append(m.names, groupRef+"/"+name)
	return m.create(groupRef, name)
}

func (m *maker) KeySecret(_ context.Context, ref adapter.KeyRef) (secret.Secret, error) {
	return m.secret(ref)
}

func keyEngine(m adapter.Manager, st Store) *Engine {
	return New(st, m, Defaults(), (&clock{t: t0}).now)
}

//nolint:cyclop // one make is checked from the request through the stored state
func TestKeysTaskMakesAKeyForAGroup(t *testing.T) {
	base := healthy()
	base.keys = func(adapter.Listing) (adapter.Page[adapter.KeyState], error) {
		return adapter.Page[adapter.KeyState]{Done: true}, nil
	}
	m2 := &maker{
		supplier: base, create: func(_, _ string) (adapter.KeyRef, error) { return "9", nil },
		secret: func(adapter.KeyRef) (secret.Secret, error) { return secret.New("s3"), nil },
	}
	st := &keyStore{memStore: newStore(), targets: []KeyTarget{{GroupRef: "1", Name: "axiom-1-2"}}}
	c := Commit{State: State{Version: 1}}
	if err := keyEngine(m2, st).keys(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	if len(m2.names) != 1 || m2.names[0] != "1/axiom-1-2" {
		t.Fatalf("made %v, want the group and its marker name", m2.names)
	}
	if len(c.KeyWrites.Made) != 1 {
		t.Fatalf("made %d credentials, want 1", len(c.KeyWrites.Made))
	}
	made := c.KeyWrites.Made[0]
	if made.Ref != "9" || made.GroupRef != "1" || made.Secret.Reveal() != "s3" || made.Status != adapter.KeyActive {
		t.Fatalf("made %+v", made)
	}
	if c.KeyPolicies == nil || len(*c.KeyPolicies) != 1 || (*c.KeyPolicies)[0].Policy == nil {
		t.Fatalf("policies %+v, want the made key with an empty policy", c.KeyPolicies)
	}
	if len(c.State.Keys) != 1 || c.State.Keys[0].Name != "" || c.State.Keys[0].Policy != nil {
		t.Fatalf("state keys %+v, want the key without the name or policy", c.State.Keys)
	}
}

func TestKeysTaskAdoptsAMadeKeyByName(t *testing.T) {
	m := healthy()
	m.keys = func(adapter.Listing) (adapter.Page[adapter.KeyState], error) {
		return adapter.Page[adapter.KeyState]{Done: true, Items: []adapter.KeyState{{
			Ref: "9", GroupRef: "1", Name: "axiom-1-2", Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{},
		}}}, nil
	}
	called := 0
	m2 := &maker{supplier: m, create: func(_, _ string) (adapter.KeyRef, error) {
		called++
		return "10", nil
	}, secret: func(adapter.KeyRef) (secret.Secret, error) { return secret.New("s3"), nil }}
	st := &keyStore{memStore: newStore(), targets: []KeyTarget{{GroupRef: "1", Name: "axiom-1-2"}}}
	c := Commit{State: State{Version: 1}}
	if err := keyEngine(m2, st).keys(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatalf("made %d keys, want to adopt the one the listing has", called)
	}
	if len(c.KeyWrites.Made) != 1 || c.KeyWrites.Made[0].Ref != "9" {
		t.Fatalf("made %+v, want the listed key adopted", c.KeyWrites.Made)
	}
	if len(c.KeyWrites.Made[0].Secret.Reveal()) == 0 {
		t.Fatal("the adopted key has no text")
	}
}

func TestKeysTaskRecordsAFixedKeyFailure(t *testing.T) {
	m := healthy()
	m.keys = func(adapter.Listing) (adapter.Page[adapter.KeyState], error) {
		return adapter.Page[adapter.KeyState]{Done: true}, nil
	}
	m2 := &maker{supplier: m, create: func(_, _ string) (adapter.KeyRef, error) {
		return "", adapter.ErrKeyNotPermitted
	}, secret: func(adapter.KeyRef) (secret.Secret, error) { return secret.Secret{}, errors.New("unused") }}
	st := &keyStore{memStore: newStore(), targets: []KeyTarget{{GroupRef: "1", Name: "axiom-1-2"}}}
	c := Commit{State: State{Version: 1}}
	if err := keyEngine(m2, st).keys(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.KeyWrites.Failed) != 1 {
		t.Fatalf("failed %+v, want one", c.KeyWrites.Failed)
	}
	failed := c.KeyWrites.Failed[0]
	if failed.GroupRef != "1" || failed.Reason != "not_permitted" || !failed.RetryAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("failed %+v", failed)
	}
	if len(c.KeyWrites.Made) != 0 {
		t.Fatalf("made %+v, want none", c.KeyWrites.Made)
	}
}

func TestKeysTaskMakesNothingWithoutAKeyMaker(t *testing.T) {
	m := healthy()
	m.keys = func(adapter.Listing) (adapter.Page[adapter.KeyState], error) {
		return adapter.Page[adapter.KeyState]{Done: true}, nil
	}
	st := &keyStore{memStore: newStore(), targets: []KeyTarget{{GroupRef: "1", Name: "axiom-1-2"}}}
	c := Commit{State: State{Version: 1}}
	if err := keyEngine(m, st).keys(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.KeyWrites.Made) != 0 || len(c.KeyWrites.Failed) != 0 {
		t.Fatalf("wrote %+v, want nothing", c.KeyWrites)
	}
}

func TestKeysTaskOnboardsOnAResumedCompleteListing(t *testing.T) {
	m := healthy()
	m.keys = func(adapter.Listing) (adapter.Page[adapter.KeyState], error) {
		return adapter.Page[adapter.KeyState]{Done: true}, nil
	}
	called := 0
	m2 := &maker{supplier: m, create: func(_, _ string) (adapter.KeyRef, error) {
		called++
		return "9", nil
	}, secret: func(adapter.KeyRef) (secret.Secret, error) { return secret.New("s9"), nil }}
	st := &keyStore{memStore: newStore(), targets: []KeyTarget{{GroupRef: "1", Name: "axiom-1-2"}}}
	c := Commit{State: State{Version: 1, KeysAt: adapter.Listing("page")}}
	if err := keyEngine(m2, st).keys(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	if called != 1 || len(c.KeyWrites.Made) != 1 {
		t.Fatalf("made %d keys, wrote %+v; want one", called, c.KeyWrites.Made)
	}
}

// TestKeysTaskKeepsPoliciesWhenALaterKeyReadIsBlocked: the first bound key is read, the second meets a full bucket.
// The commit still runs (a blocked step keeps its page position), so it must carry the listing policy for the first
// key; otherwise activating it would leave allowed_models NULL, which routing reads as unrestricted.
func TestKeysTaskKeepsPoliciesWhenALaterKeyReadIsBlocked(t *testing.T) {
	m := healthy()
	m.keys = func(adapter.Listing) (adapter.Page[adapter.KeyState], error) {
		return adapter.Page[adapter.KeyState]{Done: true, Items: []adapter.KeyState{{
			Ref: "8", GroupRef: "1", Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{Models: []string{"a"}},
		}}}, nil
	}
	first, second := uuid.NewV7(), uuid.NewV7()
	m2 := &maker{supplier: m, secret: func(ref adapter.KeyRef) (secret.Secret, error) {
		if ref == "8" {
			return secret.New("s8"), nil
		}
		return secret.Secret{}, adapter.Blocked{Until: t0.Add(time.Minute)}
	}}
	st := &keyStore{memStore: newStore(), bind: []BindTarget{
		{ID: first, Ref: "8", GroupRef: "1"},
		{ID: second, Ref: "10", GroupRef: "1"},
	}}
	c := Commit{State: State{Version: 1}}
	err := keyEngine(m2, st).keys(t.Context(), &c)
	if !errors.Is(err, adapter.ErrBucketsFull) {
		t.Fatalf("keys = %v, want a blocked read", err)
	}
	if len(c.KeyWrites.Bound) != 1 || c.KeyWrites.Bound[0].ID != first {
		t.Fatalf("bound %+v, want only the first key", c.KeyWrites.Bound)
	}
	if c.KeyPolicies == nil || len(*c.KeyPolicies) != 1 || (*c.KeyPolicies)[0].Ref != "8" ||
		!slices.Equal((*c.KeyPolicies)[0].Policy.Models, []string{"a"}) {
		t.Fatalf("policies %+v, want the listing's policy for the first key", c.KeyPolicies)
	}
}

func TestKeysTaskReadsAndActivatesABoundKey(t *testing.T) {
	m := healthy()
	m.keys = func(adapter.Listing) (adapter.Page[adapter.KeyState], error) {
		return adapter.Page[adapter.KeyState]{Done: true, Items: []adapter.KeyState{{
			Ref: "8", GroupRef: "1", Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{Models: []string{"m1"}},
		}}}, nil
	}
	id := uuid.NewV7()
	m2 := &maker{supplier: m, secret: func(adapter.KeyRef) (secret.Secret, error) { return secret.New("s8"), nil }}
	st := &keyStore{memStore: newStore(), bind: []BindTarget{{ID: id, Ref: "8", GroupRef: "1"}}}
	c := Commit{State: State{Version: 1}}
	if err := keyEngine(m2, st).keys(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.KeyWrites.Bound) != 1 || c.KeyWrites.Bound[0].ID != id || c.KeyWrites.Bound[0].Secret.Reveal() != "s8" {
		t.Fatalf("bound %+v", c.KeyWrites.Bound)
	}
	if len(c.State.Keys) != 1 || c.State.Keys[0].Policy != nil {
		t.Fatalf("state keys %+v, want only the listed key", c.State.Keys)
	}
}

func TestKeysTaskKeepsABoundKeyTheListingDoesNotHave(t *testing.T) {
	m := healthy()
	m.keys = func(adapter.Listing) (adapter.Page[adapter.KeyState], error) {
		return adapter.Page[adapter.KeyState]{Done: true}, nil
	}
	id := uuid.NewV7()
	m2 := &maker{supplier: m, secret: func(adapter.KeyRef) (secret.Secret, error) { return secret.New("s8"), nil }}
	st := &keyStore{memStore: newStore(), bind: []BindTarget{{ID: id, Ref: "8", GroupRef: "1"}}}
	c := Commit{State: State{Version: 1}}
	if err := keyEngine(m2, st).keys(t.Context(), &c); err != nil {
		t.Fatal(err)
	}
	if c.KeyPolicies == nil || len(*c.KeyPolicies) != 0 {
		t.Fatalf("policies %+v, want the listing alone", c.KeyPolicies)
	}
	if len(c.State.Keys) != 1 || c.State.Keys[0].Ref != "8" || c.State.Keys[0].Status != adapter.KeyActive {
		t.Fatalf("state keys %+v, want the bound key in the state only", c.State.Keys)
	}
}
