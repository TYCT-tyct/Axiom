package persist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

var _ syncer.KeyStore = stateStore{}

// KeysToMake implements syncer.KeyStore.
func (st stateStore) KeysToMake(ctx context.Context) ([]syncer.KeyTarget, error) {
	var out []syncer.KeyTarget
	now := st.s.node.cfg.Now()
	err := st.s.node.cfg.DB.ReadTx(ctx, st.s.node.cfg.Scope, func(tx pg.Tx) error {
		rows, err := store.New().GroupsToKey(ctx, tx, store.GroupsToKeyParams{SupplierID: st.s.Supplier.ID, Now: &now})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, syncer.KeyTarget{GroupRef: r.UpstreamRef, Name: keyName(st.s.Supplier.ID, r.UpstreamRef)})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("persist: the groups to make keys for: %w", err)
	}
	return out, nil
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
