package jsonx

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
)

func nested(depth int) string { return strings.Repeat("[", depth) + strings.Repeat("]", depth) }

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       error // nil when valid; errSyntax for an error jsontext reports
	}{
		{"object", `{"a":[1,"é",null]}`, nil},
		{"scalar with whitespace", " \t\r\n0 \n", nil},
		{"deepest allowed", nested(MaxDepth), nil},
		{"one level too deep", nested(MaxDepth + 1), errTooDeep},
		{"too deep and unterminated", strings.Repeat("[", MaxDepth+1), errTooDeep},
		{"empty", "", errEmpty},
		{"whitespace only", " \n", errEmpty},
		{"two values", `{} {}`, errTrailing},
		{"value then garbage", `1 x`, errSyntax},
		{"duplicate name", `{"a":1,"a":2}`, errSyntax},
		{"invalid UTF-8", "\"\xff\"", errSyntax},
		{"escaped lone surrogate", `"\ud800"`, errSyntax},
		{"truncated", `{"a":`, errSyntax},
		{"large", "[" + strings.Repeat("1,", keepMax) + "1]", nil},
		{"large and too deep", nested(MaxDepth+1) + strings.Repeat(" ", keepMax), errTooDeep},
		{"large and duplicate name", `{"a":1,` + strings.Repeat(" ", keepMax) + `"a":2}`, errSyntax},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The Validator is shared by the cases, so a case that follows a failed or a large one shows what it kept.
			for name, validate := range map[string]func([]byte) error{"Validate": Validate, "Validator": validator.Validate} {
				err := validate([]byte(tc.data))
				var syntax *jsontext.SyntacticError
				if errors.Is(tc.want, errSyntax) && !errors.As(err, &syntax) || !errors.Is(tc.want, errSyntax) && !errors.Is(err, tc.want) {
					t.Fatalf("%s = %v, want %v", name, err, tc.want)
				}
			}
		})
	}
}

var validator Validator

var errSyntax = errors.New("syntax error")

func TestUnmarshal(t *testing.T) {
	type event struct {
		Type  string `json:"type"`
		Index int64  `json:"index"`
	}
	for _, tc := range []struct {
		name, data    string
		want          event
		err, knownErr error // of Unmarshal and UnmarshalKnown, as in TestValidate
	}{
		{"known members", `{"type":"ping","index":2}`, event{"ping", 2}, nil, nil},
		{"unknown member", `{"type":"ping","index":2,"extra":[1]}`, event{"ping", 2}, nil, json.ErrUnknownName},
		{"names match case", `{"Type":"ping"}`, event{}, nil, json.ErrUnknownName},
		{"duplicate name", `{"type":"a","type":"b"}`, event{}, errSyntax, errSyntax},
		{"too deep", `{"type":"a","x":` + nested(MaxDepth) + `}`, event{}, errTooDeep, errTooDeep},
	} {
		for _, f := range []struct {
			name      string
			unmarshal func([]byte, any) error
			err       error
		}{
			{"Unmarshal", Unmarshal, tc.err},
			{"UnmarshalKnown", UnmarshalKnown, tc.knownErr},
			{"Decode", func(data []byte, v any) error { return Decode(bytes.NewReader(data), v) }, tc.err},
		} {
			t.Run(f.name+"/"+tc.name, func(t *testing.T) {
				var got event
				err := f.unmarshal([]byte(tc.data), &got)
				var syntax *jsontext.SyntacticError
				if errors.Is(f.err, errSyntax) && !errors.As(err, &syntax) || !errors.Is(f.err, errSyntax) && !errors.Is(err, f.err) {
					t.Fatalf("%s = %v, want %v", f.name, err, f.err)
				}
				if f.err == nil && got != tc.want {
					t.Errorf("%s decoded %+v, want %+v", f.name, got, tc.want)
				}
			})
		}
	}
}

// TestDecodeEmpty: a body with nothing in it is io.EOF, which a generated handler with an optional body takes for "no
// body".
func TestDecodeEmpty(t *testing.T) {
	var v struct{}
	if err := Decode(strings.NewReader(""), &v); !errors.Is(err, io.EOF) {
		t.Errorf("Decode of nothing = %v, want io.EOF", err)
	}
}

func TestMarshalCanonical(t *testing.T) {
	type doc struct {
		Z     string            `json:"z"`
		Big   int64             `json:"big"`
		Float jsontext.Value    `json:"float"`
		Map   map[string]string `json:"map"`
		Text  string            `json:"text"`
	}
	v := doc{Z: "last", Big: 1<<53 + 1, Float: jsontext.Value(`1.50E2`), Map: map[string]string{"b": "2", "a": "1", "é": "3"}, Text: "<é>"}
	const want = `{"big":9007199254740993,"float":150,"map":{"a":"1","b":"2","é":"3"},"text":"<é>","z":"last"}`
	got, err := MarshalCanonical(v)
	if err != nil || string(got) != want {
		t.Fatalf("MarshalCanonical = %s, %v; want %s", got, err, want)
	}
	var back doc
	if err := UnmarshalKnown(got, &back); err != nil {
		t.Fatalf("UnmarshalKnown of %s: %v", got, err)
	}
	if again, err := MarshalCanonical(back); err != nil || string(again) != want {
		t.Errorf("round trip writes %s, %v; want %s", again, err, want)
	}
}

// FuzzValidate checks Validate against jsontext's own validator: they may disagree only on documents nested deeper
// than MaxDepth, which jsontext accepts.
func FuzzValidate(f *testing.F) {
	for _, seed := range []string{`{"a":1}`, `{"a":1,"a":2}`, "\"\xff\"", nested(MaxDepth + 1), `[1] 2`, ""} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		err := Validate(data)
		valid := jsontext.Value(data).IsValid()
		if err == nil && !valid || err != nil && valid && !errors.Is(err, errTooDeep) {
			t.Fatalf("Validate(%q) = %v, jsontext valid = %v", data, err, valid)
		}
	})
}

func BenchmarkValidate(b *testing.B) {
	for _, size := range []int{1 << 10, 1 << 20} {
		member := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello, world"}},`
		data := []byte("[" + strings.Repeat(member, size/len(member)) + "{}]")
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if err := Validate(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// members is an object of n unique members.
func members(n int) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%03d":0`, i)
	}
	b.WriteByte('}')
	return b.String()
}

// nestedMembers is an object nested depth levels deep with as many unique members at its deepest level as keepMax
// bytes allow.
func nestedMembers(depth int) string {
	const wrap = len(`{"a":}`)
	n := max(1, (keepMax-wrap*(depth-1))/len(`"k000":0,`))
	return strings.Repeat(`{"a":`, depth-1) + members(n) + strings.Repeat("}", depth-1)
}

func TestValidatorKeeps(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		kept       bool
	}{
		{"small", `{"a":[1]}`, true},
		{"largest", members(1) + strings.Repeat(" ", keepMax-len(members(1))), true},
		{"too large", members(1) + strings.Repeat(" ", keepMax-len(members(1))+1), false},
		{"deepest kept", nested(keepDepth), true},
		{"too deep", nested(keepDepth + 1), false},
		{"too deep and invalid", strings.Repeat("[", keepDepth+1) + "x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v Validator
			if err := v.Validate([]byte(tc.data)); err != nil {
				t.Logf("invalid, as expected for some cases: %v", err)
			}
			if kept := v.dec != nil; kept != tc.kept {
				t.Errorf("keeps its decoder: %t, want %t", kept, tc.kept)
			}
		})
	}
}

// heapGrowth is the heap that grew from before to after. HeapAlloc is the whole process heap, so another allocation
// freed between the two readings can make the second smaller: a shrinking heap grew by nothing, where subtracting the
// two unsigned readings would wrap into a huge number.
func heapGrowth(before, after uint64) uint64 {
	if after <= before {
		return 0
	}
	return after - before
}

// retainedBy estimates the heap a Validator holds after validating docs in turn, as the growth of the process heap: it
// is an estimate, because other heap activity during the run adds to it or hides part of it.
func retainedBy(t *testing.T, docs []string) uint64 {
	t.Helper()
	var v Validator
	var before, after runtime.MemStats
	runtime.GC()
	runtime.GC() // twice: the first sweep may leave the garbage of the test before
	runtime.ReadMemStats(&before)
	for _, doc := range docs {
		if err := v.Validate([]byte(doc)); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(&v)
	runtime.KeepAlive(docs)
	return heapGrowth(before.HeapAlloc, after.HeapAlloc)
}

// TestHeapGrowth: a heap that shrank between the readings grew by nothing, so it cannot fail a retention test.
func TestHeapGrowth(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after uint64
		want          uint64
	}{
		{"grew", 1 << 20, 3 << 20, 2 << 20},
		{"unchanged", 1 << 20, 1 << 20, 0},
		{"shrank", 3 << 20, 1 << 20, 0},
	} {
		if got := heapGrowth(tc.before, tc.after); got != tc.want {
			t.Errorf("%s: heapGrowth(%d, %d) = %d, want %d", tc.name, tc.before, tc.after, got, tc.want)
		}
	}
}

// TestValidatorRetains: what a Validator holds after documents of any shape stays small. A decoder keeps a table of
// member names for every nesting level a document has used and clears one only when a later document reaches that
// level again, so documents of falling depth, each with many members at its deepest level, leave a table at every
// level behind: 1.9 MB for 256 documents of 200 members each, none over 3.4 KB, under a 4 KiB size rule alone.
func TestValidatorRetains(t *testing.T) {
	const retainedMax = 192 << 10
	cases := map[string][]string{"one large document": {members(5000)}}
	for _, top := range []int{keepDepth, 4 * keepDepth, MaxDepth} {
		var falling []string
		for depth := top; depth >= 1; depth-- {
			falling = append(falling, nestedMembers(depth))
		}
		cases[fmt.Sprintf("documents falling from depth %d", top)] = falling
	}
	for name, docs := range cases {
		t.Run(name, func(t *testing.T) {
			if got := retainedBy(t, docs); got > retainedMax {
				t.Errorf("the Validator holds %d bytes, want at most %d", got, retainedMax)
			} else {
				t.Logf("holds %d bytes", got)
			}
		})
	}
}
