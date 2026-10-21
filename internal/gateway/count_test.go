package gateway

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

func TestAttemptCountsOncePerFamily(t *testing.T) {
	body := []byte(`{"model":"m","input":"` + strings.Repeat("今天天气很好。", 1000) + `"}`)
	s := session{
		c:      &Call{Body: body, Request: protocol.Request{TextBytes: int64(len(body))}},
		logger: slog.New(slog.DiscardHandler),
	}
	a := attempt{s: &s}
	a.entry.Target.Model = "gpt-4o"
	a.entry.Limits.ContextLength = 100
	d := a.demand()
	if d.TextTokens <= 100 {
		t.Fatalf("count %d was clipped to the first route's context", d.TextTokens)
	}
	if n := billing.InputEstimate(d, a.entry.Limits); n != 100 {
		t.Fatalf("first route input %d, want its context of 100", n)
	}
	// A later attempt must not reread the body, even when its model/context differ.
	s.c.Body = []byte(`{"input":`)
	a.entry.Target.Model = "gpt-5"
	a.entry.Limits.ContextLength = 100000
	if got := a.demand(); got.TextTokens != d.TextTokens || billing.InputEstimate(got, a.entry.Limits) != d.TextTokens {
		t.Fatalf("same family lost the full cached count: %+v", got)
	}
	// Another family counts independently; failures are cached too, avoiding retry storms.
	a.entry.Target.Model = "gpt-4"
	if got := a.demand(); got.TextTokens != 0 {
		t.Fatalf("failed extraction retained %d tokens", got.TextTokens)
	}
	s.c.Body = body
	if got := a.demand(); got.TextTokens != 0 {
		t.Fatalf("failed family was counted again: %d", got.TextTokens)
	}
}

func TestAttemptSkipsCountForReportedInput(t *testing.T) {
	for _, item := range []catalog.Item{catalog.ItemInput, catalog.ItemCacheRead, catalog.ItemCacheWrite} {
		s := session{c: &Call{Body: []byte(`invalid`), Request: protocol.Request{TextBytes: 100}}}
		a := attempt{s: &s, usage: protocol.Usage{Segments: []protocol.Segment{{Reported: 1 << item}}}}
		a.entry.Target.Model = "gpt-4o"
		if d := a.demand(); d.TextTokens != 0 || s.textCounts[0].counted {
			t.Fatalf("%s: counted a reported input", item)
		}
	}
}

func TestInputTextEstimateDecodedAndOpaque(t *testing.T) {
	plain := `{"input":"你好"}`
	for _, body := range []string{
		plain,
		`{"input":"\u4f60\u597d"}`,
		`{"input":"你好","input_audio":{"data":"` + strings.Repeat("YWJj", 10000) + `"}}`,
		`{"input":"你好","encrypted_content":"` + strings.Repeat("YWJj", 10000) + `"}`,
	} {
		d := billing.Demand{TextBytes: int64(len(body))}
		got, err := textTokens([]byte(body), d, billing.TokenizerO200k)
		want, wantErr := textTokens([]byte(plain), d, billing.TokenizerO200k)
		if err != nil || wantErr != nil || got != want || got == 0 {
			t.Fatalf("decoded/opaque estimate %d (%v), want %d (%v)", got, err, want, wantErr)
		}
	}
}

func TestInputTextEstimateByteBudget(t *testing.T) {
	prefix := strings.Repeat("a", maxCountBytes)
	c := billing.NewTextCount(billing.TokenizerO200k, int64(len(prefix)))
	c.Add([]byte(prefix))
	want, err := c.Total()
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "x", "xyz12", strings.Repeat("x", 1<<20)} {
		body := []byte(`{"input":"` + prefix + suffix + `"}`)
		got, err := textTokens(body, billing.Demand{TextBytes: int64(len(body))}, billing.TokenizerO200k)
		if expected := want + tokensOf(int64(len(suffix)), 4); err != nil || got != expected {
			t.Fatalf("suffix %d bytes: %d (%v), want %d", len(suffix), got, err, expected)
		}
	}
}

func TestAttemptReleasesRouteBeforePricing(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		entry := h.call.Plan.Entries[0]
		model := catalog.ModelID{1}
		formats := routing.Of(protocol.Messages)
		snap, err := h.router.NewSnapshot(time.Now(), []routing.Endpoint{{
			ID: entry.Endpoint, Supplier: entry.Supplier, Model: model, Enabled: true, BalanceOK: true, Egresses: 1,
			Native: formats, Served: formats, Caps: ^protocol.Capabilities(0), Streams: true, Cancels: true,
			Context: 1000000, MaxOutput: 1000, Price: entry.Price, MaxConcurrent: 1,
			ServiceTiers: 1 << catalog.ServiceTierStandard,
		}}, []routing.Preset{{Classes: routing.AllClasses, Policies: routing.AllPolicies}})
		if err != nil {
			t.Fatal(err)
		}
		h.router.Replace(snap)
		in := routing.Input{Req: &h.call.Request, Models: []catalog.ModelID{model}}
		plan, wait, err := h.router.Plan(in)
		if err != nil || wait != nil || len(plan.Entries) != 1 {
			t.Fatalf("initial plan: %v %v %+v", err, wait, plan)
		}
		observed := false
		h.exec.Costs = observingCosts{Costs: h.exec.Costs, observe: func() {
			observed = true
			plan, wait, err := h.router.Plan(in)
			if err != nil || wait != nil || len(plan.Entries) != 1 {
				t.Errorf("route still occupied while pricing: %v %v %+v", err, wait, plan)
			}
		}}
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		if !observed {
			t.Fatal("attempt was not priced")
		}
	})
}

type observingCosts struct {
	Costs
	observe func()
}

func (c observingCosts) Credits(r suppliers.AttemptReceipt, u []catalog.Usage, currency platform.Currency) (suppliers.AttemptCost, int64, bool, error) {
	c.observe()
	return c.Costs.Credits(r, u, currency)
}

func BenchmarkInputTextEstimate(b *testing.B) {
	billing.WarmTokenizers()
	for _, size := range []int{1024, maxCountBytes, 1 << 20} {
		for _, corpus := range []struct{ name, text string }{
			{"english", "English words with spaces. "},
			{"unbroken", "a"},
		} {
			text := strings.Repeat(corpus.text, size/len(corpus.text)+1)[:size]
			body := []byte(`{"input":"` + text + `"}`)
			d := billing.Demand{TextBytes: int64(len(body))}
			b.Run(corpus.name+"/"+strconv.Itoa(size/1024)+"KiB", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(size))
				for b.Loop() {
					if _, err := textTokens(body, d, billing.TokenizerO200k); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(size/1024), "ns/KiB")
			})
		}
	}
	b.Run("cached", func(b *testing.B) {
		s := session{c: &Call{Body: []byte(`{"input":"hi"}`), Request: protocol.Request{TextBytes: 14}}}
		a := attempt{s: &s}
		a.entry.Target.Model = "gpt-4o"
		a.demand()
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			a.demand()
		}
	})
}
