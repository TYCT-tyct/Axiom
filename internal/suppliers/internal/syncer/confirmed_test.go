package syncer

import (
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// TestAConfirmedBaseStandsInOnlyForNoPrice: what a model's plan is made of, by what the supplier states of it and
// whether a base is confirmed. A price the supplier publishes wins, and so does one no schedule can express, which leaves
// the cost unknown rather than priced at the base. A base the ratio takes past a unit price leaves that one endpoint
// unknown and the round goes on. A model whose models were not read keeps a published plan, and gives up one made of a
// base that has been cleared since.
func TestAConfirmedBaseStandsInOnlyForNoPrice(t *testing.T) {
	t.Parallel()
	published := mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"2","output":"8"}}`)
	base := []ConfirmedBase{{GroupRef: "1", Model: "m1", Base: confirmedBase}}
	for _, c := range []struct {
		name      string
		model     adapter.Model
		ratio     adapter.Amount
		confirmed []ConfirmedBase
		basis     string     // the plan's; "" for none
		why       CostReason // why the cost is unknown without one
	}{
		{"a published price wins over a base", adapter.Model{UpstreamName: "m1", Cost: &published, Published: true}, "1.5", base, "published", ""},
		{"a price no schedule expresses leaves the cost unknown", adapter.Model{UpstreamName: "m1", Published: true}, "1.5", base, "", Unexpressible},
		{"no price stated: the base", adapter.Model{UpstreamName: "m1"}, "1.5", base, "confirmed", ""},
		{"a base past a unit price", adapter.Model{UpstreamName: "m1"}, "100000000000", base, "", OverLimit},
		{"no price and no base", adapter.Model{UpstreamName: "m1"}, "1.5", nil, "", Unpriced},
	} {
		m := healthy()
		m.offer = offerOf(c.model, c.ratio)
		e, st, _ := engine(m)
		st.state.Confirmed = c.confirmed
		round(t, e)
		basis := ""
		if len(st.plans) == 1 {
			basis = st.plans[0].Basis
		}
		why := st.state.Groups[0].Models[0].CostReason
		if basis != c.basis || why != c.why || len(st.plans) > 1 || st.state.Tasks[TaskCatalog].Failures != 0 {
			t.Errorf("%s: plans %+v, unknown for %q, catalog task %+v; want one plan of basis %q or unknown for %q",
				c.name, st.plans, why, st.state.Tasks[TaskCatalog], c.basis, c.why)
		}
	}

	// A round that reads no models keeps what the last read said: a published plan stays the plan, a base confirmed
	// since does not take its place, and a plan made of a base that has been cleared is gone.
	for _, c := range []struct {
		name          string
		model         adapter.Model
		before, after []ConfirmedBase // the bases of the round that reads the models, and of the one that does not
		basis         string          // the plan's after the second round; "" for none
		why           CostReason      // why the cost is unknown after it
	}{
		{"a published plan is kept", adapter.Model{UpstreamName: "m1", Cost: &published, Published: true}, base, nil, "published", ""},
		{"a published plan is kept over a base confirmed later", adapter.Model{UpstreamName: "m1", Cost: &published, Published: true}, nil, base, "published", ""},
		{"a published price no schedule expresses stays unknown", adapter.Model{UpstreamName: "m1", Published: true}, nil, base, "", Unexpressible},
		{"a plan made of a cleared base is not kept", adapter.Model{UpstreamName: "m1"}, base, nil, "", Unpriced},
	} {
		m := healthy()
		m.offer = offerOf(c.model, "1.5")
		e, st, clk := engine(m)
		st.state.Confirmed = c.before
		round(t, e)
		st.state.Confirmed = c.after
		m.offer = func(adapter.Listing) (adapter.Page[adapter.Group], error) {
			return adapter.Page[adapter.Group]{Done: true, Items: []adapter.Group{{Ref: "1", Name: "g", Ratio: "1.5", Facts: adapter.GroupFacts(`{}`), ModelsUnread: true}}}, nil
		}
		clk.advance(10 * time.Minute)
		round(t, e)
		basis := ""
		for _, p := range st.plans {
			if fp := st.state.Groups[0].Models[0].Plan; fp != "" && p.Fingerprint == fp {
				basis = p.Basis
			}
		}
		got, want := [2]string{basis, string(st.state.Groups[0].Models[0].CostReason)}, [2]string{c.basis, string(c.why)}
		if got != want {
			t.Errorf("%s: the plan after a round that read no models, and why the cost is unknown: %q, want %q", c.name, got, want)
		}
	}
}

// TestEndpointsWhoseJoinedNamesMeetKeepTheirOwnPlans: a plan is kept by its group and model, not by the two joined, so
// the model b/c of group a and the model c of group a/b each keep their own plan and their own confirmed base.
func TestEndpointsWhoseJoinedNamesMeetKeepTheirOwnPlans(t *testing.T) {
	t.Parallel()
	m := healthy()
	m.offer = func(adapter.Listing) (adapter.Page[adapter.Group], error) {
		return adapter.Page[adapter.Group]{Done: true, Items: []adapter.Group{
			{Ref: "a", Name: "a", Ratio: "1", Facts: adapter.GroupFacts(`{}`), Models: []adapter.Model{{UpstreamName: "b/c"}}},
			{Ref: "a/b", Name: "a/b", Ratio: "2", Facts: adapter.GroupFacts(`{}`), Models: []adapter.Model{{UpstreamName: "c"}}},
		}}, nil
	}
	e, st, _ := engine(m)
	st.state.Confirmed = []ConfirmedBase{
		{GroupRef: "a", Model: "b/c", Base: confirmedBase},
		{GroupRef: "a/b", Model: "c", Base: confirmedBase},
	}
	round(t, e)
	one, two := st.state.Groups[0].Models[0].Plan, st.state.Groups[1].Models[0].Plan
	if len(st.state.Plans) != 2 || one == "" || two == "" || one == two {
		t.Errorf("plans %v, fingerprints %q and %q; want two plans of their own, the second at twice the ratio", st.state.Plans, one, two)
	}
}
