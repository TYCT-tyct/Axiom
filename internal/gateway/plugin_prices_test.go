package gateway

import (
	"testing"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/observability"
	"axiom/internal/platform"
	"axiom/internal/protocol"
)

// TestPluginPricesGiveTheToolSumToALoneRun: the settlement's per-use sum is a run's price only when the
// run alone added every billed per-use item.
func TestPluginPricesGiveTheToolSumToALoneRun(t *testing.T) {
	t.Parallel()
	web := observability.PluginRun{Plugin: observability.PluginWeb, OK: true, Engine: "searxng", Uses: 1}
	failed := observability.PluginRun{Plugin: observability.PluginWeb, Engine: "searxng"}
	parse := observability.PluginRun{Plugin: observability.PluginFileParser, OK: true, Engine: protocol.FileEngineMistralOCR, Uses: 3}
	final := func(cost string, counts map[catalog.Item]int64) billing.Final {
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		f := billing.Final{Segments: []billing.Segment{{Usage: u}}}
		if cost != "" {
			c, err := platform.ParseExact(cost)
			if err != nil {
				t.Fatal(err)
			}
			f.ServerToolCost = &c
		}
		return f
	}
	searched := map[catalog.Item]int64{catalog.ItemInput: 10, catalog.ItemWebSearch: 1}
	for _, c := range []struct {
		name    string
		runs    []observability.PluginRun
		request protocol.Request
		f       billing.Final
		pending bool
		want    []int64
	}{
		{"none ran", nil, protocol.Request{}, final("", nil), false, nil},
		{"the one search is the per-use sum", []observability.PluginRun{web}, protocol.Request{}, final("0.01", searched), false, []int64{10_000_000}},
		{"a fraction of a billionth rounds up", []observability.PluginRun{web}, protocol.Request{}, final("0.0000000001", searched), false, []int64{1}},
		{
			"the upstream's own code run is in the sum",
			[]observability.PluginRun{web},
			protocol.Request{},
			final("0.02", map[catalog.Item]int64{catalog.ItemWebSearch: 1, catalog.ItemCodeExecution: 1}), false,
			[]int64{-1},
		},
		{"a settlement not written yet", []observability.PluginRun{web}, protocol.Request{}, billing.Final{}, true, []int64{-1}},
		{"a failed search was charged nothing", []observability.PluginRun{failed}, protocol.Request{}, billing.Final{}, false, []int64{0}},
		{
			"parsed pages are the file parse items",
			[]observability.PluginRun{parse},
			protocol.Request{FileBilling: true, FilePages: 3},
			final("0.06", map[catalog.Item]int64{catalog.ItemFileParse: 3}), false,
			[]int64{60_000_000},
		},
		{
			"a free parse added nothing and costs nothing",
			[]observability.PluginRun{parse},
			protocol.Request{FileBilling: true},
			final("", map[catalog.Item]int64{catalog.ItemInput: 10}), false,
			[]int64{0},
		},
		{
			"two runs share one sum",
			[]observability.PluginRun{parse, web},
			protocol.Request{FileBilling: true, FilePages: 3},
			final("0.07", map[catalog.Item]int64{catalog.ItemWebSearch: 1, catalog.ItemFileParse: 3}), false,
			[]int64{-1, -1},
		},
	} {
		got := pluginPrices(&Call{Request: c.request, plugins: c.runs}, c.f, c.pending)
		if len(got) != len(c.want) {
			t.Fatalf("%s: %d runs, want %d", c.name, len(got), len(c.want))
		}
		for i, run := range got {
			if run.PriceNano != c.want[i] {
				t.Errorf("%s: run %d priced %d, want %d", c.name, i, run.PriceNano, c.want[i])
			}
		}
	}
}
