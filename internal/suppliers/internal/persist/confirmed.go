package persist

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// ConfirmBases writes a person's base prices into a supplier's state: set gives a model of a group its base (replacing
// one it had), and clear takes one away. It answers what each cleared key held; a key that held none is left out. The
// catalog task is brought forward to now, as RequestSync does, so the next round works the cost plans out from the
// bases; and the raised version fences out a round already in flight, which would otherwise commit the bases it read
// over these. The caller locks the supplier after this, the order a step takes the two rows in (LockSyncState).
func ConfirmBases(ctx context.Context, tx pg.Tx, id uuid.UUID, set []syncer.ConfirmedBase, clear []syncer.BaseKey, now time.Time) ([]syncer.ConfirmedBase, error) {
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
	cleared := changeBases(&st, set, clear)
	if task := &st.Tasks[syncer.TaskCatalog]; st.Status == syncer.Normal && !task.Halted && task.Due.After(now) {
		task.Due = now
	}
	st.Version++
	raw, err := jsonx.MarshalCanonical(st)
	if err != nil {
		return nil, fmt.Errorf("persist: encode the state of supplier %s: %w", id, err)
	}
	if err := q.RequestBalanceSync(ctx, tx, store.RequestBalanceSyncParams{
		SupplierID: id, Version: st.Version, Format: format, State: raw, NextDue: nextDue(st),
	}); err != nil {
		return nil, fmt.Errorf("persist: store the state of supplier %s: %w", id, err)
	}
	return cleared, nil
}

// changeBases takes the bases of clear out of the state and puts those of set in, keeping them sorted and one a key, and
// answers the ones it took out.
func changeBases(st *syncer.State, set []syncer.ConfirmedBase, clear []syncer.BaseKey) []syncer.ConfirmedBase {
	var cleared []syncer.ConfirmedBase
	for _, k := range clear {
		if b, ok := st.Base(k); ok {
			cleared = append(cleared, b)
			st.Confirmed = slices.DeleteFunc(st.Confirmed, func(c syncer.ConfirmedBase) bool { return c.BaseKey == k })
		}
	}
	for _, b := range set {
		st.Confirmed = slices.DeleteFunc(st.Confirmed, func(c syncer.ConfirmedBase) bool { return c.BaseKey == b.BaseKey })
		st.Confirmed = append(st.Confirmed, b)
	}
	slices.SortFunc(st.Confirmed, func(a, b syncer.ConfirmedBase) int {
		return cmp.Or(strings.Compare(a.GroupRef, b.GroupRef), strings.Compare(a.Model, b.Model))
	})
	return cleared
}
