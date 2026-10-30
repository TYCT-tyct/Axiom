// Package textshape tallies the features of a text that a byte-pair tokenizer's count follows: runs of letters and of
// digits, CJK characters, emoji, symbols and whitespace. A family's weights per feature turn the tally into an estimate
// of its tokens where the family publishes no tokenizer to count with, and the tally is cheap enough to keep as a
// stream goes by: one pass, no allocation, no state but the run the last character continues.
package textshape

import (
	"math"
	"math/bits"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Feature is one kind of text a tokenizer cuts alike.
type Feature uint8

// The features. A run of letters, and a run of digits, is one Word or Number whatever its length: a tokenizer's
// vocabulary holds whole common words, and the weight is what a run takes on average.
const (
	Word     Feature = iota // a run of letters
	Number                  // a run of digits
	CJK                     // a Chinese, Japanese or Korean character
	Symbol                  // a punctuation mark or any other character, and an invalid byte
	Math                    // a mathematical symbol, such as ∑ or ≤
	URLDelim                // one of / : ? & = ; # %
	At                      // an @, which splits the words around it
	Emoji                   // an emoji
	Newline                 // a newline or a tab
	Space                   // any other whitespace
	NumFeatures
)

// Shape is a text's tally. The zero value is the empty text; Add extends it.
type Shape struct {
	// Bytes are the UTF-8 bytes tallied, the text a ratio of tokens per byte is taken over.
	Bytes  int64
	Counts [NumFeatures]int64
	run    Feature // Word or Number, while open
	open   bool    // a run of letters or digits continues into the next character of its feature
	lead   Feature // the feature of the text's first character, which Merge may join to the run before it
}

// Add tallies text, continuing the run the last Add left open: a word cut between two pieces is one word.
func (s *Shape) Add(text []byte) {
	if s.Bytes == 0 && len(text) > 0 {
		s.lead = first(text)
	}
	s.Bytes += int64(len(text))
	for i := 0; i < len(text); {
		c := text[i]
		if c < utf8.RuneSelf {
			s.tally(ascii[c])
			i++
			continue
		}
		r, n := utf8.DecodeRune(text[i:])
		s.tally(classify(r))
		i += n
	}
}

// Merge adds the tally of a text that follows s's, as Add would have tallied the two texts in one: a run open at the
// end of s that o's first character continues is one run, not two. A stream tallies each event on its own and merges
// it, and its deltas cut words wherever the upstream's tokens end, so without the join every cut would count a run
// the weights, measured on whole texts, already hold.
func (s *Shape) Merge(o Shape) {
	if o.Bytes == 0 {
		return // no text: the run s left open stays open
	}
	if s.Bytes == 0 {
		s.lead = o.lead
	} else if s.open && s.run == o.lead {
		s.Counts[s.run]-- // o's first run continues s's, which s already counted
	}
	s.Bytes += o.Bytes
	for f, n := range o.Counts {
		s.Counts[f] += n
	}
	s.run, s.open = o.run, o.open
}

// first is the feature of text's first character.
func first(text []byte) Feature {
	if text[0] < utf8.RuneSelf {
		return ascii[text[0]]
	}
	r, _ := utf8.DecodeRune(text)
	return classify(r)
}

// tally counts one character of feature f, a letter or digit only where it opens a run.
func (s *Shape) tally(f Feature) {
	if f == Word || f == Number {
		if s.open && s.run == f {
			return
		}
		s.run, s.open = f, true
	} else {
		s.run, s.open = 0, false // run is only read while open: cleared, so equal tallies compare equal
	}
	s.Counts[f]++
}

// ascii is the feature of each ASCII byte, read once per byte on the stream path. The marks with features of their
// own are all ASCII.
var ascii = func() (t [utf8.RuneSelf]Feature) {
	for c := range t {
		switch {
		case c == '\n' || c == '\t':
			t[c] = Newline
		case c == '@':
			t[c] = At
		case strings.IndexByte("/:?&=;#%", byte(c)) >= 0:
			t[c] = URLDelim
		default:
			t[c] = classify(rune(c))
		}
	}
	return t
}()

// classify is the feature of r beyond ascii's marks: fixed blocks first, the CJK ideographs leading as most of Chinese text, then the Unicode
// categories.
func classify(r rune) Feature {
	for _, b := range blocks {
		if b.lo <= r && r <= b.hi {
			return b.feature
		}
	}
	switch {
	case unicode.IsSpace(r):
		return Space
	case unicode.Is(unicode.Han, r):
		return CJK
	case unicode.IsNumber(r):
		return Number
	case unicode.IsLetter(r):
		return Word
	case isMath(r):
		return Math
	}
	return Symbol
}

var blocks = [...]struct {
	lo, hi  rune
	feature Feature
}{
	{0x4e00, 0x9fff, CJK},     // the CJK unified ideographs
	{0x3001, 0x303f, Symbol},  // CJK punctuation
	{0xff01, 0xff0f, Symbol},  // fullwidth punctuation, before the digits
	{0xff1a, 0xff20, Symbol},  // and between the digits and the letters
	{0x3040, 0x30ff, CJK},     // kana
	{0xac00, 0xd7a3, CJK},     // Hangul syllables
	{0x1f300, 0x1f9ff, Emoji}, // pictographs, emoticons and their supplement
	{0x2600, 0x27bf, Emoji},   // miscellaneous symbols and dingbats
	{0x1fa00, 0x1faff, Emoji}, // pictographs extended-A
}

func isMath(r rune) bool {
	return 0x2200 <= r && r <= 0x22ff || // mathematical operators
		0x2a00 <= r && r <= 0x2aff || // their supplement
		strings.ContainsRune("±×÷°′″‴⁺⁻⁼⁽⁾₊₋₌₍₎", r)
}

// Weights are a family's tokens per feature, in hundredths of a token.
type Weights [NumFeatures]int64

// Tokens is the tokens the shape's text takes at w, rounded up, and never more than its bytes: a byte-level tokenizer
// gives no more tokens than bytes, so the tally never claims more than the bound a reservation is made at.
func (w Weights) Tokens(s Shape) int64 {
	var hundredths uint64
	for f, n := range s.Counts {
		hi, lo := bits.Mul64(uint64(n), uint64(w[f])) //nolint:gosec // counts and weights are non-negative
		var carry uint64
		hundredths, carry = bits.Add64(hundredths, lo, 0)
		if hi != 0 || carry != 0 {
			return s.Bytes
		}
	}
	return min(int64(min((hundredths+99)/100, math.MaxInt64)), s.Bytes)
}

// Scaled is the tokens of bytes of text shaped like s, at s's tokens per byte, rounded up: the estimate for a text
// whose whole was not tallied, such as raw tool input beside the decoded text. ok is false when s holds no text to take
// a ratio from. It never exceeds bytes.
func (w Weights) Scaled(s Shape, bytes int64) (tokens int64, ok bool) {
	if s.Bytes <= 0 {
		return 0, false
	}
	if bytes <= 0 {
		return 0, true
	}
	hi, lo := bits.Mul64(uint64(bytes), uint64(w.Tokens(s))) //nolint:gosec // both non-negative
	q, r := bits.Div64(hi, lo, uint64(s.Bytes))              // Tokens ≤ s.Bytes, so the quotient is ≤ bytes: no overflow
	if r != 0 {
		q++
	}
	return min(int64(q), bytes), true //nolint:gosec // q ≤ bytes
}
