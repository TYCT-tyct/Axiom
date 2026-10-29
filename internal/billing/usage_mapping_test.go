package billing

import (
	"fmt"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

func TestImageSaleReservationAndCostAgree(t *testing.T) {
	reports := catalog.ItemSet(1<<in | 1<<catalog.ItemImageIn | 1<<catalog.ItemOutput | 1<<catalog.ItemImageOut)
	for _, own := range []string{"", `,"image_in":"0","image_out":"0"`, `,"image_in":"9","image_out":"10"`} {
		s := soldImageSchedule(t, own)
		if missing := Unpriced(s, catalog.Reportable{Items: reports, ServiceTiers: 1 << catalog.ServiceTierPriority}); missing != (catalog.Reportable{}) {
			t.Fatalf("route rejected images: %+v", missing)
		}
		for _, tier := range []catalog.ServiceTier{catalog.ServiceTierStandard, catalog.ServiceTierPriority} {
			for _, n := range []int64{100, 151} {
				checkImageCall(t, s, reports, tier, n)
			}
		}
	}
}

// soldImageSchedule is the fixed sale price of the rule whose image prices are own, if any.
func soldImageSchedule(t *testing.T, own string) catalog.Schedule {
	t.Helper()
	body := fmt.Sprintf(`{"v":1,"currency":"USD","rates":{"input":"1","output":"2"%s},
		"context_tiers":{"basis":["input","image_in"],"method":"whole","tiers":[{"above":150,"rates":{"input":"5","output":"6"}}]},
		"service_tiers":{"priority":{"rates":{"input":"3","output":"4"%s},"context_tiers":[{"above":150,"rates":{"input":"7","output":"8"}}]}}}`, own, own)
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	var rule Rule
	rule.Method.Kind, rule.Method.Schedule = "fixed", []byte(body)
	s, err := Sell(rule, catalog.ReferencePrice{}, currency)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// checkImageCall: a call of n prompt tokens, 10 of them images, and 20 image output tokens is sold for what it costs,
// and its reservation covers that cost.
func checkImageCall(t *testing.T, s catalog.Schedule, reports catalog.ItemSet, tier catalog.ServiceTier, n int64) {
	t.Helper()
	u, err := catalog.NewUsage(map[catalog.Item]int64{in: n - 10, catalog.ItemImageIn: 10, catalog.ItemImageOut: 20}, tier, nil)
	if err != nil {
		t.Fatal(err)
	}
	cost, missing, err := s.Cost(u) // The supplier cost path calls this same resolver.
	if err != nil || missing != 0 {
		t.Fatalf("cost: %v, missing %v", err, missing)
	}
	p, err := priceSegments(s, fallbacks{}, []Segment{{Usage: u, Reported: reports}}, false, 0)
	if err != nil || p.estimated || p.price != cost.CeilAmount() {
		t.Fatalf("sale %+v, cost %v, err %v", p, cost.Rat(), err)
	}
	e := Quotable{Price: s, Model: Model{ContextLength: 1000, Reports: reports, OpenAIUsage: true}, MaxOutput: 20}
	q, err := quoteEntry(Demand{TextBytes: n, ServiceTiers: 1 << tier}, e, Segments{}, false)
	if err != nil || q.amount.Cmp(cost) < 0 {
		t.Fatalf("quote %v below cost %v: %v", q.amount.Rat(), cost.Rat(), err)
	}
}

// An OpenAI call may bill the same 100 tokens as a cache read and as a cache write, so its input is reserved at
// read + write per token; a Messages call bills the items apart, so only the dearest price bounds it, and the cache
// write the request does not ask for is not reserved at all.
func TestOverlapReservationCounterexampleAndMaximumPrices(t *testing.T) {
	for _, tc := range []struct {
		name        string
		openAI      bool
		cacheMarker bool // the request marks a cache breakpoint, so it may produce a cache write
		read, write string
		want        string
	}{
		{"openai", true, true, "0.1", "1.25", "135"},
		{"openai without a marker", true, false, "0.1", "1.25", "135"},
		{"openai, dearer read", true, true, "2", "1.25", "325"},
		{"openai, highest prices", true, true, "1000", "1000", "200000"},
		{"messages with a marker", false, true, "0.1", "1.25", "125"},
		{"messages without a marker", false, false, "0.1", "1.25", "100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := mustSchedule(t, fmt.Sprintf(`{"v":1,"currency":"USD","rates":{"input":"1","cache_read":%q,"cache_write":%q,"output":"0"}}`, tc.read, tc.write))
			d := Demand{TextBytes: 100}
			if tc.cacheMarker {
				d.InputItems = 1 << cacheWrite
			}
			m := Model{ContextLength: 100, Reports: 1<<catalog.ItemCacheRead | 1<<cacheWrite, OpenAIUsage: tc.openAI}
			q, err := quoteEntry(d, Quotable{Price: s, Model: m}, Segments{}, false)
			if err != nil || q.amount.Rat().RatString() != tc.want {
				t.Fatalf("quote %v, %v; want %s", q.amount.Rat(), err, tc.want)
			}
		})
	}
}

// OpenAI reports image tokens whether or not the model's spec lists the item, so they are reserved at their own price
// even when Reports names only input and output: 100 prompt tokens that are all images settle at 100 × 10.
func TestImageInputIsReservedWithoutReportsAndFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prices string
		openAI bool
		want   string
	}{
		{"image priced above input", `"input":"1","image_in":"10"`, true, "1000"},
		{"image priced at zero", `"input":"1","image_in":"0"`, true, "100"},
		{"image unpriced falls back to input", `"input":"3"`, true, "300"},
		{"messages reports no images", `"input":"1","image_in":"10"`, false, "100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := mustSchedule(t, `{"v":1,"currency":"USD","rates":{`+tc.prices+`,"output":"0"}}`)
			m := Model{ContextLength: 100, Reports: 1<<in | 1<<catalog.ItemOutput, OpenAIUsage: tc.openAI}
			q, err := quoteEntry(Demand{TextBytes: 100}, Quotable{Price: s, Model: m}, Segments{}, false)
			if err != nil || q.amount.Rat().RatString() != tc.want {
				t.Fatalf("quote %v, %v; want %s", q.amount.Rat(), err, tc.want)
			}
			u := usage(t, map[catalog.Item]int64{catalog.ItemImageIn: 100})
			if cost, _, err := s.Cost(u); tc.openAI && (err != nil || cost.Cmp(q.amount) > 0) {
				t.Fatalf("settlement %v exceeds the quote %v: %v", cost.Rat(), q.amount.Rat(), err)
			}
		})
	}
}

func TestExtraSearchCoversOverlappingRereads(t *testing.T) {
	e := entry(t, true, 1)
	e.Price = mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","cache_read":"0.1","cache_write":"1.25","output":"1","web_search":"2"}}`)
	e.Model.Reports, e.Model.OpenAIUsage = 1<<catalog.ItemCacheRead|1<<cacheWrite, true
	d := Demand{TextBytes: 100, Compaction: true, ServerTools: []ServerTool{{Item: webSearch, MaxUses: 1}}}
	m := meter(t, d, e)
	// Each extra search costs 2, plus two rereads of a 1000-token context at 0.1+1.25.
	if got := m.searchCost(2).Rat().RatString(); got != "5404" {
		t.Fatalf("extra searches cost %s, want 5404", got)
	}
}

func TestImageOutputIsDeliveredAndMetered(t *testing.T) {
	u := usage(t, map[catalog.Item]int64{catalog.ItemImageOut: 20})
	seg := Segment{Usage: u, Reported: 1<<catalog.ItemOutput | 1<<catalog.ItemImageOut}
	s := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"output":"1","image_out":"10"}}`)
	for _, finish := range []FinishReason{FinishStop, FinishError, FinishCancelled} {
		for _, result := range []Result{Committed, Aggregated} {
			at := AttemptResult{Result: result, Finish: finish, Segments: []Segment{seg}}
			if waivable(at) {
				t.Fatalf("image-only reply waived: %+v", at)
			}
			p, err := priceSegments(s, fallbacks{}, at.Segments, waivable(at), 0)
			if err != nil || p.price != 200_000_000_000 || p.estimated {
				t.Fatalf("price: %+v, %v", p, err)
			}
		}
	}
	m := meter(t, Demand{}, entry(t, true, 0))
	out, _ := m.used(Progress{Reported: u, Final: 1<<catalog.ItemOutput | 1<<catalog.ItemImageOut})
	if out != 20 {
		t.Fatalf("meter counted %d, want 20 image tokens", out)
	}
}

func TestPhysicalInputSurvivesStoredUsageAndWaiverPricing(t *testing.T) {
	u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemCacheRead: 80, cacheWrite: 80, webSearch: 1}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if u, err = u.WithPhysicalInput(100); err != nil {
		t.Fatal(err)
	}
	native := catalog.NativeTokens{VideoInput: 10, AcceptedPrediction: 2, RejectedPrediction: 1}
	if u, err = u.WithNativeTokens(native); err != nil {
		t.Fatal(err)
	}
	seg := Segment{Usage: u}
	s := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"cache_read":"1","cache_write":"1","web_search":"1"},"context_tiers":{"basis":["input","cache_read","cache_write"],"method":"whole","tiers":[{"above":150,"rates":{"web_search":"10","cache_read":"2"}}]}}`)
	b, err := encodeUsage([]Segment{seg})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeUsage(b)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := decoded[0].Usage.PhysicalInput(); !ok || n != 100 {
		t.Fatalf("lost physical input: %+v", decoded)
	}
	if got := decoded[0].Usage.NativeTokens(); got != native {
		t.Fatalf("native tokens = %+v after storing, want %+v", got, native)
	}
	cost, _, err := segmentCost(s, decoded[0].Usage, true)
	if err != nil || cost.Rat().RatString() != "1" {
		t.Fatalf("waived per-use cost = %v, %v", cost.Rat(), err)
	}
}

func TestStoredPhysicalInputPreservesPresence(t *testing.T) {
	for _, n := range []int64{-1, 0, 100} {
		u := usage(t, nil)
		if n >= 0 {
			var err error
			u, err = u.WithPhysicalInput(n)
			if err != nil {
				t.Fatal(err)
			}
		}
		encoded, err := encodeUsage([]Segment{{Usage: u}})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeUsage(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if total, ok := decoded[0].Usage.PhysicalInput(); ok != (n >= 0) || ok && total != n {
			t.Fatalf("physical input %d/%t after storing %d", total, ok, n)
		}
	}
}
