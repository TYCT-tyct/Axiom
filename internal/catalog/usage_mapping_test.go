package catalog_test

import (
	"fmt"
	"testing"

	"axiom/internal/catalog"
)

func TestPhysicalInputSelectsContextBand(t *testing.T) {
	for _, tc := range []struct {
		name, basis string
		above       int
		want        string
	}{
		{"whole prompt", `"input","cache_read","cache_write"`, 150, "160"},
		{"physical boundary", `"input","cache_read","cache_write"`, 100, "160"},
		{"above physical boundary", `"input","cache_read","cache_write"`, 99, "240"},
		{"partial basis stays partial", `"cache_read"`, 90, "160"},
		{"partial basis crosses its own boundary", `"cache_read"`, 79, "240"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := mustParse(t, fmt.Sprintf(`{"v":1,"currency":"USD","rates":{"cache_read":"1","cache_write":"1"},"context_tiers":{"basis":[%s],"method":"whole","tiers":[{"above":%d,"rates":{"cache_write":"2"}}]}}`, tc.basis, tc.above))
			u := mustUsage(t, counts{cacheRead: 80, cacheWrite: 80})
			u, err := u.WithPhysicalInput(100)
			if err != nil {
				t.Fatal(err)
			}
			cost, missing, err := s.Cost(u)
			if err != nil || missing != 0 || cost.Rat().RatString() != tc.want {
				t.Fatalf("cost %v, missing %v, %v; want %s", cost.Rat(), missing, err, tc.want)
			}
		})
	}
}

// A whole-prompt basis reads the physical total even when the summed quantities fall short of it.
func TestPhysicalInputAboveSummedQuantities(t *testing.T) {
	s := mustParse(t, `{"v":1,"currency":"USD","rates":{"cache_read":"1","cache_write":"1"},"context_tiers":{"basis":["input","cache_read","cache_write"],"method":"whole","tiers":[{"above":90,"rates":{"cache_write":"2"}}]}}`)
	u, err := mustUsage(t, counts{cacheRead: 40, cacheWrite: 40}).WithPhysicalInput(100)
	if err != nil {
		t.Fatal(err)
	}
	cost, missing, err := s.Cost(u)
	if err != nil || missing != 0 || cost.Rat().RatString() != "120" {
		t.Fatalf("cost %v, missing %v, %v; want 120", cost.Rat(), missing, err)
	}
}

func TestPhysicalInputPresenceAndRecount(t *testing.T) {
	u := mustUsage(t, counts{in: 10})
	if _, ok := u.PhysicalInput(); ok {
		t.Fatal("absent input became explicit")
	}
	for _, n := range []int64{0, 100, 1e10} {
		v, err := u.WithPhysicalInput(n)
		if err != nil {
			t.Fatal(err)
		}
		v, err = v.Recount(counts{cacheRead: 80, cacheWrite: 80}, nil)
		if got, ok := v.PhysicalInput(); err != nil || !ok || got != n || v.InputTotal() != n {
			t.Fatalf("recount lost %d: %+v, %v", n, v, err)
		}
	}
	for _, n := range []int64{-1, 1e10 + 1} {
		if _, err := u.WithPhysicalInput(n); err == nil {
			t.Errorf("accepted %d", n)
		}
	}
}

func TestImageEffectivePriceAtEveryTierAndBand(t *testing.T) {
	for _, own := range []string{"", `,"image_in":"0","image_out":"0"`, `,"image_in":"9","image_out":"10"`} {
		s := mustParse(t, fmt.Sprintf(`{"v":1,"currency":"USD","rates":{"input":"1","output":"2"%s},
			"context_tiers":{"basis":["input","image_in"],"method":"whole","tiers":[{"above":150,"rates":{"input":"5","output":"6"}}]},
			"service_tiers":{"priority":{"rates":{"input":"3","output":"4"%s},"context_tiers":[{"above":150,"rates":{"input":"7","output":"8"}}]}}}`, own, own))
		items := catalog.ItemSet(1<<imageIn | 1<<imageOut)
		if missing := s.Missing(catalog.Reportable{Items: items, ServiceTiers: 1 << catalog.ServiceTierPriority}); missing != (catalog.Reportable{}) {
			t.Fatalf("missing image prices: %+v", missing)
		}
		for _, st := range []catalog.ServiceTier{catalog.ServiceTierStandard, catalog.ServiceTierPriority} {
			for _, n := range []int64{100, 150, 151} {
				pi, po := int64(1), int64(2)
				if st == catalog.ServiceTierPriority {
					pi, po = 3, 4
				}
				if n > 150 {
					pi, po = pi+4, po+4
				}
				ii, io := pi, po
				if own != "" {
					ii, io = 9, 10
					if own == `,"image_in":"0","image_out":"0"` {
						ii, io = 0, 0
					}
				}
				u := mustTierUsage(t, st, counts{in: n - 10, imageIn: 10, imageOut: 20})
				cost, missing, err := s.Cost(u)
				want := (n-10)*pi + 10*ii + 20*io
				if err != nil || missing != 0 || cost.Rat().RatString() != fmt.Sprint(want) {
					t.Fatalf("own=%s tier=%v input=%d: cost=%v want=%d missing=%v err=%v", own, st, n, cost.Rat(), want, missing, err)
				}
				for it, rate := range map[catalog.Item]int64{imageIn: ii, imageOut: io} {
					ceiling, ok := s.Ceiling(1<<it, 1<<st, n)
					if !ok || ceiling.Rat().RatString() != fmt.Sprint(rate) {
						t.Fatalf("ceiling %s = %v/%t, want %d", it, ceiling.Rat(), ok, rate)
					}
				}
			}
		}
	}
}

func TestImageFallbackDoesNotCrossServiceTiersOrOverrideZero(t *testing.T) {
	s := mustParse(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":150,"rates":{"image_out":"0"}}]},
		"service_tiers":{"priority":{"rates":{"input":"3"},"context_tiers":[{"above":150,"rates":{}}]}}}`)
	for _, tc := range []struct {
		tier    catalog.ServiceTier
		input   int64
		want    string
		missing catalog.ItemSet
	}{
		{catalog.ServiceTierStandard, 100, "102", 0},
		{catalog.ServiceTierStandard, 151, "151", 0},
		{catalog.ServiceTierPriority, 100, "300", 1 << imageOut},
		{catalog.ServiceTierPriority, 151, "453", 1 << imageOut},
	} {
		cost, missing, err := s.Cost(mustTierUsage(t, tc.tier, counts{in: tc.input, imageOut: 1}))
		if err != nil || missing != tc.missing || cost.Rat().RatString() != tc.want {
			t.Fatalf("cost %v, missing %v, err %v; want %+v", cost.Rat(), missing, err, tc)
		}
	}
	m := s.Missing(catalog.Reportable{Items: 1 << imageOut, ServiceTiers: 1 << catalog.ServiceTierPriority})
	if m.Items != 0 || m.ServiceTiers != 1<<catalog.ServiceTierPriority {
		t.Fatalf("listing: %+v", m)
	}
	if _, ok := s.Ceiling(1<<imageOut, 1<<catalog.ServiceTierPriority, 151); ok {
		t.Fatal("priority inherited a standard image price")
	}
}
