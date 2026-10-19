package gateway

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
)

// Whatever protocol accepts of an OpenAI usage report, priced by the schedule, stays within the reservation the same
// entry is quoted at. It parses through protocol so that the property quantifies over accepted reports rather than a
// second copy of the mapping, which is why it lives here: billing may not import protocol.
func FuzzAcceptedOpenAIUsageFitsQuote(f *testing.F) {
	for seed := range uint64(4) {
		f.Add(seed, ^seed)
	}
	f.Fuzz(func(t *testing.T, a, b uint64) {
		r := rand.New(rand.NewPCG(a, b)) //nolint:gosec // test data
		for range 30 {
			price := randomOpenAIPrices(r)
			s, err := catalog.ParseSchedule([]byte(price))
			if err != nil {
				t.Fatalf("%v: %s", err, price)
			}
			window := r.Int64N(4000) + 1
			d := billing.Demand{TextBytes: r.Int64N(window + 1), Compaction: r.IntN(2) == 0}
			d.InputItems.Add(catalog.ItemCacheWrite)
			priority := strings.Contains(price, "priority") && r.IntN(2) == 0 // a request may be served at a tier the schedule prices
			if priority {
				d.ServiceTiers.Add(catalog.ServiceTierPriority)
			}
			m := billing.Model{ContextLength: window, OpenAIUsage: true,
				Reports: 1<<catalog.ItemCacheRead | 1<<catalog.ItemCacheWrite | 1<<catalog.ItemImageIn | 1<<catalog.ItemImageOut}
			e := billing.Quotable{Price: s, Model: m, MaxOutput: r.Int64N(window) + 1}
			quote, err := billing.Quote(d, []billing.Quotable{e}, billing.Segments{}, nil)
			if err != nil {
				t.Fatalf("%v: %s", err, price)
			}
			first, _ := billing.InputBound(d, m)
			samplings := int64(1)
			if d.Compaction {
				samplings = 2
			}
			output := e.MaxOutput
			var exact platform.Exact
			for j := range samplings {
				n := window
				if j == 0 {
					n = first
				}
				n = r.Int64N(n + 1)
				out := r.Int64N(output + 1)
				output -= out
				body := fmt.Sprintf(`{"input_tokens":%d,"input_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d,"image_tokens":%d},`+
					`"output_tokens":%d,"output_tokens_details":{"image_tokens":%d}}`,
					n, r.Int64N(n+1), r.Int64N(n+1), r.Int64N(n+1), out, r.Int64N(out+1))
				tier := catalog.ServiceTierStandard
				if priority && r.IntN(2) == 0 {
					tier = catalog.ServiceTierPriority
				}
				u := acceptedOpenAI(t, body, tier, j%2 == 0)
				cost, missing, err := s.Cost(u)
				if err != nil || missing != 0 {
					t.Fatalf("cost of %s: missing %v, %v", body, missing, err)
				}
				if exact, err = exact.Add(cost); err != nil {
					t.Fatal(err)
				}
			}
			if exact.Cmp(quote.Amount) > 0 {
				t.Fatalf("exact cost %v exceeds the quote %v under %s", exact.Rat(), quote.Amount.Rat(), price)
			}
		}
	})
}

// randomOpenAIPrices is a schedule with a context tier over a random basis, each tier overriding random prices, and
// sometimes a priority tier. Images are priced only sometimes, as a schedule may leave them to the fallback. Output is
// free half of the time, so that the reserved output cannot make up for an input that is reserved short.
func randomOpenAIPrices(r *rand.Rand) string {
	freeOutput := r.IntN(2) == 0
	rates := func(base bool) string {
		var parts []string
		for _, it := range []string{"input", "cache_read", "cache_write", "output", "image_in", "image_out"} {
			required := base && !strings.HasPrefix(it, "image")
			if required || r.IntN(2) == 0 {
				price := fmt.Sprintf("%d.%06d", r.IntN(50), r.IntN(1_000_000))
				if freeOutput && (it == "output" || it == "image_out") {
					price = "0"
				}
				parts = append(parts, fmt.Sprintf("%q:%q", it, price))
			}
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	tiers := func() string { // the same thresholds for every service tier, as ParseSchedule requires
		return fmt.Sprintf(`[{"above":100,"rates":%s},{"above":2000,"rates":%s}]`, rates(false), rates(false))
	}
	basis := [...]string{`"input"`, `"input","cache_read","cache_write"`, `"input","image_in"`}[r.IntN(3)]
	s := fmt.Sprintf(`{"v":1,"currency":"USD","rates":%s,"context_tiers":{"basis":[%s],"method":"whole","tiers":%s}`, rates(true), basis, tiers())
	if r.IntN(2) == 0 {
		s += fmt.Sprintf(`,"service_tiers":{"priority":{"rates":%s,"context_tiers":%s}}`, rates(true), tiers())
	}
	return s + "}"
}

// acceptedOpenAI is the usage protocol makes of an OpenAI usage object, delivered as the last event of a Chat or a
// Responses stream.
func acceptedOpenAI(t testing.TB, body string, tier catalog.ServiceTier, chat bool) catalog.Usage {
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
	return u.Segments[0].Usage
}
