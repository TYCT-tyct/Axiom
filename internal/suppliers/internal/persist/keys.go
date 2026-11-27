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

// KeyGroups implements syncer.KeyStore: the groups to make a key for, then those with a credential, each once.
func (st stateStore) KeyGroups(ctx context.Context) ([]syncer.KeyGroup, error) {
	var out []syncer.KeyGroup
	id, now := st.s.Supplier.ID, st.s.node.cfg.Now()
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
		for _, r := range targets {
			out = append(out, syncer.KeyGroup{GroupRef: r.UpstreamRef, Name: keyName(id, r.UpstreamRef), Make: true})
		}
		at := make(map[string]int, len(stored))
		for _, r := range stored {
			i, ok := at[r.GroupRef]
			if !ok {
				i, at[r.GroupRef] = len(out), len(out)
				out = append(out, syncer.KeyGroup{GroupRef: r.GroupRef, Name: keyName(id, r.GroupRef)})
			}
			out[i].Stored = append(out[i].Stored, adapter.KeyRef(r.UpstreamKeyRef))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("persist: the groups to reconcile keys for: %w", err)
	}
	return out, nil
}

// Revoked implements syncer.KeyStore: the audit names the group and the key, in a transaction of its own.
func (st stateStore) Revoked(ctx context.Context, groupRef string, ref adapter.KeyRef) error {
	cfg, id := st.s.node.cfg, st.s.Supplier.ID
	return cfg.DB.Tx(ctx, cfg.Scope, func(tx pg.Tx) error {
		return audit.Record(ctx, tx, audit.Event{
			Actor: audit.System("suppliers.sync"), Action: "supplier_key.revoke",
			Target: audit.Target{Module: "suppliers", Kind: "supplier", ID: id.String()},
			Detail: map[string]any{"group_ref": groupRef, "upstream_key_ref": string(ref)},
		})
	})
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
