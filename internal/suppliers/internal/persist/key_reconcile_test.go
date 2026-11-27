package persist_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/syncer"
)

// keyRelay is a relay's management side that makes keys in memory, as new-api does: a key exists from the moment the
// create arrives. held, when set, receives each create once the key is made and holds it until release is closed, so a
// person's change runs while it is in flight; onDelete, when set, runs after each key is deleted. Only its key listing is
// read; the balance and offer are limited for now.
type keyRelay struct {
	mu       sync.Mutex
	keys     map[adapter.KeyRef]adapter.KeyState
	next     int
	creates  int
	held     chan struct{}
	release  chan struct{}
	onDelete func(adapter.KeyRef)
}

func (r *keyRelay) CreateKey(_ context.Context, groupRef, name string) (adapter.KeyRef, error) {
	r.mu.Lock()
	r.creates++
	ref := r.add(groupRef, name)
	held, release := r.held, r.release
	r.mu.Unlock()
	if held != nil {
		held <- struct{}{}
		<-release
	}
	return ref, nil
}

// add makes a key of the name in the group; the caller holds mu.
func (r *keyRelay) add(groupRef, name string) adapter.KeyRef {
	r.next++
	ref := adapter.KeyRef(strconv.Itoa(100 + r.next))
	r.keys[ref] = adapter.KeyState{Ref: ref, GroupRef: groupRef, Name: name, Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{}}
	return ref
}

func (r *keyRelay) DeleteKey(_ context.Context, ref adapter.KeyRef) error {
	r.mu.Lock()
	_, ok := r.keys[ref]
	delete(r.keys, ref)
	hook := r.onDelete
	r.mu.Unlock()
	if !ok {
		return adapter.ErrKeyRefused
	}
	if hook != nil {
		hook(ref)
	}
	return nil
}

func (r *keyRelay) Keys(context.Context, adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	page := adapter.Page[adapter.KeyState]{Done: true}
	for _, k := range r.keys {
		page.Items = append(page.Items, k)
	}
	return page, nil
}

func (r *keyRelay) live() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var refs []string
	for ref := range r.keys {
		refs = append(refs, string(ref))
	}
	slices.Sort(refs)
	return refs
}

func (*keyRelay) KeySecret(_ context.Context, ref adapter.KeyRef) (secret.Secret, error) {
	return secret.New("sk-text-" + string(ref)), nil
}

func (*keyRelay) Account(context.Context) (adapter.Account, error) {
	return adapter.Account{}, adapter.Blocked{Until: t0.Add(time.Hour)}
}

func (*keyRelay) Offer(context.Context, adapter.Listing) (adapter.Page[adapter.Group], error) {
	return adapter.Page[adapter.Group]{}, adapter.Blocked{Until: t0.Add(time.Hour)}
}

func (*keyRelay) Charges(context.Context, adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	return adapter.ChargePage{}, nil, adapter.Blocked{Until: t0.Add(time.Hour)}
}

// keyMaking is the fake kind as an adapter whose managers make keys.
type keyMaking struct{ replaceableLogin }

func (keyMaking) MakesKeys() bool { return true }

// TestOnlyTheSyncMakesAGroupsKey: a person's make while the sync's create is in flight asks nothing of the relay. The
// change fences the round out, so the key that round made is stored nowhere, as when a create's answer is lost; the next
// round adopts it from the listing instead of making a second one, and the make sees it stored. A second key of the
// group's name, as a node whose lease ran out would leave, is revoked there and recorded, and the stored one is kept.
func TestOnlyTheSyncMakesAGroupsKey(t *testing.T) {
	t.Parallel()
	w := newKeyWorld(t)
	w.makeDuringARound()
	reach := suppliers.Reach{DB: w.c.open(), Scope: platform}
	w.expectPending(reach)
	if err := w.round("second"); err != nil {
		t.Fatalf("the next round: %v", err)
	}
	wait, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	g, stored, err := w.admin.AwaitKey(wait, reach, w.c.supplier, w.group)
	if err != nil || stored == nil {
		t.Fatalf("the make's wait = %v, %v; want the stored key", stored, err)
	}
	if w.relay.creates != 1 || stored.UpstreamKeyRef != "101" || !g.Selected || g.KeyState != "ready" {
		t.Fatalf("%d creates, stored %s, group %+v; want the one key made, stored and ready", w.relay.creates, stored.UpstreamKeyRef, g)
	}
	same(t, "the keys recorded as made", w.c.column("made_keys", "group_ref || ' ' || upstream_key_ref", "upstream_key_ref"), []string{"g 101"})
}

// TestOnlyKeysAxiomMadeAreRevoked: of the keys of a group's marker name, only those recorded as made by the sync are
// revoked, and only while no credential holds them when the revocation runs: a key of the name a person made is kept,
// and so is one a person stores while the round is already revoking. Each revocation is in the audit. Once the group is
// no longer in use and its credentials are erased, the keys made for it are revoked too.
func TestOnlyKeysAxiomMadeAreRevoked(t *testing.T) {
	t.Parallel()
	w := newKeyWorld(t)
	w.makeDuringARound()
	if err := w.round("second"); err != nil {
		t.Fatalf("the round that stores the key: %v", err)
	}
	name := persist.KeyName(w.c.supplier, "g")
	w.relay.mu.Lock()
	lapsed, pasted := w.relay.add("g", name), w.relay.add("g", name) // made by a node whose lease ran out
	w.relay.add("g", name)                                           // a person's, named like ours
	w.relay.onDelete = func(ref adapter.KeyRef) {
		if ref == lapsed { // the person stores the other one now, between the listing and its revocation
			w.c.exec(fmt.Sprintf(`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, status, max_concurrency, source)
				VALUES ('%s', '%s', '%s', '%s', 'binding', 4, 'bound')`, uuid.NewV7(), w.c.supplier, w.group, pasted))
		}
	}
	w.relay.mu.Unlock()
	for _, ref := range []adapter.KeyRef{lapsed, pasted} {
		w.c.exec(fmt.Sprintf(`INSERT INTO suppliers.made_keys (supplier_id, upstream_key_ref, group_ref, made_at) VALUES ('%s', '%s', 'g', now())`, w.c.supplier, ref))
	}
	w.nextRound("third")
	same(t, "the keys live after the reconcile", w.relay.live(), []string{"101", "103", "104"})

	w.unuse()
	w.nextRound("fourth")
	same(t, "the keys live once the group is no longer in use", w.relay.live(), []string{"104"})
	var revoked string
	w.c.value(&revoked, `SELECT string_agg(actor_id || ' ' || (detail->>'group_ref') || ' ' || (detail->>'upstream_key_ref'), ',' ORDER BY detail->>'upstream_key_ref')
		FROM platform.audit_log WHERE action = 'supplier_key.revoke'`)
	same(t, "what the audit says was revoked", []string{revoked}, []string{"suppliers.sync g 101,suppliers.sync g 102,suppliers.sync g 103"})
	same(t, "the keys still recorded as made", w.c.column("made_keys", "upstream_key_ref", "upstream_key_ref"), nil)
}

// TestAMakeIsRefusedWhileTheKeysTaskIsHalted: a keys task stopped on an answer the adapter does not know runs no round,
// so a make is refused with why, and the group is left as it was instead of waiting for a key that never comes.
func TestAMakeIsRefusedWhileTheKeysTaskIsHalted(t *testing.T) {
	t.Parallel()
	w := newKeyWorld(t)
	s := w.c.acquire(w.c.node("halter"))
	st, err := s.Store().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	halted := syncer.Commit{State: st}
	halted.State.Version++
	halted.State.Tasks[syncer.TaskKeys].Halted = true
	if err := s.Store().Commit(t.Context(), st.Version, halted); err != nil {
		t.Fatal(err)
	}
	err = w.c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := w.admin.RequestKey(t.Context(), tx, audit.User(uuid.NewV7()), w.c.supplier, w.group, "make the key")
		return err
	})
	if !errors.Is(err, suppliers.ErrKeysHalted) {
		t.Fatalf("a make while the keys task is halted = %v, want ErrKeysHalted", err)
	}
	same(t, "the groups selected", w.c.column("supplier_groups", "selected::text", "id"), []string{"false"})
}

// keyWorld is a relay with one listed group g, never chosen and one Axiom can sell, an admin whose adapter makes keys,
// and the relay's management side that holds its first create.
type keyWorld struct {
	t     *testing.T
	c     *cluster
	admin *suppliers.Admin
	relay *keyRelay
	group uuid.UUID
}

func newKeyWorld(t *testing.T) keyWorld {
	t.Helper()
	c := newCluster(t)
	lister := c.acquire(c.node("lister"))
	c.commitListing(lister, 1, []listedGroup{{ref: "g", name: "G", ratio: "1", facts: "{}"}})
	if err := lister.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	w := keyWorld{t: t, c: c, relay: &keyRelay{keys: map[adapter.KeyRef]adapter.KeyState{}, held: make(chan struct{}), release: make(chan struct{})}}
	c.value(&w.group, "SELECT id FROM suppliers.supplier_groups WHERE supplier_id = $1 AND upstream_ref = 'g'", c.supplier)
	c.exec(fmt.Sprintf("UPDATE suppliers.supplier_groups SET key_eligible = true WHERE id = '%s'", w.group))
	r, err := registry.New(keyMaking{})
	if err != nil {
		t.Fatal(err)
	}
	if w.admin, err = suppliers.NewAdmin(suppliers.AdminConfig{Keys: c.keys, Registry: r, Clock: c.clock.now}); err != nil {
		t.Fatal(err)
	}
	return w
}

// round is one round of the sync on a lease of its own, as a node runs it. It reports instead of failing the test, so
// it can run outside the test's goroutine.
func (w keyWorld) round(holder string) error {
	s, err := w.c.node(holder).Acquire(w.t.Context(), w.c.supplier)
	if err != nil {
		return err
	}
	defer func() { _ = s.Release(context.WithoutCancel(w.t.Context())) }() //nolint:errcheck // the lease ends with the test
	return syncer.New(s.Store(), w.relay, syncer.Defaults(), w.c.clock.now).Round(w.t.Context())
}

// makeDuringARound has a person ask for the key while the sync's create of it is in flight: the group was never chosen,
// so the sync is already making its key. The round is fenced out by the change.
func (w keyWorld) makeDuringARound() {
	w.t.Helper()
	fenced := make(chan error, 1)
	go func() { fenced <- w.round("first") }()
	select {
	case <-w.relay.held:
	case err := <-fenced:
		w.t.Fatalf("the round ended before it made the key: %v", err)
	}
	if err := w.c.open().Tx(w.t.Context(), platform, func(tx pg.Tx) error {
		_, err := w.admin.RequestKey(w.t.Context(), tx, audit.User(uuid.NewV7()), w.c.supplier, w.group, "make the key")
		return err
	}); err != nil {
		w.t.Fatalf("request the key: %v", err)
	}
	w.relay.mu.Lock()
	w.relay.held = nil
	w.relay.mu.Unlock()
	close(w.relay.release)
	if err := <-fenced; !errors.Is(err, syncer.ErrFenced) {
		w.t.Fatalf("the round in flight = %v, want fenced out by the change", err)
	}
}

// expectPending fails the test unless a make's wait that ends before a key is stored answers the group selected and
// its key pending, with no credential: what the make's 202 carries.
func (w keyWorld) expectPending(reach suppliers.Reach) {
	w.t.Helper()
	over, stop := context.WithCancel(w.t.Context())
	stop()
	if g, none, err := w.admin.AwaitKey(over, reach, w.c.supplier, w.group); err != nil || none != nil || !g.Selected || g.KeyState != "pending" {
		w.t.Fatalf("a wait that ends with no key stored = %+v, %v, %v; want the selected group, pending", g, none, err)
	}
}

// nextRound runs the round when the keys task is due again.
func (w keyWorld) nextRound(holder string) {
	w.t.Helper()
	w.c.clock.advance(syncer.Defaults().Interval[syncer.TaskKeys])
	if err := w.round(holder); err != nil {
		w.t.Fatalf("round %s: %v", holder, err)
	}
}

// unuse has a person stop using the group and erase each of its credentials.
func (w keyWorld) unuse() {
	w.t.Helper()
	ids := w.c.column("credentials", "id::text", "id")
	if err := w.c.open().Tx(w.t.Context(), platform, func(tx pg.Tx) error {
		actor := audit.User(uuid.NewV7())
		if _, err := w.admin.SelectGroup(w.t.Context(), tx, actor, w.c.supplier, w.group, false, "not needed"); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := w.admin.RemoveCredential(w.t.Context(), tx, actor, w.c.supplier, uuid.MustParse(id), "not needed"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		w.t.Fatalf("stop using the group: %v", err)
	}
}
