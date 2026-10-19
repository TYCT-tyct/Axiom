package billing

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
)

func TestImageSaleReservationAndCostAgree(t *testing.T) {
	for _, own := range []string{"", `,"image_in":"0","image_out":"0"`, `,"image_in":"9","image_out":"10"`} {
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
		reports := catalog.ItemSet(1<<in | 1<<catalog.ItemImageIn | 1<<catalog.ItemOutput | 1<<catalog.ItemImageOut)
		if missing := Unpriced(s, catalog.Reportable{Items: reports, ServiceTiers: 1 << catalog.ServiceTierPriority}); missing != (catalog.Reportable{}) {
			t.Fatalf("route rejected images: %+v", missing)
		}
		for _, tier := range []catalog.ServiceTier{catalog.ServiceTierStandard, catalog.ServiceTierPriority} {
			for _, n := range []int64{100, 151} {
				u, err := catalog.NewUsage(map[catalog.Item]int64{in: n - 10, catalog.ItemImageIn: 10, catalog.ItemImageOut: 20}, tier, nil)
				if err != nil {
					t.Fatal(err)
				}
				cost, missing, err := s.Cost(u) // The supplier cost path calls this same resolver.
				if err != nil || missing != 0 {
					t.Fatalf("cost: %v, missing %v", err, missing)
				}
				seg := Segment{Usage: u, Reported: reports}
				p, err := priceSegments(s, fallbacks{}, []Segment{seg}, false)
				if err != nil || p.estimated || p.price != cost.CeilAmount() {
					t.Fatalf("sale %+v, cost %v, err %v", p, cost.Rat(), err)
				}
				e := Quotable{Price: s, Model: Model{ContextLength: 1000, Reports: reports}, MaxOutput: 20}
				q, err := quoteEntry(Demand{TextBytes: n, ServiceTiers: 1 << tier}, e, Segments{})
				if err != nil || q.amount.Cmp(cost) < 0 {
					t.Fatalf("quote %v below cost %v: %v", q.amount.Rat(), cost.Rat(), err)
				}
			}
		}
	}
}

// Prices, tiers, modality splits and repeated samplings vary independently. Parse through protocol before pricing,
// so the property quantifies over accepted reports rather than a second implementation of the mapping.
func FuzzAcceptedOpenAIUsageFitsQuote(f *testing.F) {
	for seed := range uint64(4) {
		f.Add(seed, ^seed)
	}
	f.Fuzz(func(t *testing.T, a, b uint64) {
		r := rand.New(rand.NewPCG(a, b)) //nolint:gosec // test data
		for range 30 {
			c := randomQuoteCase(t, r, 4000)
			c.d.InputItems.Add(cacheWrite)
			c.e.Model.Reports = 1<<catalog.ItemCacheRead | 1<<cacheWrite | 1<<catalog.ItemImageIn | 1<<catalog.ItemImageOut
			q, err := quoteEntry(c.d, c.e, c.s)
			if err != nil {
				t.Fatal(err)
			}
			_, samplings := serverToolUses(c.d, c.e.Model)
			first, _ := inputBound(c.d, c.e.Model, samplings)
			output := min(q.outputCap, c.s.Output)
			if !c.e.Segmentable {
				output = q.outputCap
			}
			var segments []Segment
			for j := range samplings {
				n := c.e.Model.ContextLength
				if j == 0 {
					n = first
				}
				n = r.Int64N(n + 1)
				read, write := r.Int64N(n+1), r.Int64N(n+1)
				out := r.Int64N(output + 1)
				output -= out
				body := fmt.Sprintf(`{"input_tokens":%d,"input_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d,"image_tokens":%d},"output_tokens":%d,"output_tokens_details":{"image_tokens":%d}}`, n, read, write, r.Int64N(n+1), out, r.Int64N(out+1))
				tier := catalog.ServiceTierStandard
				if c.servedAt().Has(catalog.ServiceTierPriority) && r.IntN(2) == 0 {
					tier = catalog.ServiceTierPriority
				}
				segments = append(segments, acceptedOpenAI(t, body, tier, j%2 == 0))
			}
			p, err := priceSegments(c.e.Price, fallbacks{}, segments, false)
			if err != nil || p.estimated || p.price > q.amount.CeilAmount() {
				t.Fatalf("settlement %+v exceeds quote %v: %v; %s", p, q.amount.Rat(), err, c.json)
			}
			var exact platform.Exact
			for _, seg := range segments {
				cost, missing, err := c.e.Price.Cost(seg.Usage)
				if err != nil || missing != 0 {
					t.Fatalf("cost: %v, missing %v", err, missing)
				}
				exact = sum(exact, cost)
			}
			if exact.Cmp(q.amount) > 0 {
				t.Fatalf("exact cost %v exceeds quote %v", exact.Rat(), q.amount.Rat())
			}
		}
	})
}

func acceptedOpenAI(t testing.TB, body string, tier catalog.ServiceTier, chat bool) Segment {
	t.Helper()
	f := protocol.Responses
	stream := `data: {"type":"response.completed","sequence_number":0,"response":{"id":"r","model":"m","output":[],"usage":` + body + "}}\n\n"
	if chat {
		f = protocol.Chat
		stream = `data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"usage":` + body + "}\n\ndata: [DONE]\n\n"
	}
	relay := protocol.NewRelay(f, f, strings.NewReader(stream), protocol.UsageContext{Tier: tier}, protocol.RelayOptions{Memory: protocol.NewMemory(0)})
	defer relay.Release()
	for {
		step, err := relay.Next()
		if err != nil {
			t.Fatal(err)
		}
		if step.Kind == protocol.StepUpstreamDone {
			break
		}
	}
	u := relay.Usage()
	if u.Invalid != nil || len(u.Segments) != 1 {
		t.Fatalf("invalid usage: %+v", u)
	}
	return Segment{Usage: u.Segments[0].Usage, Reported: u.Segments[0].Reported}
}

func TestOverlapReservationCounterexampleAndMaximumPrices(t *testing.T) {
	for _, tc := range []struct{ read, write, want string }{{"0.1", "1.25", "135"}, {"2", "1.25", "325"}, {"1000", "1000", "200000"}} {
		s := mustSchedule(t, fmt.Sprintf(`{"v":1,"currency":"USD","rates":{"input":"1","cache_read":%q,"cache_write":%q,"output":"0"}}`, tc.read, tc.write))
		q, err := quoteEntry(Demand{TextBytes: 100, InputItems: 1 << cacheWrite}, Quotable{Price: s, Model: Model{ContextLength: 100, Reports: 1 << catalog.ItemCacheRead}}, Segments{})
		if err != nil || q.amount.Rat().RatString() != tc.want {
			t.Fatalf("quote %v, %v; want %s", q.amount.Rat(), err, tc.want)
		}
	}
}

func TestExtraSearchCoversOverlappingRereads(t *testing.T) {
	e := entry(t, true, 1)
	e.Price = mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","cache_read":"0.1","cache_write":"1.25","output":"1","web_search":"2"}}`)
	e.Model.Reports = 1<<catalog.ItemCacheRead | 1<<cacheWrite
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
			p, err := priceSegments(s, fallbacks{}, at.Segments, waivable(at))
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
	seg := acceptedOpenAI(t, `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80},"output_tokens":0}`, catalog.ServiceTierStandard, true)
	counts := map[catalog.Item]int64{catalog.ItemCacheRead: 80, cacheWrite: 80, webSearch: 1}
	var err error
	seg.Usage, err = seg.Usage.Recount(counts, nil)
	if err != nil {
		t.Fatal(err)
	}
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
