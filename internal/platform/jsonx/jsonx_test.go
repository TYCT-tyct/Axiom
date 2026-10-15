package jsonx

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate([]byte(tc.data))
			var syntax *jsontext.SyntacticError
			if errors.Is(tc.want, errSyntax) && !errors.As(err, &syntax) || !errors.Is(tc.want, errSyntax) && !errors.Is(err, tc.want) {
				t.Fatalf("Validate = %v, want %v", err, tc.want)
			}
		})
	}
}

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
		}{{"Unmarshal", Unmarshal, tc.err}, {"UnmarshalKnown", UnmarshalKnown, tc.knownErr}} {
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
