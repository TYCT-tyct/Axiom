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

// A basis that names input but not image_in still counts the images as input: before images were split out of input
// they were part of it, so a prompt of 250000 tokens with 100000 of them images fell in the tier above 200000.
func TestInputBasisCountsImagesAsInput(t *testing.T) {
	for _, tc := range []struct {
		name, basis string
		c           counts
		physical    int64 // 0 for a usage that reports no physical total
		want        string
	}{
		{"images above the threshold", `"input"`, counts{in: 150000, imageIn: 100000}, 250000, "500000"},
		{"without a physical total", `"input"`, counts{in: 150000, imageIn: 100000}, 0, "500000"},
		{"images in the basis", `"input","image_in"`, counts{in: 150000, imageIn: 100000}, 250000, "500000"},
		{"below the threshold", `"input"`, counts{in: 100000, imageIn: 90000}, 190000, "190000"},
		{"a cache read outside the basis", `"input"`, counts{in: 150000, imageIn: 100000, cacheRead: 10000}, 260000, "510000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := mustParse(t, fmt.Sprintf(`{"v":1,"currency":"USD","rates":{"input":"1","cache_read":"1"},"context_tiers":{"basis":[%s],"method":"whole","tiers":[{"above":200000,"rates":{"input":"2"}}]}}`, tc.basis))
			u := mustUsage(t, tc.c)
			if tc.physical > 0 {
				var err error
				if u, err = u.WithPhysicalInput(tc.physical); err != nil {
					t.Fatal(err)
				}
			}
			cost, missing, err := s.Cost(u)
			if err != nil || missing != 0 || cost.Rat().RatString() != tc.want {
				t.Fatalf("cost %v, missing %v, %v; want %s", cost.Rat(), missing, err, tc.want)
			}
		})
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
				checkImageRates(t, s, own, st, n)
			}
		}
	}
}

// checkImageRates: a prompt of n tokens, 10 of them images, and 20 image output tokens cost what the price list's
// effective prices say, and so do the ceilings: the image's own price, or else its side's ordinary price in the same
// service tier and band.
func checkImageRates(t *testing.T, s catalog.Schedule, own string, st catalog.ServiceTier, n int64) {
	t.Helper()
	pi, po := int64(1), int64(2)
	if st == catalog.ServiceTierPriority {
		pi, po = 3, 4
	}
	if n > 150 {
		pi, po = pi+4, po+4
	}
	ii, io := pi, po
	switch own {
	case `,"image_in":"0","image_out":"0"`:
		ii, io = 0, 0
	case "":
	default:
		ii, io = 9, 10
	}
	cost, missing, err := s.Cost(mustTierUsage(t, st, counts{in: n - 10, imageIn: 10, imageOut: 20}))
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
