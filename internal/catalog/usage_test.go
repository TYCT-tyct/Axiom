package catalog_test

import (
	"testing"

	"axiom/internal/catalog"
)

func TestNewUsage(t *testing.T) {
	if _, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 0, catalog.ItemWebSearch: 1e10}); err != nil {
		t.Errorf("NewUsage with 0 and 10¹⁰: %v", err)
	}
	for name, counts := range map[string]map[catalog.Item]int64{
		"negative":     {catalog.ItemOutput: -1},
		"above 10¹⁰":   {catalog.ItemCacheRead: 1e10 + 1},
		"unknown item": {catalog.Item(200): 1},
	} {
		if u, err := catalog.NewUsage(counts); err == nil {
			t.Errorf("%s: NewUsage(%v) = %v, want an error", name, counts, u)
		}
	}
}

// A reply that only reasoned produced output: the zero-output waiver (docs/domains/billing.md「免单规则」) reads
// OutputTotal, which must not be zero for it.
func TestOutputTotal(t *testing.T) {
	for _, tc := range []struct {
		counts map[catalog.Item]int64
		want   int64
	}{
		{map[catalog.Item]int64{catalog.ItemReasoning: 500}, 500},
		{map[catalog.Item]int64{catalog.ItemOutput: 20, catalog.ItemReasoning: 500, catalog.ItemInput: 9}, 520},
		{map[catalog.Item]int64{catalog.ItemInput: 9}, 0},
	} {
		u, err := catalog.NewUsage(tc.counts)
		if err != nil {
			t.Fatal(err)
		}
		if got := u.OutputTotal(); got != tc.want {
			t.Errorf("OutputTotal of %v = %d, want %d", tc.counts, got, tc.want)
		}
	}
}
