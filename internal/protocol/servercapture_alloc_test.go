package protocol

import (
	"fmt"
	"strings"
	"testing"

	"axiom/internal/platform/alloctest"
)

// allocCase is an event read into a capture that has already read prime: well formed, small as JSON, and built to
// make a decoder that materialises it allocate far more than its length, or to make the capture's own buffers grow.
type allocCase struct {
	format Format
	prime  []string
	event  string
}

func allocCases() map[string]allocCase {
	empties := strings.TrimSuffix(strings.Repeat("{},", 10000), ",")
	members := strings.TrimSuffix(strings.Repeat(`"a":0,`, 10000), ",")
	deep := strings.Repeat("[", 9000) + strings.Repeat("]", 9000)
	long := strings.Repeat("x", 1<<20)
	block := func(i int) string {
		return fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":"x"}}`, i)
	}
	// Primes that leave the capture's block slice and text buffer exactly full, so the measured event makes them grow.
	var blocks []string
	for c := newServerCapture(Messages); len(c.blocks) < 4096 || len(c.blocks) < cap(c.blocks); {
		blocks = append(blocks, block(len(blocks)))
		if err := c.event([]byte(blocks[len(blocks)-1])); err != nil {
			panic(err)
		}
	}
	content := func(text string) string { return `{"choices":[{"index":0,"delta":{"content":"` + text + `"}}]}` }
	texts := []string{content(long)}
	if c := primed(Chat, [][]byte{[]byte(texts[0])}); cap(c.text) > len(c.text) {
		texts = append(texts, content(strings.Repeat("x", cap(c.text)-len(c.text)-2))) // unquote grows by the quoted length
	}
	return map[string]allocCase{
		"messages, foreign choices":  {format: Messages, event: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"x"},"choices":[` + empties + `]}`},
		"messages, foreign members":  {format: Messages, event: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"x",` + members + `}}`},
		"messages, deep nesting":     {format: Messages, event: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"x","n":` + deep + `}}`},
		"messages, deep raw block":   {format: Messages, event: `{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","n":` + deep + `}}`},
		"messages, one more block":   {format: Messages, prime: blocks, event: block(len(blocks))},
		"chat, empty choices":        {format: Chat, event: `{"choices":[` + empties + `]}`},
		"chat, empty tool calls":     {format: Chat, event: `{"choices":[{"index":0,"delta":{"tool_calls":[` + empties + `]}}]}`},
		"chat, empty details":        {format: Chat, event: `{"choices":[{"index":0,"delta":{"reasoning_details":[` + empties + `]}}]}`},
		"chat, a long text":          {format: Chat, event: content(long)},
		"chat, text past a long one": {format: Chat, prime: texts, event: content("x")},
		"responses, foreign arrays":  {format: Responses, event: `{"type":"response.output_item.added","output_index":0,"item":{"type":"message"},"choices":[` + empties + `]}`},
		"responses, deep item":       {format: Responses, event: `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","name":"openrouter_web_search","x":` + deep + `}}`},
	}
}

// primed is a capture of f that has read prime.
func primed(f Format, prime [][]byte) *serverCapture {
	c := newServerCapture(f)
	for _, p := range prime {
		if err := c.event(p); err != nil {
			panic(err) // a prime is well formed and under every bound
		}
	}
	return c
}

// TestCaptureAllocatesWithinItsCharge: what reading one event allocates is never more than growthBound, which the relay
// draws from the node's budget before the read (captureEvent). Each case is measured twice, priming alone and priming
// then the event, so the difference is the event's own allocation.
func TestCaptureAllocatesWithinItsCharge(t *testing.T) {
	t.Parallel()
	cases := allocCases()
	var funcs []alloctest.Function
	for name, tc := range cases {
		prime := make([][]byte, len(tc.prime))
		for i, p := range tc.prime {
			prime[i] = []byte(p)
		}
		event := []byte(tc.event)
		funcs = append(funcs,
			alloctest.Function{Name: name + "/prime", F: func() { primed(tc.format, prime) }},
			alloctest.Function{Name: name, F: func() {
				if err := primed(tc.format, prime).event(event); err != nil {
					panic(err)
				}
			}})
	}
	got := alloctest.Measure(t, funcs...)
	for name, tc := range cases {
		prime := make([][]byte, len(tc.prime))
		for i, p := range tc.prime {
			prime[i] = []byte(p)
		}
		drawn := primed(tc.format, prime).growthBound(len(tc.event))
		allocated := got[name].Bytes - got[name+"/prime"].Bytes
		t.Logf("%s: %.0f bytes for an event of %d, drawn %d", name, allocated, len(tc.event), drawn)
		if allocated > float64(drawn) {
			t.Errorf("%s: reading the event allocated %.0f bytes, drawn %d", name, allocated, drawn)
		}
	}
}

// TestTurnAllocatesWithinItsCharge: building the turn, which merges every reasoning_details entry the capture kept,
// allocates no more than turnBound, which ServerTurn draws before it builds.
func TestTurnAllocatesWithinItsCharge(t *testing.T) {
	t.Parallel()
	empties := strings.TrimSuffix(strings.Repeat(`{"type":"reasoning.text"},`, 10000), ",")
	events := map[string]string{
		"many details":    `{"choices":[{"index":0,"delta":{"reasoning_details":[` + empties + `]}}]}`,
		"escaped text":    `{"choices":[{"index":0,"delta":{"content":"` + strings.Repeat(`\u0001`, 10000) + `"}}]}`,
		"a call, no text": `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"openrouter_web_search","arguments":"{}"}}]}}]}`,
	}
	var funcs []alloctest.Function
	for name, ev := range events {
		event := []byte(ev)
		funcs = append(funcs,
			alloctest.Function{Name: name + "/read", F: func() { primed(Chat, [][]byte{event}) }},
			alloctest.Function{Name: name, F: func() { _ = primed(Chat, [][]byte{event}).turn() }})
	}
	got := alloctest.Measure(t, funcs...)
	for name, ev := range events {
		drawn := primed(Chat, [][]byte{[]byte(ev)}).turnBound()
		allocated := got[name].Bytes - got[name+"/read"].Bytes
		t.Logf("%s: the turn allocated %.0f, drawn %d", name, allocated, drawn)
		if allocated > float64(drawn) {
			t.Errorf("%s: building the turn allocated %.0f bytes, drawn %d", name, allocated, drawn)
		}
	}
}

// TestRequestScansAllocateOnlyTheCopy: the loop's request rewrites walk the client's tools and messages where they lie.
// A body of a hundred thousand empty objects costs each at most the copy of the body it returns, and the check for a
// colliding function nothing that grows with it; decoding the arrays would take tens of bytes an element.
func TestRequestScansAllocateOnlyTheCopy(t *testing.T) {
	t.Parallel()
	empties := strings.TrimSuffix(strings.Repeat("{},", 100000), ",")
	tools := []byte(`{"model":"m","messages":[{"role":"user","content":"q"}],"tools":[` + empties + `,{"type":"openrouter:web_search"}]}`)
	plain := []byte(`{"model":"m","messages":[{"role":"user","content":"q"}],"tools":[` + empties + `]}`)
	history := []byte(`{"model":"m","messages":[` + empties + `]}`)
	turn := ServerTurn{Calls: []ServerToolCall{{ID: "c1", Arguments: `{"query":"q"}`}}}
	const slack = 16 << 10 // the decoders and the scratch for names, once a call
	limits := map[string]int{
		"remove":   allocated(len(tools)) + slack,
		"declared": slack,
		"inject":   allocated(len(plain)+1<<10) + slack,
		"append":   allocated(len(history)+1<<10) + slack,
	}
	got := alloctest.Measure(t,
		alloctest.Function{Name: "remove", F: func() {
			if _, err := RemoveServerTool(Chat, tools); err != nil {
				panic(err)
			}
		}},
		alloctest.Function{Name: "declared", F: func() { ServerFunctionDeclared(Chat, plain) }},
		alloctest.Function{Name: "inject", F: func() {
			if _, err := injectServerFunction(Chat, plain); err != nil {
				panic(err)
			}
		}},
		alloctest.Function{Name: "append", F: func() {
			if _, err := AppendServerTurn(Chat, history, turn, []string{"result"}); err != nil {
				panic(err)
			}
		}},
	)
	for name, limit := range limits {
		t.Logf("%s: %.0f bytes, limit %d", name, got[name].Bytes, limit)
		if got[name].Bytes > float64(limit) {
			t.Errorf("%s allocated %.0f bytes for a body of a hundred thousand elements, want at most %d", name, got[name].Bytes, limit)
		}
	}
}
