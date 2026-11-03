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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	plain := `{"input":"你好"}`
	for _, body := range []string{
		plain,
		`{"input":"\u4f60\u597d"}`,
		`{"input":"你好","input_audio":{"data":"` + strings.Repeat("YWJj", 10000) + `"}}`,
		`{"input":"你好","encrypted_content":"` + strings.Repeat("YWJj", 10000) + `"}`,
	} {
		d := billing.Demand{TextBytes: int64(len(body))}
		got, err := textTokens([]byte(body), d, billing.TokenizerO200k, 0)
		want, wantErr := textTokens([]byte(plain), d, billing.TokenizerO200k, 0)
		if err != nil || wantErr != nil || got != want || got == 0 {
			t.Fatalf("decoded/opaque estimate %d (%v), want %d (%v)", got, err, want, wantErr)
		}
	}
}

func TestInputTextEstimateByteBudget(t *testing.T) {
	t.Parallel()
	// Words, not one unbroken run: the budget is the same, and a run of one letter costs BPE about seven times as much
	// (BenchmarkInputTextEstimate), which the race detector multiplies past twenty seconds.
	prefix := strings.Repeat("English words with spaces. ", maxCountBytes/27+1)[:maxCountBytes]
	c := billing.NewTextCount(billing.TokenizerO200k, int64(len(prefix)))
	c.Add([]byte(prefix))
	want, err := c.Total()
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "x", "xyz12", strings.Repeat("x", 1<<20)} {
		body := []byte(`{"input":"` + prefix + suffix + `"}`)
		got, err := textTokens(body, billing.Demand{TextBytes: int64(len(body))}, billing.TokenizerO200k, 0)
		if expected := want + tokensOf(int64(len(suffix)), 4); err != nil || got != expected {
			t.Fatalf("suffix %d bytes: %d (%v), want %d", len(suffix), got, err, expected)
		}
	}
}

func TestAttemptReleasesRouteBeforePricing(t *testing.T) {
	t.Parallel()
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
					if _, err := textTokens(body, d, billing.TokenizerO200k, 0); err != nil {
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

// TestAttemptCountsInputTheStringsDoNotShow: a missing input report is estimated from the body's strings, which leave out
// a token-array input and the repeats of a completion's suffix. Token IDs count one each, exactly ("m" is one more), and
// a suffix read again for each later prompt adds its bytes at 4 a token: three prompts with a 4000-byte suffix estimate
// 2000 tokens more than one, plus the two "x" prompts.
func TestAttemptCountsInputTheStringsDoNotShow(t *testing.T) {
	t.Parallel()
	ids := strings.TrimSuffix(strings.Repeat("7,", 1000), ",")
	suffix := strings.Repeat("a", 4000)
	estimate := func(f protocol.Format, body string) int64 {
		t.Helper()
		req, err := protocol.Inspect(f, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		a := attempt{s: &session{c: &Call{Body: []byte(body), Request: req}, logger: slog.New(slog.DiscardHandler)}}
		a.entry.Target.Model = "gpt-4o"
		a.entry.Limits.ContextLength = 1 << 20
		return billing.InputEstimate(a.demand(), a.entry.Limits)
	}
	for _, c := range []struct {
		name string
		f    protocol.Format
		body string
		want int64
	}{
		{"a token prompt", protocol.Completions, `{"model":"m","prompt":[` + ids + `]}`, 1001},
		{"token prompts", protocol.Completions, `{"model":"m","prompt":[[` + ids + `],[7,7]]}`, 1003},
		{"token inputs", protocol.Embeddings, `{"model":"m","input":[[` + ids + `],[7]]}`, 1002},
	} {
		if got := estimate(c.f, c.body); got != c.want {
			t.Errorf("%s: input estimate %d, want %d", c.name, got, c.want)
		}
	}
	one := estimate(protocol.Completions, `{"model":"m","prompt":["x"],"suffix":"`+suffix+`"}`)
	three := estimate(protocol.Completions, `{"model":"m","prompt":["x","x","x"],"suffix":"`+suffix+`"}`)
	if three-one != 2002 {
		t.Errorf("three prompts estimate %d, one %d: want 2002 more", three, one)
	}
}
