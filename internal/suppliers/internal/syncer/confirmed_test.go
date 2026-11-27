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
		basis     string // the plan's; "" for none
	}{
		{"a published price wins over a base", adapter.Model{UpstreamName: "m1", Cost: &published, Published: true}, "1.5", base, "published"},
		{"a price no schedule expresses leaves the cost unknown", adapter.Model{UpstreamName: "m1", Published: true}, "1.5", base, ""},
		{"no price stated: the base", adapter.Model{UpstreamName: "m1"}, "1.5", base, "confirmed"},
		{"a base past a unit price", adapter.Model{UpstreamName: "m1"}, "100000000000", base, ""},
		{"no price and no base", adapter.Model{UpstreamName: "m1"}, "1.5", nil, ""},
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
		if basis != c.basis || len(st.plans) > 1 || st.state.Tasks[TaskCatalog].Failures != 0 {
			t.Errorf("%s: plans %+v, catalog task %+v; want one plan of basis %q", c.name, st.plans, st.state.Tasks[TaskCatalog], c.basis)
		}
	}

	for _, c := range []struct {
		name  string
		model adapter.Model
		kept  bool // the plan survives a round that reads no models after the base is cleared
	}{
		{"a published plan is kept", adapter.Model{UpstreamName: "m1", Cost: &published, Published: true}, true},
		{"a plan made of a cleared base is not", adapter.Model{UpstreamName: "m1"}, false},
	} {
		m := healthy()
		m.offer = offerOf(c.model, "1.5")
		e, st, clk := engine(m)
		st.state.Confirmed = base
		round(t, e)
		st.state.Confirmed = nil
		m.offer = func(adapter.Listing) (adapter.Page[adapter.Group], error) {
			return adapter.Page[adapter.Group]{Done: true, Items: []adapter.Group{{Ref: "1", Name: "g", Ratio: "1.5", Facts: adapter.GroupFacts(`{}`), ModelsUnread: true}}}, nil
		}
		clk.advance(10 * time.Minute)
		round(t, e)
		if kept := st.state.Plans["1/m1"] != ""; kept != c.kept {
			t.Errorf("%s: plan %q after the base was cleared, want kept %t", c.name, st.state.Plans["1/m1"], c.kept)
		}
	}
}
