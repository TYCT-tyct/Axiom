package suppliers

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
)

// RequestKey connects a group with a key the sync makes for it (docs/domains/suppliers.md「接入流程」). Only the sync
// makes keys at a supplier, under the supplier's lease, so a person's make never races it into a second key: this
// change selects the group, clears the wait a failed key left on it, and brings the keys task forward, and that round
// adopts the key a listing shows under the group's marker name or makes one; AwaitKey reads the credential it stores.
// It is refused when the sync would not make the key: a supplier whose adapter makes none (ErrCannotMakeKey), whose sync
// does not run now (ErrStatusLocked) or whose keys task has stopped (ErrKeysHalted), and a group gone (ErrGroupGone),
// with a credential (ErrGroupHasKey) or with no model to sell (ErrNoKeyNeeded).
func (a *Admin) RequestKey(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, group uuid.UUID, reason string) (Group, error) {
	// The state's row before the supplier's, the order the sync's step takes them in (persist.Wake).
	halted, err := persist.RequestKeys(ctx, tx, supplier, a.now())
	switch {
	case err != nil:
		return Group{}, fmt.Errorf("request the keys task: %w", err)
	case halted:
		return Group{}, ErrKeysHalted
	}
	sup, err := a.locked(ctx, tx, supplier, Connecting, Active, LowBalance)
	if err != nil {
		return Group{}, err
	}
	if src, ok := a.registry.Get(adapter.Kind(sup.Kind)); !ok || !makesKeys(src) {
		return Group{}, ErrCannotMakeKey
	}
	row, err := a.connectable(ctx, tx, supplier, group)
	switch {
	case err != nil:
		return Group{}, err
	case !row.KeyEligible:
		return Group{}, ErrNoKeyNeeded
	}
	if err := store.New().ClearGroupKeyError(ctx, tx, store.ClearGroupKeyErrorParams{SupplierID: supplier, UpstreamRef: row.UpstreamRef}); err != nil {
		return Group{}, fmt.Errorf("clear the group's key error: %w", err)
	}
	return a.connected(ctx, tx, actor, supplier, row, reason, map[string]any{"group": group.String(), "key_source": "made"})
}

// makesKeys is whether an adapter's managers make keys.
func makesKeys(a adapter.Adapter) bool {
	src, ok := a.(adapter.KeyMakerSource)
	return ok && src.MakesKeys()
}

// AwaitKey reads the group every keyPoll until it has a credential or ctx ends, and answers the group with the
// credential, nil when it has none by then. It holds no connection between reads.
func (a *Admin) AwaitKey(ctx context.Context, r Reach, supplier, group uuid.UUID) (Group, *Credential, error) {
	read := context.WithoutCancel(ctx) // the last read is answered even when ctx ends while it runs
	tick := time.NewTicker(keyPoll)
	defer tick.Stop()
	for {
		var g Group
		var c *Credential
		err := r.DB.ReadTx(read, r.Scope, func(tx pg.Tx) (err error) {
			g, c, err = a.groupKey(read, tx, supplier, group)
			return err
		})
		if err != nil || c != nil {
			return g, c, err
		}
		select {
		case <-ctx.Done():
			return g, nil, nil
		case <-tick.C:
		}
	}
}

// keyPoll is how often AwaitKey reads: a key the sync stores is answered within it.
const keyPoll = 250 * time.Millisecond

// groupKey is the group with its first credential that holds its key's text, if it has one.
func (a *Admin) groupKey(ctx context.Context, tx pg.Tx, supplier, group uuid.UUID) (Group, *Credential, error) {
	g, err := a.group(ctx, tx, supplier, group)
	if err != nil {
		return Group{}, nil, err
	}
	ids, err := store.New().GroupCredentialID(ctx, tx, store.GroupCredentialIDParams{SupplierID: supplier, GroupID: group})
	if err != nil || len(ids) == 0 {
		return g, nil, err
	}
	c, err := a.credential(ctx, tx, supplier, ids[0])
	if err != nil {
		return Group{}, nil, err
	}
	g.KeyState, g.KeyReason = "ready", ""
	return g, &c, nil
}
