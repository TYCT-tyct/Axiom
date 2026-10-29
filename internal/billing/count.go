package billing

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/tiktoken-go/tokenizer"
	"github.com/tiktoken-go/tokenizer/codec"

	"axiom/internal/platform/textshape"
)

// Tokenizer is the local tokenizer of one model family: what counts a request's text when the upstream reports no
// input usage, and estimates the reply's text when it reports no output. TokenizerNone keeps the bytes-per-token rule.
type Tokenizer uint8

// The tokenizers, by family. OpenAI publishes its own, which count a request's text exactly. Anthropic publishes none
// for Claude 3 and later, so its two families are estimated from the text's shape (textshape) at weights measured
// against its reported usage; so is every family's reply, which is tallied as it streams, where running a vocabulary
// on every call would cost the call path for the few calls whose output goes unreported.
const (
	TokenizerNone Tokenizer = iota
	// TokenizerO200k is OpenAI's o200k_base for GPT-4o, GPT-4.1, GPT-4.5, GPT-5, o1, o3 and o4.
	TokenizerO200k
	// TokenizerCl100k is OpenAI's cl100k_base, which counts up to GPT-4 and GPT-3.5.
	TokenizerCl100k
	// TokenizerClaude is Claude's before Claude Opus 4.7.
	TokenizerClaude
	// TokenizerClaude47 is the one Claude Opus 4.7 introduced, which takes about a third more tokens for the same text,
	// and every later Claude's.
	TokenizerClaude47
	NumTokenizers
)

// TokenizerOf reads which tokenizer counts a model's text from the name its endpoint calls it by, as the vendor or a
// relay spells it: openai/gpt-4o and ft:gpt-4o-2024-08-06:acme::1 are gpt-4o, and us.anthropic.claude-opus-4-7-v1:0
// is Claude Opus 4.7 on Bedrock. A name of no known family has none: a supplier's own name for a model costs the
// count, never a wrong tokenizer. OpenAI's tiktoken/model.py publishes no GPT-6 encoding yet (2026-10-20), so those
// names also keep the byte rule.
func TokenizerOf(name string) Tokenizer {
	name = strings.ToLower(name)
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:] // a relay's own prefix
	}
	name = strings.TrimPrefix(name, "ft:") // a fine-tuned name carries the model it tunes
	if rest, ok := claudeName(name); ok {
		if major, minor, ok := claudeVersion(rest); ok && (major < 4 || major == 4 && minor < 7) {
			return TokenizerClaude
		}
		return TokenizerClaude47 // a Claude name without a version is a new one, at the larger count
	}
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

// claudeName is a Claude model's name after its claude- prefix, which a cloud's region and vendor may precede
// (us.anthropic.claude-…): claude-3-5-sonnet, claude-sonnet-4-5-20250929, claude-opus-4-7@20260416.
func claudeName(name string) (string, bool) {
	i := strings.Index(name, "claude-")
	if i < 0 || i > 0 && name[i-1] != '.' {
		return "", false
	}
	return name[i+len("claude-"):], true
}

// claudeVersion reads a Claude name's version: its first number of one or two digits, after the model's own name
// where that comes first (sonnet-4-5, 3-5-sonnet), and the number right after it. A date of eight digits, or a word,
// ends the version.
func claudeVersion(rest string) (major, minor int, ok bool) {
	var version []int
	for part := range strings.FieldsFuncSeq(rest, func(r rune) bool { return r == '-' || r == '.' || r == '@' }) {
		digits := len(part) - len(strings.TrimLeft(part, "0123456789"))
		if digits == 0 || digits > 2 {
			if len(version) > 0 {
				break
			}
			continue
		}
		n := int(part[0] - '0')
		if digits == 2 {
			n = n*10 + int(part[1]-'0')
		}
		version = append(version, n)
		if len(version) == 2 || digits < len(part) { // a suffix such as v1:0 ends it
			break
		}
	}
	switch len(version) {
	case 0:
		return 0, 0, false
	case 1:
		return version[0], 0, true
	}
	return version[0], version[1], true
}

// shapeWeights are each family's tokens per feature of a text, in hundredths. They are new-api's, calibrated against
// the usage Anthropic and OpenAI reported for 54 pieces of Claude Code and Codex sessions (new-api tokenkit/tokenkit.go,
// e449cc7, 2026-10-28); docs/domains/billing.md「用量来源与暂估」 has the error measured here.
var shapeWeights = [NumTokenizers]textshape.Weights{
	//                  Word Number CJK Symbol Math URL  At   Emoji Newline Space
	TokenizerO200k:    {75, 186, 96, 60, 268, 77, 200, 212, 95, 17},
	TokenizerCl100k:   {75, 186, 96, 60, 268, 77, 200, 212, 95, 17},
	TokenizerClaude:   {124, 153, 138, 66, 452, 74, 282, 260, 119, 14},
	TokenizerClaude47: {152, 177, 140, 89, 452, 145, 282, 260, 138, 21},
}

// TextTokens estimates the tokens of bytes of text shaped like s in family t's tokens: s's own tokens per byte, at
// the family's weights, over all the bytes. ok is false for a family without weights, and for a tally of no text,
// and then the bytes-per-token rule stands. It never exceeds bytes.
func TextTokens(t Tokenizer, s textshape.Shape, bytes int64) (int64, bool) {
	if t == TokenizerNone || t >= NumTokenizers {
		return 0, false
	}
	return shapeWeights[t].Scaled(s, bytes)
}

// Adapted from new-api tokenkit/tokenizer.go (206e0fa, 2026-10-20): share immutable codecs and link only the two
// vocabularies used here. Count is concurrent-safe; Decode mutates a reverse vocabulary and is never used here.
var (
	o200k  = sync.OnceValue(func() tokenizer.Codec { return codec.NewO200kBase() })
	cl100k = sync.OnceValue(func() tokenizer.Codec { return codec.NewCl100kBase() })
)

// WarmTokenizers builds the two immutable vocabularies before a node accepts calls.
func WarmTokenizers() {
	o200k()
	cl100k()
}

// NewTextCount starts counting a request's text in the tokens of a family, up to limit tokens: the most of the text the
// estimate can take, which the context clips anyway. A family with a vocabulary counts with it, one without estimates
// from the text's shape. It returns nil when the family has neither, and the bytes-per-token rule stands.
func NewTextCount(t Tokenizer, limit int64) *TextCount {
	if limit <= 0 || t == TokenizerNone || t >= NumTokenizers {
		return nil
	}
	return &TextCount{codec: codecOf(t), weights: shapeWeights[t], limit: limit}
}

// codecOf is the family's codec, nil for a family without one.
func codecOf(t Tokenizer) tokenizer.Codec {
	switch t {
	case TokenizerO200k:
		return o200k()
	case TokenizerCl100k:
		return cl100k()
	case TokenizerNone, TokenizerClaude, TokenizerClaude47, NumTokenizers:
		return nil
	}
	return nil
}

// TextCount counts a request's text in one family's tokens, a piece at a time, and stops at its limit.
type TextCount struct {
	codec   tokenizer.Codec // nil for a family estimated from its shape
	weights textshape.Weights
	shape   textshape.Shape
	limit   int64
	total   int64
	err     error
}

// Add counts one piece of the request's text and reports whether to go on: false once the count reached its limit,
// which ends the walk that hands the text out. A tokenizer failure invalidates the whole count: callers must fall
// back rather than charge a partial count.
func (c *TextCount) Add(piece []byte) bool {
	if c.err != nil {
		return false
	}
	if c.codec == nil {
		if c.total < c.limit {
			c.shape.Add(piece)
			c.total = min(c.weights.Tokens(c.shape), c.limit)
		}
		return c.total < c.limit
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
