package catalog_test

import (
	"slices"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// Missing lists what an upstream may report and the schedule cannot price, for billing's listing threshold: standard
// items unpriced in any band, and service tiers the schedule lacks or that lack any reportable item in any band
// (README.md「上架门槛与缺项」).
func TestMissing(t *testing.T) {
	flex, priority, fast := catalog.ServiceTierFlex, catalog.ServiceTierPriority, catalog.ServiceTierFast
	// Output is priced only above the threshold, so a short request's output has no price.
	outputAboveOnly := `{"v":1,"currency":"USD","rates":{"input":"1"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":10,"rates":{"output":"2"}}]}}`
	for _, tc := range []struct {
		name, schedule string
		items          []catalog.Item
		tiers          []catalog.ServiceTier
		wantItems      []catalog.Item
		wantTiers      []catalog.ServiceTier
	}{
		{"complete", serviceTiers, []catalog.Item{in, cacheRead, out}, nil, nil, nil},
		{"reasoning falls back to output", serviceTiers, []catalog.Item{in, out, reasoning}, []catalog.ServiceTier{priority, fast}, nil, nil},
		{"unpriced items", serviceTiers, []catalog.Item{in, audioIn, out, webSearch}, nil, []catalog.Item{audioIn, webSearch}, nil},
		{"a service tier without a reportable item", serviceTiers, []catalog.Item{in, cacheRead, out}, []catalog.ServiceTier{priority, fast}, nil, []catalog.ServiceTier{priority, fast}},
		{"a service tier the schedule lacks", serviceTiers, []catalog.Item{in, out}, []catalog.ServiceTier{flex, priority}, nil, []catalog.ServiceTier{flex}},
		{"a service tier the schedule lacks, no items", serviceTiers, nil, []catalog.ServiceTier{flex}, nil, []catalog.ServiceTier{flex}},
		{"items the upstream cannot report", serviceTiers, []catalog.Item{in}, []catalog.ServiceTier{priority}, nil, nil},
		{"an item priced only above a threshold", outputAboveOnly, []catalog.Item{in, out}, nil, []catalog.Item{out}, nil},
	} {
		var r catalog.Reportable
		for _, it := range tc.items {
			r.Items.Add(it)
		}
		for _, st := range tc.tiers {
			r.ServiceTiers.Add(st)
		}
		got := mustParse(t, tc.schedule).Missing(r)
		if !slices.Equal(members(got.Items), tc.wantItems) || !slices.Equal(serviceTierMembers(got.ServiceTiers), tc.wantTiers) {
			t.Errorf("%s: Missing = %v, %v; want %v, %v",
				tc.name, members(got.Items), serviceTierMembers(got.ServiceTiers), tc.wantItems, tc.wantTiers)
		}
	}
}

func serviceTierMembers(s catalog.ServiceTierSet) []catalog.ServiceTier {
	var tiers []catalog.ServiceTier
	for _, st := range allServiceTiers {
		if s.Has(st) {
			tiers = append(tiers, st)
		}
	}
	return tiers
}

// Fill prices an item every offered service tier lacks at its base, in every band, and keeps what the schedule sets:
// the priority tier's own search price, and the standard tier's long-context output.
func TestFill(t *testing.T) {
	s := mustParse(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":10,"rates":{"output":"4"}}]},
		"service_tiers":{"priority":{"context_tiers":[{"above":10,"rates":{}}],"rates":{"input":"1","output":"3","web_search":"7"}}}}`)
	filled := s.Fill(map[catalog.Item]platform.UnitPrice{webSearch: mustPrice(t, "0.5"), codeExec: mustPrice(t, "0.25")})
	got, err := filled.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":10,"rates":{"output":"4"}}]},` +
		`"currency":"USD","rates":{"code_execution":"0.25","input":"1","output":"2","web_search":"0.5"},` +
		`"service_tiers":{"priority":{"context_tiers":[{"above":10,"rates":{}}],` +
		`"rates":{"code_execution":"0.25","input":"1","output":"3","web_search":"7"}}},"v":1}`
	if string(got) != want {
		t.Errorf("Fill =\n%s\nwant\n%s", got, want)
	}
	if m := filled.Missing(catalog.Reportable{Items: 1<<webSearch | 1<<codeExec, ServiceTiers: 1 << catalog.ServiceTierPriority}); m != (catalog.Reportable{}) {
		t.Errorf("filled schedule misses %+v", m)
	}
	if again, err := s.MarshalJSON(); err != nil || string(again) == want {
		t.Error("Fill changed the schedule it was called on")
	}
	if filled.Currency().String() != "USD" {
		t.Errorf("currency %s", filled.Currency())
	}
}

func mustPrice(tb testing.TB, s string) platform.UnitPrice {
	tb.Helper()
	p, err := platform.ParseUnitPrice(s)
	if err != nil {
		tb.Fatal(err)
	}
	return p
}
