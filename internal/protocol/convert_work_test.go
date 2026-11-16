package protocol_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"axiom/internal/platform/alloctest"
	"axiom/internal/protocol"
)

// TestConvertSchemaRefs: definitions that share others are walked once each, so a chain of them that would take 2⁶⁴
// walks one path at a time converts at once; a definition that comes back to itself, or to the root, is recursion,
// which Anthropic's structured outputs refuse.
func TestConvertSchemaRefs(t *testing.T) {
	t.Parallel()
	var defs []string
	for i := range 64 {
		defs = append(defs, fmt.Sprintf(`"d%d":{"anyOf":[{"$ref":"#/$defs/d%d"},{"$ref":"#/$defs/d%d"}]}`, i, i+1, i+1))
	}
	defs = append(defs, `"d64":{"type":"string"}`)
	for name, c := range map[string]struct {
		defs string
		want error
	}{
		"shared definitions": {strings.Join(defs, ","), nil},
		"a cycle":            {`"d0":{"anyOf":[{"$ref":"#/$defs/d1"}]},"d1":{"anyOf":[{"$ref":"#/$defs/d0"},{"type":"null"}]}`, &protocol.NotSupported{}},
		"the root":           {`"d0":{"anyOf":[{"$ref":"#"},{"type":"null"}]}`, &protocol.NotSupported{}},
	} {
		t.Run(name, func(t *testing.T) {
			schema := `{"type":"object","properties":{"a":{"$ref":"#/$defs/d0"}},"required":["a"],"additionalProperties":false,"$defs":{` + c.defs + `}}`
			_, err := convertBody(protocol.Messages, chatBody(`"messages":[`+userHi+`],"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":`+schema+`}}`), nil)
			if ns, _ := errors.AsType[*protocol.NotSupported](err); (ns != nil) != (c.want != nil) || c.want == nil && err != nil {
				t.Errorf("error %v, want %v", err, c.want)
			}
		})
	}
}

// TestConvertManyDetails: reasoning a client streamed back in many pieces, of one index or of many, merges in work
// that grows with the pieces, not with their square: the bytes allocated stay within a small multiple of the body,
// where joining 20,000 pieces of 100 bytes by copying all before each would allocate 20 GB. The measurement runs in a
// child process, so this binary's other goroutines and collections are not counted as the conversion's.
func TestConvertManyDetails(t *testing.T) {
	t.Parallel()
	piece := strings.Repeat("x", 100)
	for name, index := range map[string]func(int) int{"one index": func(int) int { return 0 }, "an index each": func(i int) int { return i }} {
		t.Run(name, func(t *testing.T) {
			var details []string
			for i := range 20000 {
				details = append(details, fmt.Sprintf(`{"type":"reasoning.text","format":"anthropic-claude-v1","text":"%s","index":%d}`, piece, index(i)))
			}
			body := chatBody(`"messages":[` + userHi + `,{"role":"assistant","content":"x","reasoning_details":[` + strings.Join(details, ",") + `]},` + userHi + `]`)
			got := alloctest.Measure(t, alloctest.Function{Name: name, F: func() {
				if _, err := convertBody(protocol.Messages, body, nil); err != nil {
					t.Fatal(err)
				}
			}})
			if allocated := got[name].Bytes; allocated > 50*float64(len(body)) {
				t.Errorf("%v bytes allocated for a body of %d", allocated, len(body))
			}
		})
	}
}

// TestConvertManyItemIDs: reasoning handed back for many Responses items, each with its own ID, is gathered by the ID
// decoded once, so the bytes allocated stay within a small multiple of the body. The measurement runs in a child
// process, so this binary's other goroutines and collections are not counted as the conversion's.
func TestConvertManyItemIDs(t *testing.T) {
	t.Parallel()
	var details []string
	for i := range 20000 {
		details = append(details, fmt.Sprintf(`{"type":"reasoning.summary","format":"openai-responses-v1","id":"rs_%d","summary":"s"},`+
			`{"type":"reasoning.encrypted","format":"openai-responses-v1","id":"rs_%d","data":"e"}`, i, i))
	}
	body := chatBody(`"messages":[` + userHi + `,{"role":"assistant","content":"x","reasoning_details":[` + strings.Join(details, ",") + `]},` + userHi + `]`)
	got, err := convertBody(protocol.Responses, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	measured := alloctest.Measure(t, alloctest.Function{Name: "items", F: func() {
		if _, err := convertBody(protocol.Responses, body, nil); err != nil {
			t.Fatal(err)
		}
	}})
	if allocated := measured["items"].Bytes; allocated > 50*float64(len(body)) {
		t.Errorf("%v bytes allocated for a body of %d", allocated, len(body))
	}
	if n := strings.Count(string(got), `"type":"reasoning"`); n != 20000 {
		t.Errorf("%d reasoning items, want 20000", n)
	}
}

// TestConvertCacheTTL: the usage context tells the relay of a 1-hour cache write only for a Messages endpoint, the one
// format that keeps the TTL.
func TestConvertCacheTTL(t *testing.T) {
	t.Parallel()
	body := []byte(chatBody(`"messages":[{"role":"user","content":[{"type":"text","text":"Hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]`))
	r := inspected(t, protocol.Chat, body)
	for _, f := range []protocol.Format{protocol.Messages, protocol.Responses} {
		u, err := protocol.Prepare(&r, body, protocol.Target{Format: f, Model: "up", MaxOutput: 1}, protocol.Rewrites{Identity: derive})
		if err != nil || u.Usage.CacheTTL1h != (f == protocol.Messages) {
			t.Errorf("format %d: %v, 1-hour TTL %t", f, err, u.Usage.CacheTTL1h)
		}
	}
}

// BenchmarkConvert converts a conversation of n turns, each a call, its result and the reasoning streamed back in
// pieces, for each endpoint format.
func BenchmarkConvert(b *testing.B) {
	for _, turns := range []int{4, 64} {
		body := longConversation(turns)
		r, err := protocol.Inspect(protocol.Chat, body)
		if err != nil {
			b.Fatal(err)
		}
		for _, f := range []protocol.Format{protocol.Messages, protocol.Responses} {
			b.Run(fmt.Sprintf("format=%d/turns=%d", f, turns), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					if _, err := protocol.Prepare(&r, body, protocol.Target{Format: f, Model: "up", MaxOutput: 4096}, protocol.Rewrites{Identity: derive}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// longConversation is a Chat request of n turns, each a call, its result and the reasoning streamed back in 21 pieces.
func longConversation(turns int) []byte {
	var history []string
	for i := range turns {
		var details []string
		for j := range 20 {
			details = append(details, fmt.Sprintf(`{"type":"reasoning.text","format":"anthropic-claude-v1","text":"step %d","index":0}`, j))
		}
		details = append(details, `{"type":"reasoning.text","format":"anthropic-claude-v1","text":"","signature":"sig","index":0}`)
		history = append(history, userHi, fmt.Sprintf(`{"role":"assistant","content":"ok","reasoning_details":[%s],`+
			`"tool_calls":[{"id":"call_%d","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},`+
			`{"role":"tool","tool_call_id":"call_%d","content":"done"}`, strings.Join(details, ","), i, i))
	}
	return []byte(chatBody(`"messages":[` + strings.Join(history, ",") + `,` + userHi + `],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]`))
}
