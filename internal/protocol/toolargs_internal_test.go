package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // the scanner is held to jsontext's grammar
	"strings"
	"testing"
	"unicode/utf8"

	"axiom/internal/platform/jsonx"
)

// feedText feeds text to s in pieces of at most cut bytes, each quoted as an event carries it, split only between
// characters as a JSON string's pieces are.
func feedText(t *testing.T, s *argScan, text string, cut int) {
	t.Helper()
	b := []byte(text)
	for len(b) > 0 {
		n := min(cut, len(b))
		for n < len(b) && !utf8.RuneStart(b[n]) {
			n++
		}
		piece, err := jsontext.AppendQuote(nil, b[:n])
		if err != nil {
			t.Fatal(err)
		}
		feedArgs(s, piece)
		b = b[n:]
	}
}

// FuzzArgScan: the scanner accepts exactly the JSON texts jsontext does, repeated names allowed, however the text is
// split into pieces and whatever its pieces escape; whitespace alone is accepted as a call without arguments.
func FuzzArgScan(f *testing.F) {
	for _, seed := range []string{
		`{}`, ` {"a":[1,-2.5e+3,true,false,null,"xé\n"]} `, `0`, `-0.0E-0`, `"s"`, `[]`, `[[[]]]`, ``, `  `,
		`{"a":1,}`, `[1,]`, `01`, `1.`, `.5`, `1e`, `-`, `tru`, `nul`, `{"a" 1}`, `{1:2}`, `"\x"`, `"\u12g4"`, "\"\x01\"",
		`{} {}`, `{"a":1,"a":2}`, `]`, `{]`, `[}`, `"abc`, `{"a":{"b":[{"c":null}]}}`, `{"é":"😀"}`, "{\"a\":\"\t\"}",
	} {
		f.Add(seed, 3)
	}
	f.Fuzz(func(t *testing.T, text string, cut int) {
		if !utf8.ValidString(text) || strings.Count(text, "[")+strings.Count(text, "{") > 200 { // an event's string is valid UTF-8; jsontext nests deeper than jsonx.MaxDepth
			return
		}
		var s argScan
		feedText(t, &s, text, max(1, cut%16+1))
		blank := strings.TrimLeft(text, " \t\r\n") == ""
		// JSON.parse accepts a lone surrogate escape such as \uD800, which strict jsontext refuses as invalid UTF-8.
		want := blank || jsontext.Value(text).IsValid(jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
		if got := s.ok(); got != want {
			t.Errorf("ok(%q) = %v, want %v", text, got, want)
		}
	})
}

// TestArgScanDepth: arguments nest at most jsonx.MaxDepth levels, as everything else the gateway reads.
func TestArgScanDepth(t *testing.T) {
	t.Parallel()
	for depth, want := range map[int]bool{jsonx.MaxDepth: true, jsonx.MaxDepth + 1: false} {
		var s argScan
		feedText(t, &s, strings.Repeat("[", depth)+strings.Repeat("]", depth), 64)
		if got := s.ok(); got != want {
			t.Errorf("%d levels: ok = %v, want %v", depth, got, want)
		}
	}
}
