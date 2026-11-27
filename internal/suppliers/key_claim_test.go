package suppliers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/syncer"
)

// claimRelay is a relay's management side that makes keys the way new-api does, in memory: a key is made when the
// create arrives, whatever its answer says. at, when set, receives each create as it arrives and holds it until the
// test sends on release, so another path runs while it is in flight. hidden keys are made but not listed by name yet;
// a read by id finds them.
type claimRelay struct {
	mu        sync.Mutex
	keys      map[adapter.KeyRef]adapter.KeyState
	hidden    map[adapter.KeyRef]bool
	creates   int // create requests received
	made      int // keys made
	hide      bool
	keepBack  int     // creates the rate buckets refuse before they leave, from the next
	createErr error   // answered to every create, after the key is made
	secretErr []error // answered to the next text reads, in order
	at        chan struct{}
	release   chan struct{}
}

func newClaimRelay() *claimRelay {
	return &claimRelay{keys: map[adapter.KeyRef]adapter.KeyState{}, hidden: map[adapter.KeyRef]bool{}}
}

func (r *claimRelay) CreateKey(_ context.Context, groupRef, name string) (adapter.KeyRef, error) {
	r.mu.Lock()
	if r.keepBack > 0 {
		r.keepBack--
		r.mu.Unlock()
		return "", fmt.Errorf("%w: %w", adapter.ErrNotSent, adapter.ErrBucketsFull)
	}
	r.creates++
	r.made++
	ref := adapter.KeyRef(strconv.Itoa(100 + r.made))
	r.keys[ref] = adapter.KeyState{Ref: ref, GroupRef: groupRef, Name: name, Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{}}
	r.hidden[ref] = r.hide
	err, at, release := r.createErr, r.at, r.release
	r.mu.Unlock()
	if at != nil {
		at <- struct{}{}
		<-release
	}
	if err != nil {
		return "", err
	}
	return ref, nil
}

func (r *claimRelay) KeyNamed(_ context.Context, groupRef, name string, _ adapter.Listing) (adapter.KeyState, bool, adapter.Listing, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ref, k := range r.keys {
		if k.Name == name && k.GroupRef == groupRef && !r.hidden[ref] {
			return k, true, nil, nil
		}
	}
	return adapter.KeyState{}, false, nil, nil
}

func (r *claimRelay) KeyByRef(_ context.Context, ref adapter.KeyRef) (adapter.KeyState, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.keys[ref]
	return k, ok, nil
}

func (r *claimRelay) KeySecret(_ context.Context, ref adapter.KeyRef) (secret.Secret, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.secretErr) > 0 {
		err := r.secretErr[0]
		r.secretErr = r.secretErr[1:]
		return secret.Secret{}, err
	}
	return secret.New("sk-text-" + string(ref)), nil
}

func (*claimRelay) Account(context.Context) (adapter.Account, error) {
	return adapter.Account{}, errors.New("not in this test")
}

func (*claimRelay) Offer(context.Context, adapter.Listing) (adapter.Page[adapter.Group], error) {
	return adapter.Page[adapter.Group]{}, errors.New("not in this test")
}

func (*claimRelay) Keys(context.Context, adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	return adapter.Page[adapter.KeyState]{Done: true}, nil
}

func (*claimRelay) Charges(context.Context, adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	return adapter.ChargePage{}, nil, errors.New("not in this test")
}

func (r *claimRelay) counts() (creates, made int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creates, r.made
}

func (r *claimRelay) set(f func(*claimRelay)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(r)
}

// claimKind is the adapter whose management side is the relay.
type claimKind struct {
	onboardable
	relay *claimRelay
}

func (k claimKind) Open(context.Context, adapter.Client, adapter.LoginStore) (adapter.Manager, error) {
	return k.relay, nil
}

// claimWorld is a relay in use with one group that needs a key Axiom makes.
type claimWorld struct {
	admins
	relay           *claimRelay
	supplier, group uuid.UUID
	ref             string
}

func newClaimWorld(t *testing.T) claimWorld {
	t.Helper()
	relay := newClaimRelay()
	a := newAdmins(t, claimKind{onboardable{kind: "token", detects: recognising}, relay})
	id := a.connect("https://api.example.com", "ops@example.com").ID
	a.setStatus(id, "active")
	group := uuid.NewV7()
	a.sql(fmt.Sprintf(`INSERT INTO suppliers.supplier_groups (id, supplier_id, upstream_ref, name, ratio, present, key_eligible)
		VALUES ('%s', '%s', 'g', 'Group g', 1, true, true)`, group, id))
	return claimWorld{admins: a, relay: relay, supplier: id, group: group, ref: "g"}
}

func (w claimWorld) reach() Reach { return Reach{DB: w.db, Scope: platformScope} }

func (w claimWorld) name() string { return persist.KeyName(w.supplier, w.ref) }

// makeKey is the admin's make, as connectSupplierGroup runs it: the key first, then the connection.
func (w claimWorld) makeKey() (Credential, error) {
	made, err := w.admin.MakeKey(w.t.Context(), w.reach(), w.supplier, w.group)
	if err != nil {
		return Credential{}, err
	}
	return w.connect(made)
}

func (w claimWorld) connect(made MadeKey) (Credential, error) {
	var c Credential
	err := w.db.Tx(w.t.Context(), platformScope, func(tx pg.Tx) (err error) {
		_, c, err = w.admin.ConnectGroup(w.t.Context(), tx, w.actor, w.supplier, w.group, GroupKey{
			Source: "made", Ref: made.Ref, GroupRef: made.GroupRef, Text: made.Text, Policy: made.Policy, Claim: made.Claim,
		}, "connect")
		return err
	})
	return c, err
}

// syncRound is one round of the supplier's sync on its own lease, as a node runs it. Its keys task obtains the key of
// the group under the same claim; no listing was read, so the commit stores none of what it obtained.
func (w claimWorld) syncRound() error {
	s, err := w.node("sync").Acquire(w.t.Context(), w.supplier)
	if err != nil {
		return err
	}
	defer func() { _ = s.Release(context.WithoutCancel(w.t.Context())) }() //nolint:errcheck // the lease ends with the test
	return syncer.New(s.Store(), w.relay, syncer.Defaults(), w.clock.now).Round(w.t.Context())
}

// syncStores stores the key as the sync's commit does, under the claim it obtained the key under (zero for none).
func (w claimWorld) syncStores(ref string, claim uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := w.db.Tx(w.t.Context(), platformScope, func(tx pg.Tx) (err error) {
		id, _, err = persist.StoreMadeKey(w.t.Context(), tx, w.keys, w.supplier, w.group,
			persist.MadeKey{Ref: ref, Text: secret.New("sk-text-" + ref), Status: "active", Claim: claim})
		return err
	})
	return id, err
}

// claim is the group's claim as the tables hold it: none, held, sent, or the key's id.
func (w claimWorld) claim() string {
	return w.text(`SELECT CASE WHEN key_claim_ref IS NOT NULL THEN 'known ' || key_claim_ref
		WHEN key_claim_sent_at IS NOT NULL THEN 'sent' WHEN key_claim_id IS NOT NULL THEN 'held' ELSE 'none' END
		FROM suppliers.supplier_groups WHERE id = $1`, w.group)
}

// claimID is the group's claim, as a person reads it to release it.
func (w claimWorld) claimID() uuid.UUID {
	id, err := uuid.Parse(w.text(`SELECT key_claim_id::text FROM suppliers.supplier_groups WHERE id = $1`, w.group))
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

// credentials are the group's credentials that hold a key, by id, with their status.
func (w claimWorld) credentials() string {
	return w.text(`SELECT COALESCE(string_agg(upstream_key_ref || ' ' || status, ',' ORDER BY id), '')
		FROM suppliers.credentials WHERE supplier_group_id = $1`, w.group)
}

// textFails makes the next read of a key's text fail, so the claim keeps the key's id.
func (w claimWorld) textFails() {
	w.relay.set(func(r *claimRelay) { r.secretErr = []error{errors.New("503")} })
}

// TestOneKeyPerGroupAcrossPaths: a person's make and the sync obtain one group's key at the same time; whichever
// sends the create first, the other one never sends a second. While the first create is at the relay, the other path
// adopts the key once the relay lists it, and otherwise waits; the make that went second is refused with
// ErrKeyInProgress or connects with the key the first made, which fences the round of the sync that made it.
func TestOneKeyPerGroupAcrossPaths(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		syncFirst  bool
		hide       bool
		wantClaim  string
		wantSecond error
		wantFirst  error
	}{
		{name: "the make first, the key listed while it is in flight", wantClaim: "none"},
		{name: "the make first, the key not listed yet", hide: true, wantClaim: "none"},
		{name: "the sync first, the key listed", syncFirst: true, wantClaim: "none", wantFirst: syncer.ErrFenced},
		{name: "the sync first, the key not listed yet", syncFirst: true, hide: true, wantClaim: "known 101", wantSecond: ErrKeyInProgress},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newClaimWorld(t)
			w.relay.set(func(r *claimRelay) { r.hide, r.at, r.release = c.hide, make(chan struct{}), make(chan struct{}) })
			first, second := w.syncRound, func() error { _, err := w.makeKey(); return err }
			if !c.syncFirst {
				first, second = second, first
			}
			done := make(chan error, 1)
			go func() { done <- first() }()
			select {
			case <-w.relay.at: // the first create is at the relay, its answer held
			case err := <-done:
				t.Fatalf("the first path ended before its create reached the relay: %v", err)
			}
			w.relay.set(func(r *claimRelay) { r.at = nil })
			secondErr := second()
			close(w.relay.release)
			refuses(t, "the first path", <-done, c.wantFirst)
			refuses(t, "the second path", secondErr, c.wantSecond)
			creates, made := w.relay.counts()
			same(t, "creates sent and keys made", []int{1, 1}, []int{creates, made})
			same(t, "the claim after both", c.wantClaim, w.claim())
		})
	}
}

// TestAKeyWhoseAnswerWasLostIsAdoptedNotMadeAgain: a create whose answer was lost, or whose text could not be read,
// leaves the claim, and nothing sends a second create: the id it knows is read again, as the relay lists it now; a key
// the relay lists later is adopted; one it never lists waits for a person. A create the rate buckets kept back sent
// nothing, so it leaves no claim; a known id the relay answers it no longer has is made again.
func TestAKeyWhoseAnswerWasLostIsAdoptedNotMadeAgain(t *testing.T) {
	t.Parallel()
	t.Run("made, then the text read fails", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		w.textFails()
		_, err := w.makeKey()
		refuses(t, "the first make", err, ErrSupplierUnanswered)
		same(t, "the claim it leaves", "known 101", w.claim())
		_, err = w.makeKey()
		refuses(t, "the make again", err, nil)
		creates, made := w.relay.counts()
		same(t, "creates and keys", []int{1, 1}, []int{creates, made})
		same(t, "the credential", "101 active", w.credentials())
		same(t, "the claim", "none", w.claim())
	})
	t.Run("the answer was lost", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		w.relay.set(func(r *claimRelay) { r.hide, r.createErr = true, errors.New("timeout") })
		_, err := w.makeKey()
		refuses(t, "the first make", err, ErrSupplierUnanswered)
		w.clock.advance(24 * time.Hour) // long past any time a create takes, or a relay keeps its answer
		w.relay.set(func(r *claimRelay) { r.createErr = nil })
		_, err = w.makeKey()
		refuses(t, "the make a day later", err, ErrKeyInProgress)
		refuses(t, "a sync round", w.syncRound(), nil)
		same(t, "the claim", "sent", w.claim())
		creates, made := w.relay.counts()
		same(t, "creates sent and keys made", []int{1, 1}, []int{creates, made})
		got, err := inTx(w.admins, func(ctx context.Context, tx pg.Tx) (Group, error) {
			return w.admin.GroupOf(ctx, tx, w.supplier, w.group)
		})
		refuses(t, "reading the group", err, nil)
		same(t, "the group's key state", "unconfirmed", got.KeyState)
		w.relay.set(func(r *claimRelay) { clear(r.hidden) }) // the relay lists the key at last
		_, err = w.makeKey()
		refuses(t, "the make once it is listed", err, nil)
		same(t, "the credential", "101 active", w.credentials())
		creates, made = w.relay.counts()
		same(t, "creates sent and keys made at the end", []int{1, 1}, []int{creates, made})
	})
	t.Run("kept back by the rate buckets", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		w.relay.set(func(r *claimRelay) { r.keepBack = 1 })
		_, err := w.makeKey()
		refuses(t, "the make kept back", err, ErrSupplierUnanswered)
		same(t, "the claim it leaves", "none", w.claim())
		_, err = w.makeKey()
		refuses(t, "the make once the buckets have room", err, nil)
		creates, made := w.relay.counts()
		same(t, "creates and keys", []int{1, 1}, []int{creates, made})
	})
	t.Run("a holder that stopped before sending", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		claims := persist.NewClaims(w.db, platformScope, w.supplier, w.clock.now)
		lost, err := claims.Claim(t.Context(), w.ref)
		refuses(t, "taking the claim", err, nil)
		_, err = w.makeKey()
		refuses(t, "a make while it is held", err, ErrKeyInProgress)
		w.clock.advance(persist.ClaimHold)
		_, err = w.makeKey()
		refuses(t, "a make once the hold is over", err, nil)
		_, sent, err := claims.Send(t.Context(), lost)
		refuses(t, "the lost holder sending", err, nil)
		same(t, "whether the lost holder may send", false, sent)
		creates, made := w.relay.counts()
		same(t, "creates and keys", []int{1, 1}, []int{creates, made})
	})
	t.Run("the known id is gone at the relay", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		w.textFails()
		_, err := w.makeKey()
		refuses(t, "the first make", err, ErrSupplierUnanswered)
		w.relay.set(func(r *claimRelay) { delete(r.keys, "101") }) // a person deleted it there
		_, err = w.makeKey()
		refuses(t, "the make after", err, nil)
		same(t, "the credential", "102 active", w.credentials())
	})
	t.Run("the known id was disabled and limited at the relay", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		w.textFails()
		_, err := w.makeKey()
		refuses(t, "the first make", err, ErrSupplierUnanswered)
		listedAs := func(status adapter.KeyStatus) {
			w.relay.set(func(r *claimRelay) {
				k := r.keys["101"]
				k.Status, k.Policy = status, &adapter.KeyPolicy{Models: []string{"m1"}}
				r.keys["101"] = k
			})
		}
		listedAs(adapter.KeyDisabled)
		_, err = w.makeKey()
		refuses(t, "a make while the relay lists it disabled", err, ErrKeyUnusable)
		same(t, "the claim it keeps", "known 101", w.claim())
		listedAs(adapter.KeyActive)
		_, err = w.makeKey()
		refuses(t, "a make once it is enabled", err, nil)
		same(t, "the models it may call", "{m1}", w.text(`SELECT allowed_models::text FROM suppliers.credentials WHERE upstream_key_ref = '101'`))
	})
}
