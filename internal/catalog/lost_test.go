package catalog_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
)

// TestLost lists the prices the old schedule had and the new one lacks, per service tier, band and item.
func TestLost(t *testing.T) {
	t.Parallel()
	const (
		priorityIn = `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"},
			"service_tiers":{"priority":{"rates":{"input":"2","output":"4"}}}}`
		priorityOutOnly = `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"},
			"service_tiers":{"priority":{"rates":{"output":"4"}}}}`
		// oldWithoutWeb has priority input and output, and no web_search.
		tiered = `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"},
			"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":200000,"rates":{"output":"4"}}]}}`
		tieredNoHighOutput = `{"v":1,"currency":"USD","rates":{"input":"1"},
			"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":200000,"rates":{"input":"2"}}]}}`
	)
	in, out, reasoning := catalog.ItemInput, catalog.ItemOutput, catalog.ItemReasoning
	var priority catalog.ServiceTierSet
	priority.Add(catalog.ServiceTierPriority)
	items := func(its ...catalog.Item) (s catalog.ItemSet) {
		for _, it := range its {
			s.Add(it)
		}
		return s
	}
	std, prio := catalog.ServiceTierStandard, catalog.ServiceTierPriority
	cases := []struct {
		name, old, new string
		r              catalog.Reportable
		want           []catalog.PricePoint
	}{
		{"nothing lost", priorityIn, priorityIn, catalog.Reportable{Items: items(in, out), ServiceTiers: priority}, nil},
		{
			"the new priority has only output", priorityIn, priorityOutOnly,
			catalog.Reportable{Items: items(in, out), ServiceTiers: priority},
			[]catalog.PricePoint{{ServiceTier: prio, Item: in}},
		},
		{
			"the new schedule has no priority", priorityIn, `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"}}`,
			catalog.Reportable{Items: items(in, out), ServiceTiers: priority},
			[]catalog.PricePoint{{ServiceTier: prio, Item: in}, {ServiceTier: prio, Item: out}},
		},
		{
			"a tier the model does not report is not asked about", priorityIn, `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"}}`,
			catalog.Reportable{Items: items(in, out)},
			nil,
		},
		{
			"reasoning falls back to output on both sides",
			`{"v":1,"currency":"USD","rates":{"output":"2","reasoning":"3"}}`, `{"v":1,"currency":"USD","rates":{"output":"2"}}`,
			catalog.Reportable{Items: items(reasoning)},
			nil,
		},
		{
			"a new threshold whose high band lacks output", `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"}}`, tieredNoHighOutput,
			catalog.Reportable{Items: items(in, out)},
			[]catalog.PricePoint{{ServiceTier: std, Above: 0, Item: out}, {ServiceTier: std, Above: 200000, Item: out}},
		},
		{
			"the new schedule drops the old threshold", tiered, `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"}}`,
			catalog.Reportable{Items: items(in, out)},
			nil,
		},
	}
	for _, c := range cases {
		got, err := mustParse(t, c.new).Lost(mustParse(t, c.old), c.r)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.name, diff)
		}
	}
}

// TestLostBasis: thresholds that count other items mean other lengths, so two tiered schedules cannot be compared. One
// request (input 1, cache_read 200000, output 1) has an output price in the old schedule and none in the new, which
// is why the answer is an error rather than "nothing lost". A schedule without tiers has no basis to differ.
func TestLostBasis(t *testing.T) {
	t.Parallel()
	highOutputOnly := func(basis string) string {
		return `{"v":1,"currency":"USD","rates":{"input":"1"},"service_tiers":{"priority":{"rates":{"input":"2"},
			"context_tiers":[{"above":200000,"rates":{"output":"5"}}]}},
			"context_tiers":{"basis":[` + basis + `],"method":"whole","tiers":[{"above":200000,"rates":{}}]}}`
	}
	var r catalog.Reportable
	r.Items.Add(catalog.ItemOutput)
	r.ServiceTiers.Add(catalog.ServiceTierPriority)
	oldS, newS := mustParse(t, highOutputOnly(`"input","cache_read"`)), mustParse(t, highOutputOnly(`"input"`))
	if _, err := newS.Lost(oldS, r); !errors.Is(err, catalog.ErrTierBasisChanged) {
		t.Errorf("different bases: %v", err)
	}
	if _, err := oldS.Lost(oldS, r); err != nil {
		t.Errorf("the same basis: %v", err)
	}
	plain := mustParse(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"}}`)
	for name, pair := range map[string][2]catalog.Schedule{"new without tiers": {plain, oldS}, "old without tiers": {newS, plain}} {
		if _, err := pair[0].Lost(pair[1], r); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
