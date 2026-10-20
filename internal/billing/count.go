package billing

import (
	"fmt"
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
// bytes-per-token rule rather than use an unrelated vocabulary.
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
	case named(name, "gpt-4o", "gpt-4.1", "gpt-4.5", "gpt-5", "chatgpt-4o", "o1", "o3", "o4"):
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

// Adapted from new-api tokenkit/tokenizer.go (206e0fa, 2026-10-20): share immutable codecs and link only the two
// vocabularies used here. Count is concurrent-safe; Decode mutates a reverse vocabulary and is never used here.
var (
	o200k  = sync.OnceValue(func() tokenizer.Codec { return codec.NewO200kBase() })
	cl100k = sync.OnceValue(func() tokenizer.Codec { return codec.NewCl100kBase() })
)

// NewTextCount starts counting a request's text in the tokens of a family, up to limit tokens: the most of the text the
// estimate can take, which the context clips anyway. It returns nil when the family has no tokenizer, and the
// bytes-per-token rule stands.
func NewTextCount(t Tokenizer, limit int64) *TextCount {
	if limit <= 0 {
		return nil
	}
	c := codecOf(t)
	if c == nil {
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
	err   error
}

// Add counts one piece of the request's text and reports whether to go on: false once the count reached its limit,
// which ends the walk that hands the text out. A tokenizer failure invalidates the whole count: callers must fall
// back rather than charge a partial count.
func (c *TextCount) Add(piece []byte) bool {
	if c.err != nil {
		return false
	}
	for len(piece) > 0 && c.total < c.limit {
		cut := cutOf(piece)
		counted, err := c.codec.Count(string(piece[:cut]))
		if err != nil {
			c.err = fmt.Errorf("count input tokens: %w", err)
			return false
		}
		c.total += min(int64(counted), c.limit-c.total)
		piece = piece[cut:]
	}
	return c.total < c.limit
}

// Total returns the capped count, or no count if any piece failed.
func (c *TextCount) Total() (int64, error) {
	if c.err != nil {
		return 0, c.err
	}
	return c.total, nil
}

// Bound each BPE run: merging pairs in one unbroken run can take quadratic time. Like new-api's maxRunBytes
// (tokenkit/tokenizer.go, 206e0fa), cap it at 1 KiB. Cuts can move tokens across boundaries, so this is an estimate.
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
