package persist

import (
	"testing"

	"axiom/internal/suppliers/internal/syncer"
)

// TestABaseForAnotherModelReplacesTheWholePlan: a base confirmed for another model than the one the plan was made of
// takes the plan away at once, so no step that commits before the next catalog round prices the new model with the old
// model's plan; the same model confirmed again keeps its plan until that round prices the new base.
func TestABaseForAnotherModelReplacesTheWholePlan(t *testing.T) {
	t.Parallel()
	key := syncer.BaseKey{GroupRef: "g", Model: "m"}
	for _, c := range []struct {
		name, now string // the model the new base is for
		plan      string // the model's plan after the change
	}{
		{"the same model", "a", "f1"},
		{"another model", "b", ""},
	} {
		st := syncer.State{
			Groups:    []syncer.GroupSnap{{Ref: "g", Models: []syncer.ModelSnap{{Name: "m", Plan: "f1", Confirmed: true}}}},
			Plans:     syncer.Plans{key: "f1"},
			Confirmed: []syncer.ConfirmedBase{{BaseKey: key, For: "a", Base: "{}"}},
		}
		changeBases(&st, []syncer.ConfirmedBase{{BaseKey: key, For: c.now, Base: "{}"}}, nil)
		if m := st.Groups[0].Models[0]; m.Plan != c.plan || st.Plans[key] != c.plan || m.Confirmed != (c.plan != "") {
			t.Errorf("%s: the model %+v, plans %v; want the plan %q", c.name, m, st.Plans, c.plan)
		}
	}
}
