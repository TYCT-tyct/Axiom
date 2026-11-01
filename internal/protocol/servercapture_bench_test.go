package protocol

import (
	"fmt"
	"testing"
)

// BenchmarkServerCapture reads a typical tool hop in each client format, a reply streamed in a hundred chunks of text
// or thinking and a web search call, and builds the turn the next hop replays.
func BenchmarkServerCapture(b *testing.B) {
	chunk := "Searching for the latest figures on the gateway's release, "
	hops := map[Format][][]byte{}
	for range 100 {
		hops[Chat] = append(hops[Chat], fmt.Appendf(nil, `{"choices":[{"index":0,"delta":{"content":%q,"reasoning_details":[{"type":"reasoning.text","index":0,"text":%q}]}}]}`, chunk, chunk))
		hops[Responses] = append(hops[Responses], fmt.Appendf(nil, `{"type":"response.function_call_arguments.delta","output_index":1,"delta":%q}`, "q"))
	}
	hops[Chat] = append(hops[Chat], []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"openrouter_web_search","arguments":"{\"query\":\"axiom\"}"}}]}}]}`))
	hops[Messages] = append(hops[Messages], []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`))
	for range 100 {
		hops[Messages] = append(hops[Messages], fmt.Appendf(nil, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":%q}}`, chunk))
	}
	hops[Messages] = append(hops[Messages],
		[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed"}}`),
		[]byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"c1","name":"openrouter_web_search","input":{}}}`),
		[]byte(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"axiom\"}"}}`))
	hops[Responses] = append([][]byte{[]byte(`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"c1","name":"openrouter_web_search","arguments":""}}`)}, hops[Responses]...)
	for name, f := range map[string]Format{"chat": Chat, "messages": Messages, "responses": Responses} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				c := newServerCapture(f)
				for _, ev := range hops[f] {
					if err := c.event(ev); err != nil {
						b.Fatal(err)
					}
				}
				_ = c.turn()
			}
		})
	}
}
