package persist

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// ConfirmBases writes a person's base prices into a supplier's state: set gives an endpoint (syncer.PlanKey) its base,
// a schedule in the supplier's own unit as canonical JSON, and clear takes one away. It answers what each cleared key
// held, or nothing for one that held none. The catalog task is brought forward to now, as RequestSync does, so the next
// round works the endpoints' cost plans out from the bases; and the raised version fences out a round already in flight,
// which would otherwise commit the bases it read over these. The caller locks the supplier after this, the order a step
// takes the two rows in (LockSyncState).
func ConfirmBases(ctx context.Context, tx pg.Tx, id uuid.UUID, set map[string]string, clear []string, now time.Time) (map[string]string, error) {
	q := store.New()
	if err := q.EnsureSyncState(ctx, tx, store.EnsureSyncStateParams{SupplierID: id, Now: now}); err != nil {
		return nil, fmt.Errorf("persist: make the state of supplier %s: %w", id, err)
	}
	row, err := q.LockSyncState(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("persist: lock the state of supplier %s: %w", id, err)
	}
	st, err := decode(row.Version, row.Format, row.State)
	if err != nil {
		return nil, fmt.Errorf("persist: the state of supplier %s: %w", id, err)
	}
	if st.Confirmed == nil {
		st.Confirmed = map[string]string{}
	}
	cleared := map[string]string{}
	for _, key := range clear {
		if base, ok := st.Confirmed[key]; ok {
			cleared[key] = base
			delete(st.Confirmed, key)
		}
	}
	for key, base := range set {
		st.Confirmed[key] = base
	}
	if task := &st.Tasks[syncer.TaskCatalog]; st.Status == syncer.Normal && !task.Halted && task.Due.After(now) {
		task.Due = now
	}
	st.Version++
	raw, err := jsonx.MarshalCanonical(st)
	if err != nil {
		return nil, fmt.Errorf("persist: encode the state of supplier %s: %w", id, err)
	}
	if err := q.RequestBalanceSync(ctx, tx, store.RequestBalanceSyncParams{
		SupplierID: id, Version: st.Version, State: raw, NextDue: nextDue(st),
	}); err != nil {
		return nil, fmt.Errorf("persist: store the state of supplier %s: %w", id, err)
	}
	return cleared, nil
}
