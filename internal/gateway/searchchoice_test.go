package gateway

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/websearch"
)

// fakeEngines is the engine set a decision reads, without loading or opening a node's own.
type fakeEngines struct {
	byName map[string]*websearch.Backend
	def    *websearch.Backend
}

func (f fakeEngines) Find(name string) (*websearch.Backend, bool) {
	b, ok := f.byName[name]
	return b, ok
}

func (f fakeEngines) Default() (*websearch.Backend, bool) { return f.def, f.def != nil }
func (f fakeEngines) List() []websearch.Engine {
	var list []websearch.Engine
	for _, b := range f.byName {
		list = append(list, b.Engine)
	}
	return list
}
func (f fakeEngines) Configured() bool { return len(f.byName) > 0 }

//nolint:modernize // the embedded field's key is its type
func pricedEngine(name, search, result string) *websearch.Backend {
	price := func(s string) platform.UnitPrice {
		p, err := platform.ParseUnitPrice(s)
		if err != nil {
			panic(err)
		}
		return p
	}
	return &websearch.Backend{Engine: websearch.Engine{Name: name, PriceSearch: price(search), PriceResult: price(result)}}
}

//nolint:gocognit // the table is the test
func TestChooseSearch(t *testing.T) {
	t.Parallel()
	paid := pricedEngine("paid", "0.005", "0.0125")
	def := pricedEngine("default", "0.01", "0")
	all := fakeEngines{byName: map[string]*websearch.Backend{"paid": paid, "default": def}, def: def}
	for _, tc := range []struct {
		name     string
		web      *protocol.WebPlugin
		choice   searchChoice
		declared bool
		native   int
		backend  *websearch.Backend
		price    string
		err      string
	}{
		{name: "native asked for", web: &protocol.WebPlugin{Engine: "native", MaxResults: 5}, choice: searchChoice{Native: true, MaxUses: 3}, native: 3},
		{name: "native asked for without the ability", web: &protocol.WebPlugin{Engine: "native", MaxResults: 5}, err: "no native web search"},
		{name: "native default without the ability", web: &protocol.WebPlugin{MaxResults: 2}, backend: def, price: "0.01"},
		{name: "native default with the ability", web: &protocol.WebPlugin{MaxResults: 2}, choice: searchChoice{Native: true, MaxUses: 4}, native: 4},
		{name: "a model bound of zero uses one", web: &protocol.WebPlugin{MaxResults: 2}, choice: searchChoice{Native: true}, native: 1},
		{name: "a model bound above the cap uses ten", web: &protocol.WebPlugin{MaxResults: 2}, choice: searchChoice{Native: true, MaxUses: 99}, native: 10},
		{name: "a named engine", web: &protocol.WebPlugin{Engine: "paid", MaxResults: 3}, choice: searchChoice{Native: true}, backend: paid, price: "0.03"},
		{name: "an unknown engine", web: &protocol.WebPlugin{Engine: "gone", MaxResults: 3}, err: "not an enabled search engine"},
		{name: "the request declares its own tool", web: &protocol.WebPlugin{Engine: "paid"}, declared: true},
		{name: "the model always searches", web: &protocol.WebPlugin{Engine: "paid"}, choice: searchChoice{Always: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chooseSearch(tc.web, tc.choice, tc.declared, all)
			if tc.err != "" {
				var refusal *Refusal
				if !errors.As(err, &refusal) || !strings.Contains(refusal.Message, tc.err) {
					t.Fatalf("chooseSearch() error = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("chooseSearch() error = %v", err)
			}
			if got.native != tc.native || got.backend != tc.backend {
				t.Fatalf("chooseSearch() = %+v, want native %d backend %v", got, tc.native, tc.backend)
			}
			if tc.price != "" && got.price.String() != tc.price {
				t.Fatalf("price = %s, want %s", got.price, tc.price)
			}
		})
	}
}

func TestQuotablesSellsTheEnginePrice(t *testing.T) {
	t.Parallel()
	schedule, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000001","web_search":"0.01"}}`))
	if err != nil {
		t.Fatal(err)
	}
	price, err := platform.ParseUnitPrice("0.03")
	if err != nil {
		t.Fatal(err)
	}
	entries := []routing.Entry{{Price: routing.PriceRef{Schedule: schedule}}}
	got := quotables(entries, &Call{searchBackend: &websearch.Backend{}, searchPrice: price})
	var found bool
	for _, r := range got[0].Price.View().Rates {
		if r.Item == catalog.ItemWebSearch {
			found = true
			if r.Price != price {
				t.Errorf("web_search rate = %s, want the engine's %s", r.Price, price)
			}
		}
	}
	if !found {
		t.Fatal("the quoted schedule has no web_search rate")
	}
	for _, r := range entries[0].Price.Schedule.View().Rates {
		if r.Item == catalog.ItemWebSearch && r.Price.String() != "0.01" {
			t.Fatalf("the locked schedule was repriced to %s; the engine rate must apply to this call only", r.Price)
		}
	}
	// A zero engine price keeps the model's published rate.
	if got := quotables(entries, &Call{}); got[0].Price.View().Rates[1].Price.String() != "0.01" {
		t.Errorf("a zero engine price did not keep the model's rate: %+v", got[0].Price.View().Rates)
	}
}
