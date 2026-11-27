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
// test sends on release, so another path runs while it is in flight. hidden keys are made but not listed by name yet.
type claimRelay struct {
	mu        sync.Mutex
	keys      map[adapter.KeyRef]adapter.KeyState
	hidden    map[adapter.KeyRef]bool
	creates   int // create requests received
	made      int // keys made
	replay    time.Duration
	hide      bool
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
	r.creates++
	var ref adapter.KeyRef
	for k, s := range r.keys {
		if r.replay > 0 && s.Name == name && s.GroupRef == groupRef {
			ref = k // the supplier replays a create of the same name
		}
	}
	if ref == "" {
		r.made++
		ref = adapter.KeyRef(strconv.Itoa(100 + r.made))
		r.keys[ref] = adapter.KeyState{Ref: ref, GroupRef: groupRef, Name: name, Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{}}
		r.hidden[ref] = r.hide
	}
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

func (r *claimRelay) KeyNamed(_ context.Context, groupRef, name string) (adapter.KeyState, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ref, k := range r.keys {
		if k.Name == name && k.GroupRef == groupRef && !r.hidden[ref] {
			return k, true, nil
		}
	}
	return adapter.KeyState{}, false, nil
}

func (r *claimRelay) CreateReplay() time.Duration { return r.replay }

func (r *claimRelay) KeySecret(_ context.Context, ref adapter.KeyRef) (secret.Secret, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.secretErr) > 0 {
		err := r.secretErr[0]
		r.secretErr = r.secretErr[1:]
		return secret.Secret{}, err
	}
	if _, ok := r.keys[ref]; !ok {
		return secret.Secret{}, adapter.ErrKeyGone
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
			Source: "made", Ref: made.Ref, GroupRef: made.GroupRef, Text: made.Text, Policy: made.Policy, Claim: made.Claim, Stored: made.Stored,
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

// claim is the group's claim as the tables hold it: none, held, sent, or the key's id.
func (w claimWorld) claim() string {
	return w.text(`SELECT CASE WHEN key_claim_ref IS NOT NULL THEN 'known ' || key_claim_ref
		WHEN key_claim_sent_at IS NOT NULL THEN 'sent' WHEN key_claim_id IS NOT NULL THEN 'held' ELSE 'none' END
		FROM suppliers.supplier_groups WHERE id = $1`, w.group)
}

// credentials are the group's credentials that hold a key, by id, with their status.
func (w claimWorld) credentials() string {
	return w.text(`SELECT COALESCE(string_agg(upstream_key_ref || ' ' || status, ',' ORDER BY id), '')
		FROM suppliers.credentials WHERE supplier_group_id = $1`, w.group)
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
// leaves the claim, and nothing sends a second create: the id it knows is read again, a key the relay lists later is
// adopted, and one it never lists waits for a person, unless the relay replays a create of the same name.
func TestAKeyWhoseAnswerWasLostIsAdoptedNotMadeAgain(t *testing.T) {
	t.Parallel()
	t.Run("made, then the text read fails", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		w.relay.set(func(r *claimRelay) { r.secretErr = []error{errors.New("503")} })
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
	for _, c := range []struct {
		name   string
		replay time.Duration
		want   error
		claim  string
		sends  int
	}{
		{name: "the relay does not replay", want: ErrKeyInProgress, claim: "sent", sends: 1},
		{name: "the relay replays a create of the name", replay: 23 * time.Hour, claim: "none", sends: 2},
	} {
		t.Run("the answer was lost: "+c.name, func(t *testing.T) {
			t.Parallel()
			w := newClaimWorld(t)
			w.relay.set(func(r *claimRelay) { r.hide, r.replay, r.createErr = true, c.replay, errors.New("timeout") })
			_, err := w.makeKey()
			refuses(t, "the first make", err, ErrSupplierUnanswered)
			w.clock.advance(time.Hour) // long past any time a create takes
			w.relay.set(func(r *claimRelay) { r.createErr = nil })
			_, err = w.makeKey()
			refuses(t, "the make an hour later", err, c.want)
			refuses(t, "a sync round", w.syncRound(), nil)
			same(t, "the claim", c.claim, w.claim())
			creates, made := w.relay.counts()
			same(t, "creates sent and keys made", []int{c.sends, 1}, []int{creates, made})
			if c.want == nil {
				return
			}
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
	}
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
		sent, err := claims.Send(t.Context(), lost)
		refuses(t, "the lost holder sending", err, nil)
		same(t, "whether the lost holder may send", false, sent)
		creates, made := w.relay.counts()
		same(t, "creates and keys", []int{1, 1}, []int{creates, made})
	})
	t.Run("the known id is gone at the relay", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		w.relay.set(func(r *claimRelay) { r.secretErr = []error{errors.New("503")} })
		_, err := w.makeKey()
		refuses(t, "the first make", err, ErrSupplierUnanswered)
		w.relay.set(func(r *claimRelay) { delete(r.keys, "101") }) // a person deleted it there
		_, err = w.makeKey()
		refuses(t, "the make after", err, nil)
		same(t, "the credential", "102 active", w.credentials())
	})
}

// TestCredentialsOfAClaimedGroup: while a key Axiom made for a group may still be stored, no other credential enters
// it; a released claim, or one held with nothing sent past its hold, lets one in.
func TestCredentialsOfAClaimedGroup(t *testing.T) {
	t.Parallel()
	pasted := func(w claimWorld) error {
		_, err := inTx(w.admins, func(ctx context.Context, tx pg.Tx) (Group, error) {
			g, _, err := w.admin.ConnectGroup(ctx, tx, w.actor, w.supplier, w.group, GroupKey{Source: "pasted", Ref: "900", GroupRef: w.ref, Text: secret.New("sk-pasted")}, "paste")
			return g, err
		})
		return err
	}
	added := func(w claimWorld) error {
		_, err := inTx(w.admins, func(ctx context.Context, tx pg.Tx) ([]KeyCheck, error) {
			return w.admin.AddKeys(ctx, tx, w.actor, w.supplier, w.group, []PastedKey{{Ref: "900", Text: secret.New("sk-pasted"), Check: KeyCheck{Outcome: CheckPassed}}},
				Limits{MaxConcurrency: 4}, "paste")
		})
		return err
	}
	bound := func(w claimWorld) error {
		w.sql(fmt.Sprintf(`UPDATE suppliers.supplier_groups SET selected = true, codename = 'gabcdefgh' WHERE id = '%s'`, w.group))
		_, err := inTx(w.admins, func(ctx context.Context, tx pg.Tx) (Credential, error) {
			return w.admin.BindKey(ctx, tx, w.actor, w.supplier, w.group, "900", "bind")
		})
		return err
	}
	for name, enter := range map[string]func(claimWorld) error{"paste": pasted, "add keys": added, "bind": bound} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newClaimWorld(t)
			w.relay.set(func(r *claimRelay) { r.secretErr = []error{errors.New("503")} })
			_, err := w.makeKey()
			refuses(t, "the make whose text was not read", err, ErrSupplierUnanswered)
			refuses(t, "a credential while the made key may be stored", enter(w), ErrKeyInProgress)
			g, err := inTx(w.admins, func(ctx context.Context, tx pg.Tx) (Group, error) {
				return w.admin.ReleaseKeyClaim(ctx, tx, w.actor, w.supplier, w.group, "the relay's key 101 is revoked")
			})
			refuses(t, "releasing the claim", err, nil)
			same(t, "the claim after", []string{"none", "pending"}, []string{w.claim(), g.KeyState})
			refuses(t, "a credential after the release", enter(w), nil)
			same(t, "the audit of the release", "101", w.text(`SELECT detail->>'upstream_key_ref' FROM platform.audit_log
				WHERE action = 'supplier_group.release_key_claim'`))
		})
	}
	t.Run("a claim held past its hold", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		_, err := persist.NewClaims(w.db, platformScope, w.supplier, w.clock.now).Claim(t.Context(), w.ref)
		refuses(t, "taking the claim", err, nil)
		refuses(t, "a paste while it is held", pasted(w), ErrKeyInProgress)
		w.clock.advance(persist.ClaimHold)
		refuses(t, "a paste after", pasted(w), nil)
	})
}

// TestAMadeKeyFoundByItsName: a key the relay lists under the group's marker name is adopted, never made again, with
// what the relay lists for it: an erased credential of it is sealed again in place, a disabled one is refused, its model
// limits are kept, and one the sync stored while the make asked for it completes the connection.
func TestAMadeKeyFoundByItsName(t *testing.T) {
	t.Parallel()
	listed := func(w claimWorld, status adapter.KeyStatus, models []string) {
		w.relay.set(func(r *claimRelay) {
			r.keys["77"] = adapter.KeyState{Ref: "77", GroupRef: w.ref, Name: w.name(), Status: status, Policy: &adapter.KeyPolicy{Models: models}}
		})
	}
	t.Run("an erased credential of it", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		listed(w, adapter.KeyActive, nil)
		erased := uuid.NewV7()
		w.sql(fmt.Sprintf(`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, status, max_concurrency, source)
			VALUES ('%s', '%s', '%s', '77', 'erased', 4, 'made')`, erased, w.supplier, w.group))
		c, err := w.makeKey()
		refuses(t, "the make", err, nil)
		same(t, "the credential sealed again in place", []any{erased, "active"}, []any{c.ID, c.Status})
	})
	t.Run("disabled at the relay", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		listed(w, adapter.KeyDisabled, nil)
		_, err := w.makeKey()
		refuses(t, "the make", err, ErrKeyUnusable)
		same(t, "the claim it leaves", "none", w.claim())
	})
	t.Run("limited to some models", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		listed(w, adapter.KeyActive, []string{"m1"})
		_, err := w.makeKey()
		refuses(t, "the make", err, nil)
		same(t, "the models it may call", "{m1}", w.text(`SELECT allowed_models::text FROM suppliers.credentials WHERE upstream_key_ref = '77'`))
	})
	t.Run("stored by the sync meanwhile", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		made, err := w.admin.MakeKey(t.Context(), w.reach(), w.supplier, w.group)
		refuses(t, "the make's key", err, nil)
		var synced uuid.UUID
		err = w.db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
			synced, _, err = persist.StoreMadeKey(t.Context(), tx, w.keys, w.supplier, w.group, persist.MadeKey{Ref: made.Ref, Text: made.Text, Status: "active"})
			return err
		})
		refuses(t, "the sync storing it", err, nil)
		c, err := w.connect(made)
		refuses(t, "the connection", err, nil)
		same(t, "the credential it completes with", synced, c.ID)
		same(t, "whether the group is selected", "true", w.text(`SELECT selected::text FROM suppliers.supplier_groups WHERE id = $1`, w.group))
		creates, made2 := w.relay.counts()
		same(t, "creates and keys", []int{1, 1}, []int{creates, made2})
	})
}
