package catalog_test

import (
	"slices"
	"testing"

	"axiom/internal/catalog"
)

func TestNewUsage(t *testing.T) {
	if _, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 0, catalog.ItemWebSearch: 1e10},
		catalog.ServiceTierFast, nil); err != nil {
		t.Errorf("NewUsage with 0 and 10¹⁰: %v", err)
	}
	if u, err := catalog.NewUsage(nil, catalog.ServiceTierFast+1, nil); err == nil {
		t.Errorf("NewUsage with an unknown service tier = %v, want an error", u)
	}
	for name, bad := range map[string]map[catalog.Item]int64{
		"negative":     {catalog.ItemOutput: -1},
		"above 10¹⁰":   {catalog.ItemCacheRead: 1e10 + 1},
		"unknown item": {catalog.NumItems: 1},
	} {
		if u, err := catalog.NewUsage(bad, catalog.ServiceTierStandard, nil); err == nil {
			t.Errorf("%s: NewUsage(%v) = %v, want an error", name, bad, u)
		}
	}
}

// The stored names round-trip: every value parses back from its String, and an unknown name is an error.
func TestParseNames(t *testing.T) {
	for item := range catalog.NumItems {
		if got, err := catalog.ParseItem(item.String()); err != nil || got != item {
			t.Errorf("ParseItem(%q) = %v, %v; want %v", item.String(), got, err, item)
		}
	}
	for tier := catalog.ServiceTierStandard; tier <= catalog.ServiceTierFast; tier++ {
		if got, err := catalog.ParseServiceTier(tier.String()); err != nil || got != tier {
			t.Errorf("ParseServiceTier(%q) = %v, %v; want %v", tier.String(), got, err, tier)
		}
	}
	if got, err := catalog.ParseItem("no_such_item"); err == nil {
		t.Errorf("ParseItem of an unknown name = %v, want an error", got)
	}
	if got, err := catalog.ParseServiceTier("no_such_tier"); err == nil {
		t.Errorf("ParseServiceTier of an unknown name = %v, want an error", got)
	}
}

// Usage the item table does not map, and a service tier value protocol does not recognise, reach billing as they were
// reported instead of being dropped or guessed (README.md「计费项与用量」); a Usage does not change once made.
func TestUsageUnknown(t *testing.T) {
	reported := []catalog.UnknownUsage{
		{Field: "usage.server_tool_use.future_tool_requests", Value: "1e0"},
		{Field: "usage.service_tier", Value: `"scale"`},
	}
	want := slices.Clone(reported)
	u, err := catalog.NewUsage(counts{in: 5}, catalog.ServiceTierStandard, reported)
	if err != nil {
		t.Fatal(err)
	}
	reported[0].Value = "2"
	got := u.Unknown()
	if !slices.Equal(got, want) {
		t.Fatalf("Unknown = %v, want %v", got, want)
	}
	got[1].Value = `"flex"`
	if again := u.Unknown(); !slices.Equal(again, want) {
		t.Errorf("Unknown after its result changed = %v, want %v", again, want)
	}
	if got := mustUsage(t, counts{in: 5}).Unknown(); len(got) != 0 {
		t.Errorf("Unknown of fully mapped usage = %v, want none", got)
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
		u, err := catalog.NewUsage(tc.counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := u.OutputTotal(); got != tc.want {
			t.Errorf("OutputTotal of %v = %d, want %d", tc.counts, got, tc.want)
		}
	}
}

// TestUnits: every item is billed in the unit its price is for, and the counted set holds exactly the items counted per
// piece, second or character rather than per token or use. A new item without a unit fails here.
func TestUnits(t *testing.T) {
	want := map[catalog.Item]catalog.Unit{
		catalog.ItemInput: catalog.UnitToken, catalog.ItemCacheRead: catalog.UnitToken,
		catalog.ItemCacheWrite: catalog.UnitToken, catalog.ItemCacheWrite1h: catalog.UnitToken,
		catalog.ItemAudioIn: catalog.UnitToken, catalog.ItemImageIn: catalog.UnitToken,
		catalog.ItemOutput: catalog.UnitToken, catalog.ItemReasoning: catalog.UnitToken,
		catalog.ItemAudioOut: catalog.UnitToken, catalog.ItemImageOut: catalog.UnitToken,
		catalog.ItemWebSearch: catalog.UnitUse, catalog.ItemCodeExecution: catalog.UnitUse,
		catalog.ItemFileSearch: catalog.UnitUse, catalog.ItemFileParse: catalog.UnitUse,
		catalog.ItemImageInPiece: catalog.UnitPiece, catalog.ItemImageOutPiece: catalog.UnitPiece,
		catalog.ItemAudioInSecond: catalog.UnitSecond, catalog.ItemAudioOutSecond: catalog.UnitSecond,
		catalog.ItemCharacterIn: catalog.UnitCharacter, catalog.ItemRequest: catalog.UnitUse,
	}
	if len(want) != int(catalog.NumItems) {
		t.Fatalf("the table names %d items, want %d", len(want), catalog.NumItems)
	}
	for item := range catalog.NumItems {
		if got := item.Unit(); got != want[item] {
			t.Errorf("%s is billed per %v, want %v", item, got, want[item])
		}
		if counted := want[item] != catalog.UnitToken && want[item] != catalog.UnitUse; catalog.CountedItems.Has(item) != counted {
			t.Errorf("%s: counted %v, want %v", item, catalog.CountedItems.Has(item), counted)
		}
		if token := want[item] == catalog.UnitToken; catalog.TokenItems.Has(item) != token {
			t.Errorf("%s: a token item %v, want %v", item, catalog.TokenItems.Has(item), token)
		}
	}
	for unit, name := range map[catalog.Unit]string{
		catalog.UnitToken: "token", catalog.UnitUse: "use", catalog.UnitPiece: "piece",
		catalog.UnitSecond: "second", catalog.UnitCharacter: "character",
	} {
		if got := unit.String(); got != name {
			t.Errorf("unit %d is %q, want %q", unit, got, name)
		}
	}
}
