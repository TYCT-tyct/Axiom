package suppliers

import (
	"context"
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/persist"
)

// TestAnOldClaimStoresNothing: a key obtained under a claim that is no longer the group's is not stored, and the newer
// claim stays: the old holder's commit can never take the group from the holder that replaced it.
func TestAnOldClaimStoresNothing(t *testing.T) {
	t.Parallel()
	w := newClaimWorld(t)
	claims := persist.NewClaims(w.db, platformScope, w.supplier, w.clock.now)
	old, err := claims.Claim(t.Context(), w.ref)
	refuses(t, "the old claim", err, nil)
	refuses(t, "its key found gone", claims.Drop(t.Context(), old), nil)
	newer, err := claims.Claim(t.Context(), w.ref)
	refuses(t, "the newer claim", err, nil)
	_, err = w.syncStores("K1", old.ID)
	refuses(t, "storing under the old claim", err, persist.ErrClaimLost)
	same(t, "after the old store", []any{"", newer.ID}, []any{w.credentials(), w.claimID()})
	_, err = w.syncStores("K2", newer.ID)
	refuses(t, "storing under the newer claim", err, nil)
	same(t, "after the newer store", []string{"K2 active", "none"}, []string{w.credentials(), w.claim()})
}

// TestCredentialsOfAClaimedGroup: while a key Axiom made for a group may still be stored, no other credential enters
// it; a released claim, or one held with nothing sent past its hold, lets one in. A release names the claim the person
// read, and one that is no longer the group's changes nothing.
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
	release := func(w claimWorld, claim uuid.UUID) (Group, error) {
		return inTx(w.admins, func(ctx context.Context, tx pg.Tx) (Group, error) {
			return w.admin.ReleaseKeyClaim(ctx, tx, w.actor, w.supplier, w.group, claim, "the relay's key 101 is revoked")
		})
	}
	for name, enter := range map[string]func(claimWorld) error{"paste": pasted, "add keys": added, "bind": bound} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newClaimWorld(t)
			w.textFails()
			_, err := w.makeKey()
			refuses(t, "the make whose text was not read", err, ErrSupplierUnanswered)
			refuses(t, "a credential while the made key may be stored", enter(w), ErrKeyInProgress)
			_, err = release(w, uuid.NewV7())
			refuses(t, "releasing a claim that is not the group's", err, ErrClaimChanged)
			same(t, "the claim after it", "known 101", w.claim())
			g, err := release(w, w.claimID())
			refuses(t, "releasing the claim", err, nil)
			same(t, "the claim after", []any{"none", "pending", (*uuid.UUID)(nil)}, []any{w.claim(), g.KeyState, g.KeyClaim})
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

// TestAMadeKeyFoundAtTheRelay: a key the relay still has is adopted, never made again, with what the relay lists for
// it: one under the group's marker name, and one of the group's erased credentials under any name, whose row is sealed
// again in place; a disabled one is refused; its model limits are kept; and one the sync stored while the make asked for
// it, before or after its create answered, completes the connection with that credential.
func TestAMadeKeyFoundAtTheRelay(t *testing.T) {
	t.Parallel()
	listed := func(w claimWorld, name string, status adapter.KeyStatus, models []string) {
		w.relay.set(func(r *claimRelay) {
			r.keys["77"] = adapter.KeyState{Ref: "77", GroupRef: w.ref, Name: name, Status: status, Policy: &adapter.KeyPolicy{Models: models}}
		})
	}
	erased := func(w claimWorld) uuid.UUID {
		id := uuid.NewV7()
		w.sql(fmt.Sprintf(`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, status, max_concurrency, source)
			VALUES ('%s', '%s', '%s', '77', 'erased', 4, 'made')`, id, w.supplier, w.group))
		return id
	}
	for _, name := range []string{"the marker name", "a name a person gave it there"} {
		t.Run("an erased credential of it, under "+name, func(t *testing.T) {
			t.Parallel()
			w := newClaimWorld(t)
			listed(w, map[bool]string{true: w.name(), false: "renamed"}[name == "the marker name"], adapter.KeyActive, nil)
			row := erased(w)
			c, err := w.makeKey()
			refuses(t, "the make", err, nil)
			same(t, "the credential sealed again in place", []any{row, "active"}, []any{c.ID, c.Status})
			creates, _ := w.relay.counts()
			same(t, "creates sent", 0, creates)
		})
	}
	t.Run("disabled at the relay", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		listed(w, w.name(), adapter.KeyDisabled, nil)
		_, err := w.makeKey()
		refuses(t, "the make", err, ErrKeyUnusable)
		same(t, "the claim it leaves", "none", w.claim())
	})
	t.Run("limited to some models", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		listed(w, w.name(), adapter.KeyActive, []string{"m1"})
		_, err := w.makeKey()
		refuses(t, "the make", err, nil)
		same(t, "the models it may call", "{m1}", w.text(`SELECT allowed_models::text FROM suppliers.credentials WHERE upstream_key_ref = '77'`))
	})
	t.Run("stored by the sync once the create answered", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		made, err := w.admin.MakeKey(t.Context(), w.reach(), w.supplier, w.group)
		refuses(t, "the make's key", err, nil)
		synced, err := w.syncStores(made.Ref, made.Claim)
		refuses(t, "the sync storing it", err, nil)
		c, err := w.connect(made)
		refuses(t, "the connection", err, nil)
		same(t, "the credential it completes with", synced, c.ID)
		same(t, "whether the group is selected", "true", w.text(`SELECT selected::text FROM suppliers.supplier_groups WHERE id = $1`, w.group))
	})
	t.Run("stored by the sync while the create was in flight", func(t *testing.T) {
		t.Parallel()
		w := newClaimWorld(t)
		w.relay.set(func(r *claimRelay) { r.at, r.release = make(chan struct{}), make(chan struct{}) })
		done := make(chan error, 1)
		var c Credential
		go func() {
			var err error
			c, err = w.makeKey()
			done <- err
		}()
		select {
		case <-w.relay.at: // the create made 101 at the relay, and its answer is held
		case err := <-done:
			t.Fatalf("the make ended before its create reached the relay: %v", err)
		}
		synced, err := w.syncStores("101", w.claimID()) // the sync found it by name and stored it first
		refuses(t, "the sync storing it", err, nil)
		close(w.relay.release)
		refuses(t, "the make", <-done, nil)
		same(t, "the credential it completes with", synced, c.ID)
		creates, made := w.relay.counts()
		same(t, "creates and keys", []int{1, 1}, []int{creates, made})
	})
}

// TestRemovalNamesTheKeysOnlyAClaimKnows: a key Axiom made and never stored has no credential row, so removing the
// supplier names it to revoke from the group's claim, with the groups whose create's result is not known.
func TestRemovalNamesTheKeysOnlyAClaimKnows(t *testing.T) {
	t.Parallel()
	w := newClaimWorld(t)
	w.textFails()
	_, err := w.makeKey()
	refuses(t, "the make whose text was not read", err, ErrSupplierUnanswered)
	_, err = inTx(w.admins, func(ctx context.Context, tx pg.Tx) (Supplier, error) {
		return w.admin.Remove(ctx, tx, w.actor, w.supplier, "closing the account")
	})
	refuses(t, "the removal", err, nil)
	same(t, "what the removal names to revoke", `1 ["101"] []`, w.text(`SELECT concat_ws(' ', detail->>'keys_to_revoke_count',
		detail->>'claimed_keys_to_revoke', detail->>'unconfirmed_key_groups') FROM platform.audit_log WHERE action = 'supplier.remove'`))
}
