package billing

import (
	"flag"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/textshape"
)

// samples adds the samples scripts/usage_samples.py writes from Claude Code's session logs to the committed corpus
// (go test -run TestEstimateError -samples <file> -v). They hold the usage Anthropic reported for each reply, and their
// content is the user's, so they are read where they lie and never copied into testdata.
var samples = flag.String("samples", "", "a file of usage samples to measure the estimate's error on, besides testdata")

// sample is a text and the tokens the upstream reported for it.
type sample struct {
	Model  string `json:"model"`
	Side   string `json:"side"` // input or output
	Kind   string `json:"kind"` // text, or tool for a reply that called tools, whose framing its text leaves out
	Text   string `json:"text"`
	Tokens int64  `json:"tokens"`
}

// TestEstimateError measures the estimates against reported usage, beside the byte rule they replace: Claude's from
// the shape of the text, and o200k's estimate of a reply from its shape against the vocabulary's own count of it. A
// reply's shape is the same whole or streamed one token a delta (streamedShape), so its error is a stream's too. The
// committed corpus (testdata/usage-samples.jsonl: replies of Claude Opus 5.5 and Sonnet 5.5 written in this project,
// in English, Chinese and tool calls) holds the median of a reply's text to ±15%; docs/domains/billing.md「用量来源与
// 暂估」 has the distribution over the session logs as well.
func TestEstimateError(t *testing.T) {
	t.Parallel()
	corpus := readSamples(t, "testdata/usage-samples.jsonl")
	if *samples != "" {
		corpus = append(corpus, readSamples(t, *samples)...)
	}
	groups, bill := map[string]*estimateErrors{}, estimateErrors{}
	add := func(key string, est, bytes, actual int64) {
		if groups[key] == nil {
			groups[key] = &estimateErrors{}
		}
		groups[key].add(est, bytes, actual)
	}
	for _, s := range corpus {
		tok := TokenizerOf(s.Model)
		est, bytes := estimate(t, tok, s)
		add(family(tok)+"/"+s.Side+"/"+s.Kind+"/"+language(s.Text), est, bytes, s.Tokens)
		price := int64(1) // Anthropic lists the output at five times the input
		if s.Side == "output" {
			price = 5
			var shape textshape.Shape
			shape.Add([]byte(s.Text))
			if streamed := streamedShape(t, s.Text); streamed != shape {
				t.Fatalf("%q: tallied as it streams %+v, whole %+v", s.Text, streamed, shape)
			}
			n, _ := TextTokens(TokenizerO200k, shape, shape.Bytes)
			add("o200k/output/vocabulary/"+language(s.Text), n, ceilDiv(shape.Bytes, 3), wholeCount(t, TokenizerO200k, s.Text))
		}
		bill.add(price*est, price*bytes, price*s.Tokens)
	}
	for _, key := range slices.Sorted(maps.Keys(groups)) {
		g := groups[key]
		t.Logf("%-32s n=%4d median %+6.1f%% p5 %+6.1f%% p95 %+6.1f%% sum %+6.1f%% | byte rule: median %+6.1f%% sum %+6.1f%%",
			key, len(g.est), 100*quantile(g.est, 0.5), 100*quantile(g.est, 0.05), 100*quantile(g.est, 0.95), 100*g.sum(g.sumEst),
			100*quantile(g.bytes, 0.5), 100*g.sum(g.sumBytes))
		if median := quantile(g.est, 0.5); *samples == "" && strings.Contains(key, "/output/text/") && math.Abs(median) > 0.15 {
			t.Errorf("%s: the median error %+.1f%% of the committed samples left ±15%%", key, 100*median)
		}
	}
	t.Logf("Claude's bill: estimate %+.1f%%, byte rule %+.1f%%", 100*bill.sum(bill.sumEst), 100*bill.sum(bill.sumBytes))
}

// streamedShape is a reply's tally as the relay makes it: each event tallied on its own and merged, the events cut
// where OpenAI's streams cut them, one o200k token a delta, joined until a delta ends on a whole character as a JSON
// string must. Equal to the whole text's, it makes the error measured on whole texts the error of a streamed reply.
func streamedShape(t *testing.T, text string) textshape.Shape {
	t.Helper()
	_, tokens, err := o200k().Encode(text)
	if err != nil {
		t.Fatal(err)
	}
	var shape textshape.Shape
	delta := ""
	for _, tok := range tokens {
		if delta += tok; utf8.ValidString(delta) {
			var one textshape.Shape
			one.Add([]byte(delta))
			shape.Merge(one)
			delta = ""
		}
	}
	if delta != "" {
		t.Fatalf("%q: the tokens end inside a character", text)
	}
	return shape
}

// estimate is a sample's estimate as a settlement makes it, a request's text by its count and a reply's by its shape,
// and the byte rule's: gateway's outputBytesPerToken for a reply, InputBytesPerToken for a request.
func estimate(t *testing.T, tok Tokenizer, s sample) (est, bytes int64) {
	t.Helper()
	if s.Tokens <= 0 {
		t.Fatalf("%s %s: no reported usage", s.Model, s.Side)
	}
	if s.Side == "input" {
		c := NewTextCount(tok, math.MaxInt64)
		if c == nil {
			t.Fatalf("%s: no count", s.Model)
		}
		c.Add([]byte(s.Text))
		return countTotal(t, c), ceilDiv(int64(len(s.Text)), InputBytesPerToken)
	}
	var shape textshape.Shape
	shape.Add([]byte(s.Text))
	n, ok := TextTokens(tok, shape, shape.Bytes)
	if !ok {
		t.Fatalf("%s: no estimate", s.Model)
	}
	return n, ceilDiv(shape.Bytes, 3)
}

// estimateErrors are a group's errors, (estimate − actual) / actual, by the estimate and by the byte rule, and the
// sums its bill deviates by.
type estimateErrors struct {
	est, bytes                  []float64
	sumEst, sumBytes, sumActual int64
}

func (e *estimateErrors) add(est, bytes, actual int64) {
	e.est, e.bytes = append(e.est, errorOf(est, actual)), append(e.bytes, errorOf(bytes, actual))
	e.sumEst, e.sumBytes, e.sumActual = e.sumEst+est, e.sumBytes+bytes, e.sumActual+actual
}

func (e *estimateErrors) sum(est int64) float64 { return errorOf(est, e.sumActual) }

func errorOf(est, actual int64) float64 { return float64(est-actual) / float64(actual) }

func quantile(e []float64, q float64) float64 {
	s := slices.Sorted(slices.Values(e))
	return s[min(len(s)-1, int(q*float64(len(s))))]
}

func family(t Tokenizer) string {
	return [NumTokenizers]string{"none", "o200k", "cl100k", "claude", "claude4.7"}[t]
}

// language sorts a text by what most of it is: Chinese, or else code where it is a tool's input, or English.
func language(text string) string {
	cjk, all := 0, 0
	for _, r := range text {
		all++
		if r >= 0x4e00 && r <= 0x9fff {
			cjk++
		}
	}
	switch {
	case cjk*10 > all:
		return "zh"
	case strings.HasPrefix(text, "{"):
		return "code"
	}
	return "en"
}

func readSamples(t *testing.T, name string) []sample {
	t.Helper()
	data, err := os.ReadFile(name) //nolint:gosec // the test's own corpus, or the file its caller names
	if err != nil {
		t.Fatal(err)
	}
	var out []sample
	for line := range strings.Lines(string(data)) {
		var s sample
		if err := jsonx.Unmarshal([]byte(line), &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}
