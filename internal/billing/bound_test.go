package billing

import (
	"testing"

	"axiom/internal/catalog"
)

// TestInputBound: the longest prompt a quote prices is the text, the images and the files at their most, held to the
// context; a request that can sample again, by tool use, can reread the whole context.
func TestInputBound(t *testing.T) {
	t.Parallel()
	model := Model{ContextLength: 100000, ImageTokens: 1000, PageTokens: 100, FilePages: 50, MaxUses: map[catalog.Item]int64{catalog.ItemWebSearch: 3}}
	cases := []struct {
		name           string
		demand         Demand
		first, longest int64
	}{
		{"text only", Demand{TextBytes: 4000}, 4000, 4000},
		{"images", Demand{TextBytes: 4000, Images: 3}, 7000, 7000},
		{"a file at its pages' most", Demand{TextBytes: 4000, Documents: 2}, 14000, 14000},
		{"held to the context", Demand{TextBytes: 90000, Images: 50}, 100000, 100000},
		{"a search: later samplings reread the context", Demand{TextBytes: 4000, ServerTools: []ServerTool{{Item: catalog.ItemWebSearch, MaxUses: 2}}}, 4000, 100000},
		{"a tool with the model's default uses", Demand{TextBytes: 4000, ServerTools: []ServerTool{{Item: catalog.ItemWebSearch}}}, 4000, 100000},
	}
	for _, c := range cases {
		if first, longest := InputBound(c.demand, model); first != c.first || longest != c.longest {
			t.Errorf("%s: %d and %d, want %d and %d", c.name, first, longest, c.first, c.longest)
		}
	}
}
