package persist

import (
	"context"
	"errors"
	"fmt"
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
	c := Costs{st: st, models: map[syncer.BaseKey]syncer.ModelSnap{}}
	for _, g := range st.Groups {
		for _, m := range g.Models {
			c.models[syncer.BaseKey{GroupRef: g.Ref, Model: m.Name}] = m
		}
	}
	return c, nil
}

// Reason is why the sync has no cost plan of the name in the group for model, the catalog model it maps to; "" when it
// has one or has not read the name. A plan made of a base confirmed for another model is none for this one, as the
// projection puts it in force (projector.planOf): only a base for this model can price it.
func (c Costs) Reason(groupRef, name string, model uuid.UUID) syncer.CostReason {
	key := syncer.BaseKey{GroupRef: groupRef, Model: name}
	m, ok := c.models[key]
	switch {
	case !ok:
		return ""
	case m.Plan == "":
		return m.CostReason
	case m.Confirmed:
		if base, ok := c.st.Base(key); !ok || base.For != model.String() {
			return syncer.Unpriced
		}
	}
	return ""
}
