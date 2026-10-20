package billing

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/tiktoken-go/tokenizer"
	"github.com/tiktoken-go/tokenizer/codec"
)

// Tokenizer is the local tokenizer of one model family: the one that counts a request's text when the upstream reports
// no input usage, where the family publishes one. TokenizerNone keeps the bytes-per-token rule.
type Tokenizer uint8

// The tokenizers, by the family that publishes them. Only OpenAI does: Anthropic publishes none, so its models keep the
// bytes-per-token rule rather than be counted with another vendor's tokenizer, which measured 1.7 times apart on
// Chinese text between the two OpenAI ones alone (docs/domains/billing.md「用量来源与暂估」).
const (
	TokenizerNone Tokenizer = iota
	// TokenizerO200k is OpenAI's o200k_base, which counts from GPT-4o and GPT-4.1 on, and o1, o3 and o4.
	TokenizerO200k
	// TokenizerCl100k is OpenAI's cl100k_base, which counts up to GPT-4 and GPT-3.5.
	TokenizerCl100k
)

// TokenizerOf reads which tokenizer counts a model's text from the name its endpoint calls it by, as the vendor or a
// relay spells it: openai/gpt-4o and ft:gpt-4o-2024-08-06:acme::1 are gpt-4o. A name of no known family has none: a
// supplier's own name for a model costs the count, never a wrong tokenizer.
func TokenizerOf(name string) Tokenizer {
	name = strings.ToLower(name)
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:] // a relay's own prefix
	}
	name = strings.TrimPrefix(name, "ft:") // a fine-tuned name carries the model it tunes
	switch {
	case named(name, "gpt-4o", "gpt-4.1", "gpt-5", "chatgpt-4o", "o1", "o3", "o4"):
		return TokenizerO200k
	case named(name, "gpt-4", "gpt-3.5", "gpt-35"):
		return TokenizerCl100k
	}
	return TokenizerNone
}

// named reports whether name is one of prefixes or a version or variant of one: gpt-5 names gpt-5.1 and gpt-5-mini but
// not gpt-50, and gpt-4o names the :free variant a relay spells that way.
func named(name string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		rest, ok := strings.CutPrefix(name, prefix)
		if ok && (rest == "" || rest[0] == '-' || rest[0] == '.' || rest[0] == ':') {
			return true
		}
	}
	return false
}

// The codecs, built once each: building one reads its vocabulary, 200k or 100k tokens as Go maps, and compiles its
// split pattern, so it is built by the first count that needs it rather than at startup: 15 ms and 7 MB for o200k,
// 5 ms and 4 MB for cl100k (measured 2026-10-20, docs/domains/billing.md「用量来源与暂估」). They come from the
// module's codec package rather than its Get, which would link the p50k and r50k vocabularies too: 2.7 MB of binary no
// request here uses (measured 2026-10-20).
var (
	o200k  = sync.OnceValue(func() tokenizer.Codec { return codec.NewO200kBase() })
	cl100k = sync.OnceValue(func() tokenizer.Codec { return codec.NewCl100kBase() })
)

// NewTextCount starts counting a request's text in the tokens of a family, up to limit tokens: the most of the text the
// estimate can take, which the context clips anyway. It returns nil when the family has no tokenizer, and the
// bytes-per-token rule stands.
func NewTextCount(t Tokenizer, limit int64) *TextCount {
	c := codecOf(t)
	if c == nil || limit <= 0 {
		return nil
	}
	return &TextCount{codec: c, limit: limit}
}

// codecOf is the family's codec, nil for a family without one.
func codecOf(t Tokenizer) tokenizer.Codec {
	switch t {
	case TokenizerO200k:
		return o200k()
	case TokenizerCl100k:
		return cl100k()
	case TokenizerNone:
		return nil
	}
	return nil
}

// TextCount counts a request's text in one family's tokens, a piece at a time, and stops at its limit.
type TextCount struct {
	codec tokenizer.Codec
	limit int64
	total int64
}

// Add counts one piece of the request's text and reports whether to go on: false once the count reached its limit,
// which ends the walk that hands the text out. A piece the tokenizer fails on ends it too, at the count it reached;
// Count has no such piece for text it was given.
func (c *TextCount) Add(piece []byte) bool {
	for len(piece) > 0 && c.total < c.limit {
		cut := cutOf(piece)
		counted, err := c.codec.Count(string(piece[:cut]))
		if err != nil {
			return false
		}
		c.total += int64(counted)
		piece = piece[cut:]
	}
	return c.total < c.limit
}

// Total is the number of tokens counted so far.
func (c *TextCount) Total() int64 { return c.total }

// maxRunBytes is where the count cuts a piece of text. The tokenizer merges byte pairs within one run of letters or
// digits in time quadratic in the run, so a run without a break costs its square: 16 KiB of Chinese without a break took
// 117 ms as one piece and 8.6 ms cut at 1 KiB, 64 KiB took 1.9 s against 33 ms, and cutting moved the count by under 0.1%
// on such a run and under 0.5% on English, Chinese and Go source (measured 2026-10-20). new-api cuts at the same size
// for the same reason (QuantumNous/new-api tokenkit/tokenizer.go, maxRunBytes, 2026-10-20).
const maxRunBytes = 1024

// cutOf is where a piece is cut: maxRunBytes, backed up to a rune boundary so that no character is split.
func cutOf(piece []byte) int {
	n := min(len(piece), maxRunBytes)
	if n == len(piece) {
		return n
	}
	for n > 1 && !utf8.RuneStart(piece[n]) {
		n--
	}
	return n
}
