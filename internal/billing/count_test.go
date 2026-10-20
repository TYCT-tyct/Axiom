package billing

import (
	"math"
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

// TestTokenizerOf: the tokenizer a model's text is counted in is read from the name its endpoint calls it by, with a
// relay's prefix and a fine-tune's name around it; a family that publishes no tokenizer, and a name of no family, keep
// the bytes-per-token rule.
func TestTokenizerOf(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		want Tokenizer
	}{
		{"gpt-4o", TokenizerO200k},
		{"gpt-4o-2024-08-06", TokenizerO200k},
		{"openai/gpt-4o-mini", TokenizerO200k},
		{"openai/gpt-4o:free", TokenizerO200k}, // a relay's variant of the model
		{"gpt-4.1-nano", TokenizerO200k},
		{"gpt-4.5-preview", TokenizerO200k},
		{"gpt-5.1", TokenizerO200k},
		{"o1-mini", TokenizerO200k},
		{"o3", TokenizerO200k},
		{"chatgpt-4o-latest", TokenizerO200k},
		{"ft:gpt-4o-2024-08-06:acme::abc", TokenizerO200k},
		{"GPT-4O", TokenizerO200k},
		{"gpt-4-turbo", TokenizerCl100k},
		{"gpt-4", TokenizerCl100k},
		{"gpt-3.5-turbo-0125", TokenizerCl100k},
		{"gpt-35-turbo", TokenizerCl100k}, // Azure's name for gpt-3.5-turbo
		{"gpt-50", TokenizerNone},         // no version and no variant follows: another model
		{"gpt-4x", TokenizerNone},
		{"o5", TokenizerNone},
		{"claude-sonnet-4-5", TokenizerNone},
		{"anthropic/claude-opus-4-1", TokenizerNone},
		{"gemini-2.5-pro", TokenizerNone},
		{"deepseek-chat", TokenizerNone},
		{"", TokenizerNone},
	} {
		if got := TokenizerOf(tc.name); got != tc.want {
			t.Errorf("TokenizerOf(%q) is %d, want %d", tc.name, got, tc.want)
		}
	}
}

// wholeCount is the library's own count of a whole text, which the pieces of a count must agree with.
func wholeCount(t *testing.T, tok Tokenizer, text string) int64 {
	t.Helper()
	var encoding tokenizer.Encoding
	switch tok {
	case TokenizerO200k:
		encoding = tokenizer.O200kBase
	case TokenizerCl100k:
		encoding = tokenizer.Cl100kBase
	case TokenizerNone:
		t.Fatal("a family without a tokenizer has no reference count")
	}
	codec, err := tokenizer.Get(encoding)
	if err != nil {
		t.Fatal(err)
	}
	n, err := codec.Count(text)
	if err != nil {
		t.Fatal(err)
	}
	return int64(n)
}

// TestTextCount: a count is the family's own tokenizer's count of the whole text, cut where maxRunBytes cuts it: equal
// while the text fits one run, and within the cut's error above it, whatever the text holds.
func TestTextCount(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		text  string
		exact bool // the text fits one run, so the count must be the whole text's
	}{
		{"empty", "", true},
		{"one byte", "a", true},
		{"English", strings.Repeat("The quick brown fox jumps over the lazy dog. ", 8), true},
		{"English, long", strings.Repeat("The quick brown fox jumps over the lazy dog. ", 400), false},
		{"Chinese", strings.Repeat("今天天气很好，我们一起去公园散步。", 20), true},
		{"Chinese, long", strings.Repeat("今天天气很好，我们一起去公园散步。", 500), false},
		{"emoji", strings.Repeat("🙂🙃 ", 100), true},
		{"emoji, long", strings.Repeat("🙂🙃 ", 400), false},
		{"Korean", "안녕하세요 세계", true},
		{"mixed", "Hello 你好 お誕生日 🙂 func f() {}", true},
		{"invalid UTF-8", string([]byte{0xff, 0xc0, 0x80}), true},
		{"code", strings.Repeat("func f() error {\n\treturn nil\n}\n", 40), false},
	} {
		for _, tok := range []Tokenizer{TokenizerO200k, TokenizerCl100k} {
			checkCount(t, tok, tc.name, tc.text, tc.exact)
		}
	}
}

// checkCount holds one text's count to the tokenizer's count of it whole, within what the cut may move it, and to the
// same count run twice.
func checkCount(t *testing.T, tok Tokenizer, name, text string, exact bool) {
	t.Helper()
	want := wholeCount(t, tok, text)
	tolerance := want/100 + 2 // the cut's measured half a percent, and the token or two a cut adds
	if exact {
		tolerance = 0
	}
	c := NewTextCount(tok, math.MaxInt64)
	if c == nil {
		t.Fatalf("%s: no counter for %d", name, tok)
	}
	c.Add([]byte(text))
	got := countTotal(t, c)
	if off := got - want; off < -tolerance || off > tolerance {
		t.Errorf("%s in %d: counted %d, the tokenizer says %d", name, tok, got, want)
	}
	again := NewTextCount(tok, math.MaxInt64)
	again.Add([]byte(text))
	if countTotal(t, again) != got {
		t.Errorf("%s in %d: counted %d and then %d", name, tok, got, countTotal(t, again))
	}
}

// TestTextCountStopsAtItsLimit: a count takes no more text than its limit needs, which is all an estimate within the
// context can use, and the walk that hands the text out is told when to stop.
func TestTextCountStopsAtItsLimit(t *testing.T) {
	t.Parallel()
	text := []byte(strings.Repeat("今天天气很好，我们一起去公园散步。", 1000)) // 51 kilobytes, about 340 tokens a kilobyte
	c := NewTextCount(TokenizerO200k, 1000)
	if c.Add(text) {
		t.Error("the walk was told to go on past the limit")
	}
	if got := countTotal(t, c); got != 1000 {
		t.Errorf("counted %d, want exactly the limit", got)
	}
	before := countTotal(t, c)
	if c.Add(text) {
		t.Error("the walk was told to go on after the limit")
	}
	if countTotal(t, c) != before {
		t.Errorf("counted on to %d past the limit at %d", countTotal(t, c), before)
	}
}

// TestTextCountCountsNothing: a family without a tokenizer, and a limit the estimate can take nothing of, have no
// counter, and the bytes-per-token rule stands.
func TestTextCountCountsNothing(t *testing.T) {
	t.Parallel()
	if c := NewTextCount(TokenizerNone, 1000); c != nil {
		t.Errorf("a family without a tokenizer made a counter: %+v", c)
	}
	if c := NewTextCount(TokenizerO200k, 0); c != nil {
		t.Errorf("a limit of 0 made a counter: %+v", c)
	}
	if c := NewTextCount(TokenizerO200k, -1); c != nil {
		t.Errorf("a limit below 0 made a counter: %+v", c)
	}
}

// BenchmarkTextCount is the count on the call path: the tokens of a kilobyte of a request's text, and what it allocates.
func BenchmarkTextCount(b *testing.B) {
	for _, tc := range []struct{ name, text string }{
		{"english", strings.Repeat("The quick brown fox jumps over the lazy dog. ", 40)},
		{"chinese", strings.Repeat("今天天气很好，我们一起去公园散步。", 200)},
		{"unbroken", strings.Repeat("中文", 1707)},
		{"code", strings.Repeat("func f() error {\n\treturn nil\n}\n", 64)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			text := []byte(tc.text)
			c := NewTextCount(TokenizerO200k, math.MaxInt64)
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				c.total = 0 // the same counter, as one request's does not outlive its own text
				c.Add(text)
			}
		})
	}
}
