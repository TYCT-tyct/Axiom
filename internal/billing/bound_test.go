package billing

import (
	"math/rand/v2"
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

// TestInputEstimate: what a charge takes as the input when the upstream reported none is the text at 4 bytes a token
// and each image and file at 1600 and 4096 tokens, or at the model's bound when that is smaller, held to the context;
// the reservation's bound stays the context for an image or a file of unknown size.
func TestInputEstimate(t *testing.T) {
	t.Parallel()
	unknown := Model{ContextLength: 200000}
	cases := []struct {
		name     string
		demand   Demand
		model    Model
		estimate int64
		bound    int64 // InputBound's first
	}{
		{"text only, a token begun is charged", Demand{TextBytes: 4001}, unknown, 1001, 4001},
		{"an image and a file of unknown size", Demand{TextBytes: 4000, Images: 1, Documents: 1}, unknown, 1000 + 1600 + 4096, 200000},
		{"two images", Demand{Images: 2}, unknown, 3200, 200000},
		{"a small window holds the estimate", Demand{TextBytes: 4000, Images: 1, Documents: 1}, Model{ContextLength: 4000}, 4000, 4000},
		{"an image bound below the estimate", Demand{Images: 2}, Model{ContextLength: 200000, ImageTokens: 500}, 1000, 1000},
		{"an image bound above the estimate", Demand{Images: 2}, Model{ContextLength: 200000, ImageTokens: 5000}, 3200, 10000},
		{"a file bound below the estimate", Demand{Documents: 1}, Model{ContextLength: 200000, PageTokens: 100, FilePages: 2}, 200, 200},
		{"a file bound above the estimate", Demand{Documents: 1}, Model{ContextLength: 200000, PageTokens: 3000, FilePages: 100}, 4096, 200000},
	}
	for _, c := range cases {
		first, _ := InputBound(c.demand, c.model)
		if got := InputEstimate(c.demand, c.model); got != c.estimate || first != c.bound {
			t.Errorf("%s: estimate %d and bound %d, want %d and %d", c.name, got, first, c.estimate, c.bound)
		}
	}
}

// TestInputEstimateNeverExceedsTheBound: the reservation covers the first sampling at InputBound, so a charge on the
// estimate is never above what was reserved, whatever the model's data and the request.
func TestInputEstimateNeverExceedsTheBound(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // G404: test data, not a secret
	for range 2000 {
		c := randomQuoteCase(t, r, 1<<20)
		first, _ := InputBound(c.d, c.e.Model)
		if est := InputEstimate(c.d, c.e.Model); est > first {
			t.Fatalf("estimate %d above the bound %d: %+v", est, first, c)
		}
	}
}
