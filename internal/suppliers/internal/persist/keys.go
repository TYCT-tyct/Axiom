package persist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

var _ syncer.KeyStore = stateStore{}

// KeyGroups implements syncer.KeyStore: the groups to make a key for, then those with a credential or a key recorded as
// made, each once.
func (st stateStore) KeyGroups(ctx context.Context) ([]syncer.KeyGroup, error) {
	id, now := st.s.Supplier.ID, st.s.node.cfg.Now()
	var groups keyGroups
	err := st.s.node.cfg.DB.ReadTx(ctx, st.s.node.cfg.Scope, func(tx pg.Tx) error {
		q := store.New()
		targets, err := q.GroupsToKey(ctx, tx, store.GroupsToKeyParams{SupplierID: id, Now: &now})
		if err != nil {
			return err
		}
		stored, err := q.SupplierKeyRefs(ctx, tx, id)
		if err != nil {
			return err
		}
		made, err := q.MadeKeyRefs(ctx, tx, id)
		if err != nil {
			return err
		}
		groups = keyGroups{supplier: id, at: map[string]int{}}
		for _, r := range targets {
			groups.of(r.UpstreamRef).Make = true
		}
		for _, r := range stored {
			g := groups.of(r.GroupRef)
			g.Stored = append(g.Stored, adapter.KeyRef(r.UpstreamKeyRef))
		}
		for _, r := range made {
			g := groups.of(r.GroupRef)
			g.Made = append(g.Made, adapter.KeyRef(r.UpstreamKeyRef))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("persist: the groups to reconcile keys for: %w", err)
	}
	return groups.list, nil
}

// keyGroups gathers the groups of KeyGroups by their ref, in the order first seen.
type keyGroups struct {
	supplier uuid.UUID
	list     []syncer.KeyGroup
	at       map[string]int
}

func (k *keyGroups) of(ref string) *syncer.KeyGroup {
	i, ok := k.at[ref]
	if !ok {
		i, k.at[ref] = len(k.list), len(k.list)
		k.list = append(k.list, syncer.KeyGroup{GroupRef: ref, Name: keyName(k.supplier, ref)})
	}
	return &k.list[i]
}

// Made implements syncer.KeyStore.
func (st stateStore) Made(ctx context.Context, groupRef string, ref adapter.KeyRef) error {
	cfg, id := st.s.node.cfg, st.s.Supplier.ID
	return cfg.DB.Tx(ctx, cfg.Scope, func(tx pg.Tx) error {
		return store.New().RecordMadeKey(ctx, tx, store.RecordMadeKeyParams{
			SupplierID: id, UpstreamKeyRef: string(ref), GroupRef: groupRef, MadeAt: cfg.Now(),
		})
	})
}

// Revoke implements syncer.KeyStore. It holds the supplier's row while the supplier deletes the key: every change of a
// credential takes that row first, so a key a person stores meanwhile is either seen here and kept, or stored after the
// key is gone. The wait is one request to the supplier, which its client bounds; revocations are rare.
func (st stateStore) Revoke(ctx context.Context, groupRef string, ref adapter.KeyRef, del func(context.Context) error) (bool, error) {
	cfg, id := st.s.node.cfg, st.s.Supplier.ID
	revoked := false
	err := cfg.DB.Tx(ctx, cfg.Scope, func(tx pg.Tx) error {
		q := store.New()
		if _, err := q.LockSupplier(ctx, tx, id); err != nil {
			return fmt.Errorf("lock the supplier: %w", err)
		}
		ok, err := q.KeyRevocable(ctx, tx, store.KeyRevocableParams{SupplierID: id, UpstreamKeyRef: string(ref)})
		if err != nil || !ok {
			return err
		}
		if err := del(ctx); err != nil {
			return err
		}
		if err := q.ForgetMadeKey(ctx, tx, store.ForgetMadeKeyParams{SupplierID: id, UpstreamKeyRef: string(ref)}); err != nil {
			return err
		}
		revoked = true
		return audit.Record(ctx, tx, audit.Event{
			Actor: audit.System("suppliers.sync"), Action: "supplier_key.revoke",
			Target: audit.Target{Module: "suppliers", Kind: "supplier", ID: id.String()},
			Detail: map[string]any{"group_ref": groupRef, "upstream_key_ref": string(ref)},
		})
	})
	return revoked && err == nil, err
}

// KeysToBind implements syncer.KeyStore.
func (st stateStore) KeysToBind(ctx context.Context, now time.Time) ([]syncer.BindTarget, error) {
	var out []syncer.BindTarget
	err := st.s.node.cfg.DB.ReadTx(ctx, st.s.node.cfg.Scope, func(tx pg.Tx) error {
		rows, err := store.New().GroupsToBind(ctx, tx, store.GroupsToBindParams{SupplierID: st.s.Supplier.ID, Now: &now})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, syncer.BindTarget{
				ID: r.ID, Ref: adapter.KeyRef(r.UpstreamKeyRef), GroupRef: r.UpstreamRef,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("persist: the keys to bind: %w", err)
	}
	return out, nil
}

// keyName is the name Axiom gives a key it makes: a marker that says the key is Axiom's, for which supplier and group,
// so a key made before a commit that failed is adopted instead of made again. Eight hex digits of the supplier's id and
// eight of a hash of the supplier and group keep it short enough to read and unique to the group in practice.
func keyName(supplier uuid.UUID, groupRef string) string {
	sum := sha256.Sum256([]byte(supplier.String() + "\x00" + groupRef))
	id := strings.ReplaceAll(supplier.String(), "-", "")
	return "axiom-" + id[:8] + "-" + hex.EncodeToString(sum[:4])
}
