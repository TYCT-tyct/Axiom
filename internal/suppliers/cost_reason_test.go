package suppliers

import (
	"context"
	"testing"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/syncer"
)

// TestAnUnknownCostSaysWhy: an endpoint whose cost is unknown says why, from what the sync's last catalog round decided
// of its name and what reconciliation wrote on its row; only an unpriced one is what a base can price. A plan made of a
// base confirmed for another model prices nothing here, so the endpoint is unpriced for its own model.
func TestAnUnknownCostSaysWhy(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1) // the endpoint "gpt" of group "g"
	model := w.endpointModel()
	stored := func(reason string) *string { return &reason }
	for _, c := range []struct {
		name      string
		snap      syncer.ModelSnap
		base      string // the model a base of gpt is confirmed for; "" for none
		cost      string
		costState *string // the reason the endpoint's row keeps
		want      CostReason
	}{
		{"a known cost", syncer.ModelSnap{Plan: "f1"}, "", "known", nil, ""},
		{"no price published", syncer.ModelSnap{CostReason: syncer.Unpriced}, "", "unknown", stored(string(syncer.CostUnknown)), Unpriced},
		{"a price the table cannot express", syncer.ModelSnap{Published: true, CostReason: syncer.Unexpressible}, "", "unknown", nil, Unexpressible},
		{"a base past the unit price limit", syncer.ModelSnap{CostReason: syncer.OverLimit}, model.String(), "unknown", nil, OverLimit},
		{"reconciliation found it under-estimated", syncer.ModelSnap{Plan: "f1", Published: true}, "", "unknown", stored("under_estimated"), UnderEstimated},
		{"a base confirmed for another model", syncer.ModelSnap{Plan: "f1", Confirmed: true}, uuid.NewV7().String(), "unknown", nil, Unpriced},
		{"a base for this model, the endpoint out for another reason", syncer.ModelSnap{Plan: "f1", Confirmed: true}, model.String(), "unknown", stored(string(syncer.NotTaken)), ""},
	} {
		c.snap.Name, c.snap.Formats = "gpt", []int{2}
		st := syncer.State{Groups: []syncer.GroupSnap{{Ref: "g", Name: "g", Ratio: "1", Facts: "{}", Models: []syncer.ModelSnap{c.snap}}}}
		if c.base != "" {
			st.Confirmed = []syncer.ConfirmedBase{{GroupRef: "g", Model: "gpt", For: c.base, Base: confirmedBaseText}}
		}
		raw, err := jsonx.MarshalCanonical(st)
		if err != nil {
			t.Fatal(err)
		}
		var d Detail
		w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
			if _, err = tx.Exec(ctx, `INSERT INTO suppliers.sync_states (supplier_id, format, state, next_due) VALUES ($1, 3, $2, now())
				ON CONFLICT (supplier_id) DO UPDATE SET format = 3, state = $2`, w.supplier, raw); err != nil {
				return err
			}
			if c.cost == "unknown" {
				_, err = tx.Exec(ctx, `UPDATE suppliers.endpoints SET cost_state = 'unknown', cost_state_reason = $2 WHERE id = $1`, w.endpoint, c.costState)
			}
			if err != nil {
				return err
			}
			d, err = w.adminOf().Supplier(ctx, tx, w.supplier, PlatformOwned)
			return err
		})
		same(t, c.name, c.want, d.Endpoints[0].CostReason)
	}
}

// confirmedBaseText is a base in the supplier's own unit, as one is stored.
const confirmedBaseText = `{"currency":"XXX","rates":{"input":"0.000001"},"v":1}`
