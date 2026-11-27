package persist

import (
	"testing"

	"axiom/internal/suppliers/internal/syncer"
)

// TestABaseForAnotherModelReplacesTheWholePlan: a base confirmed for another model than the one the plan was made of
// takes the plan away at once, so no step that commits before the next catalog round prices the new model with the old
// model's plan; the same models confirmed again, one or several at a time, keep their plans until that round prices
// the new bases, and lose the round's verdict on the old base (over_limit), which no longer holds.
func TestABaseForAnotherModelReplacesTheWholePlan(t *testing.T) {
	t.Parallel()
	a, b := syncer.BaseKey{GroupRef: "g", Model: "a"}, syncer.BaseKey{GroupRef: "g", Model: "b"}
	for _, c := range []struct {
		name  string
		set   []syncer.ConfirmedBase
		plans [2]string            // a's and b's plans after the change
		why   [2]syncer.CostReason // and the verdicts left on them
	}{
		{"the same model", []syncer.ConfirmedBase{{BaseKey: a, For: "x"}}, [2]string{"fa", ""}, [2]syncer.CostReason{"", syncer.OverLimit}},
		{"the model past the limit, again at another price", []syncer.ConfirmedBase{{BaseKey: b, For: "x"}}, [2]string{"fa", ""}, [2]syncer.CostReason{"", ""}},
		{"another model", []syncer.ConfirmedBase{{BaseKey: a, For: "y"}}, [2]string{"", ""}, [2]syncer.CostReason{"", syncer.OverLimit}},
		{"both again, in one batch", []syncer.ConfirmedBase{{BaseKey: a, For: "x"}, {BaseKey: b, For: "x"}}, [2]string{"fa", ""}, [2]syncer.CostReason{"", ""}},
	} {
		st := syncer.State{
			Groups: []syncer.GroupSnap{{Ref: "g", Models: []syncer.ModelSnap{
				{Name: "a", Plan: "fa", Confirmed: true},
				{Name: "b", CostReason: syncer.OverLimit},
			}}},
			Plans:     syncer.Plans{a: "fa"},
			Confirmed: []syncer.ConfirmedBase{{BaseKey: a, For: "x"}, {BaseKey: b, For: "x"}},
		}
		changeBases(&st, c.set, nil)
		m := st.Groups[0].Models
		got := [4]string{m[0].Plan, st.Plans[a], string(m[0].CostReason), string(m[1].CostReason)}
		if want := [4]string{c.plans[0], c.plans[0], string(c.why[0]), string(c.why[1])}; got != want {
			t.Errorf("%s: a's plan, a's stored plan, a's and b's verdicts %q, want %q", c.name, got, want)
		}
	}
}

// TestBasesConfirmedTogetherKeepTheirPlans: a batch that confirms several models again, each for the model it was for,
// leaves every plan, however the bases were ordered before.
func TestBasesConfirmedTogetherKeepTheirPlans(t *testing.T) {
	t.Parallel()
	a, b := syncer.BaseKey{GroupRef: "g", Model: "a"}, syncer.BaseKey{GroupRef: "g", Model: "b"}
	st := syncer.State{
		Groups: []syncer.GroupSnap{{Ref: "g", Models: []syncer.ModelSnap{
			{Name: "a", Plan: "fa", Confirmed: true}, {Name: "b", Plan: "fb", Confirmed: true},
		}}},
		Plans:     syncer.Plans{a: "fa", b: "fb"},
		Confirmed: []syncer.ConfirmedBase{{BaseKey: a, For: "x"}, {BaseKey: b, For: "x"}},
	}
	changeBases(&st, []syncer.ConfirmedBase{{BaseKey: a, For: "x"}, {BaseKey: b, For: "x"}}, nil)
	if m := st.Groups[0].Models; m[0].Plan != "fa" || m[1].Plan != "fb" || len(st.Plans) != 2 {
		t.Errorf("the plans after a batch confirmed again: %q, %q, %v; want both kept", m[0].Plan, m[1].Plan, st.Plans)
	}
}
