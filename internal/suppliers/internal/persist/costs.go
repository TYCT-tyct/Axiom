package persist

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// Costs is what the sync last decided of the cost of each model of a supplier's groups, which the admin reads to say why
// a cost is unknown.
type Costs struct {
	st     syncer.State
	models map[syncer.BaseKey]syncer.ModelSnap
}

// ReadCosts reads the supplier's state for Costs; a supplier that has never synced has decided nothing.
func ReadCosts(ctx context.Context, tx pg.Tx, id uuid.UUID) (Costs, error) {
	row, err := store.New().GetSyncState(ctx, tx, id)
	if errors.Is(err, pg.ErrNoRows) {
		return Costs{}, nil
	}
	if err != nil {
		return Costs{}, fmt.Errorf("persist: read the state of supplier %s: %w", id, err)
	}
	st, err := decode(row.Version, row.Format, row.State)
	if err != nil {
		return Costs{}, fmt.Errorf("persist: the state of supplier %s: %w", id, err)
	}
	return costsOf(st), nil
}

// LockCosts is ReadCosts of the state locked for a change of its bases, made if there is none yet: ConfirmBases, in the
// same transaction, then writes over exactly what was read.
func LockCosts(ctx context.Context, tx pg.Tx, id uuid.UUID, now time.Time) (Costs, error) {
	q := store.New()
	if err := q.EnsureSyncState(ctx, tx, store.EnsureSyncStateParams{SupplierID: id, Now: now}); err != nil {
		return Costs{}, fmt.Errorf("persist: make the state of supplier %s: %w", id, err)
	}
	row, err := q.LockSyncState(ctx, tx, id)
	if err != nil {
		return Costs{}, fmt.Errorf("persist: lock the state of supplier %s: %w", id, err)
	}
	st, err := decode(row.Version, row.Format, row.State)
	if err != nil {
		return Costs{}, fmt.Errorf("persist: the state of supplier %s: %w", id, err)
	}
	return costsOf(st), nil
}

func costsOf(st syncer.State) Costs {
	c := Costs{st: st, models: map[syncer.BaseKey]syncer.ModelSnap{}}
	for _, g := range st.Groups {
		for _, m := range g.Models {
			c.models[syncer.BaseKey{GroupRef: g.Ref, Model: m.Name}] = m
		}
	}
	return c
}

// Unpublished reports whether the sync read the name in the group and the supplier states no price for it: the only
// model a base can price.
func (c Costs) Unpublished(groupRef, name string) bool {
	m, ok := c.models[syncer.BaseKey{GroupRef: groupRef, Model: name}]
	return ok && !m.Published
}

// Reason is why the cost of the name in the group is unknown for model, the catalog model it maps to; "" when it is
// known or the sync has not read the name. It is worked out from the bases now, not carried over from the round: a
// model whose supplier states no price is unpriced unless a base for this very model priced it, as the projection puts
// a plan in force (projector.planOf), or the last round found that base past the unit price limit.
func (c Costs) Reason(groupRef, name string, model uuid.UUID) syncer.CostReason {
	key := syncer.BaseKey{GroupRef: groupRef, Model: name}
	m, ok := c.models[key]
	switch base, confirmed := c.st.Base(key); {
	case !ok:
		return ""
	case m.Published && m.Plan == "":
		return syncer.Unexpressible
	case m.Published:
		return ""
	case !confirmed || base.For != model.String():
		return syncer.Unpriced
	case m.Plan == "" && m.CostReason == syncer.OverLimit:
		return syncer.OverLimit
	case m.Plan == "":
		return syncer.Unpriced // confirmed since the last round, which has not priced it yet
	}
	return ""
}
