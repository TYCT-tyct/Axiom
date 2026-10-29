package textshape

import (
	"math"
	"testing"
	"testing/quick"
	"unicode/utf8"
)

func shapeOf(text string) Shape {
	var s Shape
	s.Add([]byte(text))
	return s
}

// TestShape: each run of letters or digits counts once, every other character on its own.
func TestShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text string
		want map[Feature]int64
	}{
		{"", nil},
		{"a", map[Feature]int64{Word: 1}},
		{"Hello, world!", map[Feature]int64{Word: 2, Symbol: 2, Space: 1}},
		{"abc123def", map[Feature]int64{Word: 2, Number: 1}},
		{"今天天气", map[Feature]int64{CJK: 4}},
		{"お誕生日 안녕", map[Feature]int64{CJK: 6, Space: 1}},
		{"🙂🙃", map[Feature]int64{Emoji: 2}},
		{"a\n\tb", map[Feature]int64{Word: 2, Newline: 2}},
		{"https://x.io/a?b=1#c", map[Feature]int64{Word: 6, Number: 1, URLDelim: 7, Symbol: 1}},
		{"me@x", map[Feature]int64{Word: 2, At: 1}},
		{"∑x≤2", map[Feature]int64{Math: 2, Word: 1, Number: 1}},
		{"x²±1", map[Feature]int64{Word: 1, Number: 2, Math: 1}}, // a superscript is a digit
		{"Ünïcödé", map[Feature]int64{Word: 1}},
		{string([]byte{0xff, 'a', 0xc0}), map[Feature]int64{Symbol: 2, Word: 1}},
	} {
		s := shapeOf(tc.text)
		var want [NumFeatures]int64
		for f, n := range tc.want {
			want[f] = n
		}
		if s.Counts != want || s.Bytes != int64(len(tc.text)) {
			t.Errorf("%q: counts %v over %d bytes, want %v", tc.text, s.Counts, s.Bytes, want)
		}
	}
}

// TestShapeAcrossPieces: a text tallied in pieces cut between characters is tallied as it is whole, a run cut in two
// included, which is how a stream's deltas arrive.
func TestShapeAcrossPieces(t *testing.T) {
	t.Parallel()
	f := func(text string, cuts []uint16) bool {
		var s Shape
		rest := []byte(text)
		for _, c := range cuts {
			n := int(c) % (len(rest) + 1)
			for n > 0 && n < len(rest) && !utf8.RuneStart(rest[n]) {
				n--
			}
			s.Add(rest[:n])
			rest = rest[n:]
		}
		s.Add(rest)
		return s == shapeOf(text)
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
	if s := shapeOf("token"); s != func() Shape { var p Shape; p.Add([]byte("tok")); p.Add([]byte("en")); return p }() {
		t.Error("a word cut between two pieces was counted twice")
	}
}

// TestWeightsBounds: whatever the text and the weights, the tokens never pass the bytes, and a text scaled to more
// bytes is held to those bytes; the extremes of the integers saturate rather than wrap.
func TestWeightsBounds(t *testing.T) {
	t.Parallel()
	f := func(text []byte, w Weights, bytes int64) bool {
		for i := range w {
			w[i] = abs(w[i])
		}
		s := Shape{}
		s.Add(text)
		n := w.Tokens(s)
		scaled, ok := w.Scaled(s, abs(bytes))
		return n >= 0 && n <= s.Bytes && ok == (len(text) > 0) && scaled >= 0 && scaled <= abs(bytes)
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
	huge := Shape{Bytes: math.MaxInt64}
	huge.Counts[Word] = math.MaxInt64
	if n := (Weights{Word: math.MaxInt64}).Tokens(huge); n != math.MaxInt64 {
		t.Errorf("an overflowing tally gave %d, want its bytes", n)
	}
	if n, _ := (Weights{Word: 100}).Scaled(shapeOf("ab"), math.MaxInt64); n != math.MaxInt64/2+1 {
		t.Errorf("scaled to the largest bytes gave %d", n)
	}
}

func abs(n int64) int64 {
	if n == math.MinInt64 {
		return math.MaxInt64
	}
	return max(n, -n)
}

// TestWeights: the weights are hundredths of a token per feature, the sum rounded up; scaling takes the tally's own
// tokens per byte to the bytes asked for.
func TestWeights(t *testing.T) {
	t.Parallel()
	w := Weights{Word: 124, Space: 14, Symbol: 66}
	s := shapeOf("Hello, world!") // 2 words, 2 symbols, a space: 248 + 132 + 14 = 394 hundredths
	if n := w.Tokens(s); n != 4 {
		t.Errorf("tokens %d, want 4", n)
	}
	if n, ok := w.Scaled(s, 26); !ok || n != 8 {
		t.Errorf("scaled to twice the bytes: %d, %v, want 8", n, ok)
	}
	if n, ok := w.Scaled(s, 0); !ok || n != 0 {
		t.Errorf("scaled to no bytes: %d, %v", n, ok)
	}
	if _, ok := w.Scaled(Shape{}, 10); ok {
		t.Error("an empty tally gave a ratio")
	}
}

// BenchmarkShapeAdd is the tally on the stream path, per kilobyte of a delta's text.
func BenchmarkShapeAdd(b *testing.B) {
	for _, tc := range []struct{ name, text string }{
		{"english", "The quick brown fox jumps over the lazy dog. "},
		{"chinese", "今天天气很好，我们一起去公园散步。"},
		{"code", "func f() error {\n\treturn nil\n}\n"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			text := make([]byte, 0, 1100)
			for len(text) < 1024 {
				text = append(text, tc.text...)
			}
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			var s Shape
			for range b.N {
				s.Add(text)
			}
		})
	}
}

// TestMerge: a tally merged after another adds to it, without continuing the run the first left open; merging no text
// leaves that run open.
func TestMerge(t *testing.T) {
	t.Parallel()
	s := shapeOf("to")
	s.Merge(Shape{})
	s.Add([]byte("ken"))
	if s.Counts[Word] != 1 {
		t.Errorf("merging no text broke the run: %+v", s)
	}
	s.Merge(shapeOf("s, ok"))
	if s.Counts[Word] != 3 || s.Bytes != int64(len("tokens, ok")) {
		t.Errorf("merged %+v", s)
	}
}
