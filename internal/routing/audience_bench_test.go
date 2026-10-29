package routing

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// Compare the same fifty-candidate ranking workload. Admission computes price derivatives once per model and
// reuses a preallocated view: an optimistic allocation lower bound. Snapshot computes the derivatives at build.
func audienceBenchmark(b *testing.B) (*Router, []*candidate, *need, *billing.AudiencePrices) {
	r, err := New(Config{Rand: func() float64 { return 0.5 }})
	if err != nil {
		b.Fatal(err)
	}
	schedule, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_read":"0.0000001"}}`))
	if err != nil {
		b.Fatal(err)
	}
	tier := accounts.TierID(uuid.NewV7())
	prices := []billing.AudiencePrice{
		{Target: billing.Selector{Kind: "all"}, Sold: billing.Sold{Version: uuid.NewV7(), Schedule: schedule}},
		{Audience: billing.Audience{Tier: tier}, Target: billing.Selector{Kind: "all"}, Sold: billing.Sold{Version: uuid.NewV7(), Schedule: schedule}},
	}
	compiled := billing.CompilePrices(prices)
	priceCache := map[*billing.AudiencePrices][]candidatePrice{}
	var candidates []*candidate
	for i := range 50 {
		c, err := r.candidate(Endpoint{ID: EndpointID(uuid.NewV7()), Supplier: SupplierID(uuid.NewV7()), Enabled: true, BalanceOK: true, Egresses: 1, Class: ClassOfficial, Policy: PolicyNoRetention, Health: 1, Quality: 1, Confidence: 1, Context: 128000, ServiceTiers: 1 << catalog.ServiceTierStandard, Native: Of(protocol.Chat), Served: Of(protocol.Chat), AudiencePrices: compiled, Streams: true, MaxOutput: 100, Throughput: 100, BreakerOpen: i%7 == 0})
		if err != nil {
			b.Fatal(err)
		}
		c.prices, err = compileAudiencePrices(compiled, priceCache)
		if err != nil {
			b.Fatal(err)
		}
		candidates = append(candidates, c)
	}
	in := Input{Admission: Admission{Tier: tier}, Req: &protocol.Request{Format: protocol.Chat, ServiceTiers: 1 << catalog.ServiceTierStandard, Choices: 1}}
	p := Preset{Classes: AllClasses, Policies: AllPolicies}
	n := &need{in: &in, req: in.Req, preset: &p, over: &in.Override, now: time.Now()}
	return r, candidates, n, compiled
}

func BenchmarkAudiencePlan(b *testing.B) {
	r, candidates, n, compiled := audienceBenchmark(b)
	for _, mode := range []string{"snapshot", "admission"} {
		b.Run(mode, func(b *testing.B) {
			benchmarkAudienceMode(b, mode, r, candidates, n, compiled)
		})
	}
}

func benchmarkAudienceMode(b *testing.B, mode string, r *Router, candidates []*candidate, n *need, compiled *billing.AudiencePrices) {
	b.ReportAllocs()
	for b.Loop() {
		if mode == "admission" {
			price := billing.Sold{Audiences: compiled}.For(billing.Audience{Tier: n.in.Admission.Tier})
			derived, err := priceOf(PriceRef{Version: price.Version, Schedule: price.Schedule})
			if err != nil {
				b.Fatal(err)
			}
			// This view belongs to this single-threaded benchmark, not a shared production snapshot.
			at := compiled.Select(billing.Audience{Tier: n.in.Admission.Tier})
			candidates[0].prices[at] = derived
		}
		var rec Record
		ranked := r.rank(candidates, n, &rec)
		if len(ranked) != len(candidates)-(len(candidates)+6)/7 {
			b.Fatal(len(ranked))
		}
	}
}
