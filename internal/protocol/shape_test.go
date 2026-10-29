package protocol

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"axiom/internal/platform/textshape"
)

// TestAddShape: a JSON string is tallied as its decoded text, whichever escapes it is written with: a two-character
// escape, \u for any character and a surrogate pair for one beyond the basic plane; a lone surrogate is the invalid
// character it decodes to. Its bytes agree with unquotedLen, which counts the content the tally's ratio scales.
func TestAddShape(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // seeded, so a failure reproduces
	alphabet := []rune("ab z1\n\t\"\\/你好お안🙂∑@#é \b\f\r")
	for range 2000 {
		var text []rune
		for range r.IntN(20) {
			text = append(text, alphabet[r.IntN(len(alphabet))])
		}
		quoted := quote(text, r)
		var got, want textshape.Shape
		addShape(&got, []byte(quoted))
		want.Add([]byte(string(text)))
		if got != want {
			t.Fatalf("%s: tallied %+v, want %+v", quoted, got, want)
		}
		if n := unquotedLen([]byte(quoted)); n != got.Bytes {
			t.Fatalf("%s: tallied %d bytes, unquotedLen %d", quoted, got.Bytes, n)
		}
	}
	var lone textshape.Shape
	addShape(&lone, []byte(`"\ud83dx"`))
	if lone.Counts[textshape.Symbol] != 1 || lone.Counts[textshape.Word] != 1 {
		t.Errorf("a lone surrogate tallied %+v", lone)
	}
	addShape(&lone, nil)
	addShape(&lone, []byte(`""`))
	if lone.Bytes != 4 {
		t.Errorf("an empty string or null added to the tally: %+v", lone)
	}
}

// quote writes text as a JSON string, each character as itself where JSON allows or else escaped, at random.
func quote(text []rune, r *rand.Rand) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range text {
		short := map[rune]string{'"': `\"`, '\\': `\\`, '\n': `\n`, '\t': `\t`, '\b': `\b`, '\f': `\f`, '\r': `\r`, '/': `\/`}
		switch {
		case short[c] != "" && (c < 0x20 || c == '"' || c == '\\' || r.IntN(2) == 0):
			b.WriteString(short[c])
		case c < 0x20 || r.IntN(3) == 0:
			if c > 0xffff {
				hi, lo := utf16.EncodeRune(c)
				fmt.Fprintf(&b, `\u%04x\u%04X`, hi, lo)
			} else {
				fmt.Fprintf(&b, `\u%04x`, c)
			}
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	if !utf8.ValidString(b.String()) {
		panic("invalid")
	}
	return b.String()
}
