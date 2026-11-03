package protocol_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// TestConvertRoundTrip: a reply converted for a Chat client, gathered the way a client gathers a stream and handed
// back in the next request, reaches the upstream as the upstream gave it (I2): Messages' leading thinking with its
// signature and its tool calls with their IDs and input; Responses' reasoning item with its summary and encrypted
// content and its function call with its call_id and arguments.
func TestConvertRoundTrip(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		stream  string
		from    protocol.Format
		history string // the member of the converted request that holds the conversation
		want    func(t *testing.T, upstream []byte) []map[string]any
	}{
		{"messages/thinking/upstream.sse", protocol.Messages, "messages", messagesBlocks},
		{"messages/tool-use/upstream.sse", protocol.Messages, "messages", messagesBlocks},
		{"assembled/responses-reasoning-tool.sse", protocol.Responses, "input", responsesItems},
	} {
		t.Run(c.stream, func(t *testing.T) {
			upstream := sample(t, c.stream)
			got := relayTo(t, protocol.Chat, c.from, bytes.NewReader(upstream), protocol.UsageContext{},
				protocol.NewMemory(0), settled)
			if got.err != nil {
				t.Fatal(got.err)
			}
			body, err := convertBody(c.from, nextRequest(t, gather(t, got.out)), nil)
			if err != nil {
				t.Fatal(err)
			}
			history := flatten(member(t, body, c.history))
			for _, w := range c.want(t, upstream) {
				if !containsSubset(history, w) {
					t.Errorf("the converted request lacks %v:\n%s", w, body)
				}
			}
		})
	}
}

// member is the member name of the JSON object body.
func member(t *testing.T, body []byte, name string) any {
	t.Helper()
	var o map[string]any
	if err := jsonx.Unmarshal(body, &o); err != nil {
		t.Fatal(err)
	}
	return o[name]
}

// gather is the assistant message a client makes of a Chat stream: the content and reasoning joined, the reasoning
// details of every chunk one after another, as OpenRouter says clients build them, and each tool call's members
// gathered by its index.
func gather(t *testing.T, stream []byte) map[string]any {
	t.Helper()
	var content strings.Builder
	var details []any
	var calls []map[string]any
	for _, e := range events(t, bytes.TrimSuffix(stream, []byte("data: [DONE]\n\n"))) {
		choices, _ := e["choices"].([]any)
		if len(choices) == 0 || e["usage"] != nil {
			continue
		}
		delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
		s, _ := delta["content"].(string)
		content.WriteString(s)
		more, _ := delta["reasoning_details"].([]any)
		details = append(details, more...)
		toolCalls, _ := delta["tool_calls"].([]any)
		for _, c := range toolCalls {
			calls = gatherCall(calls, c.(map[string]any))
		}
	}
	msg := map[string]any{"role": "assistant", "content": content.String(), "reasoning_details": details}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	return msg
}

func gatherCall(calls []map[string]any, c map[string]any) []map[string]any {
	i := int(c["index"].(float64))
	if i == len(calls) {
		calls = append(calls, map[string]any{
			"id": c["id"], "type": "function",
			"function": map[string]any{"name": c["function"].(map[string]any)["name"], "arguments": ""},
		})
	}
	f := calls[i]["function"].(map[string]any)
	args, _ := c["function"].(map[string]any)["arguments"].(string)
	f["arguments"] = f["arguments"].(string) + args
	return calls
}

// nextRequest is a Chat request that hands msg back, with a result for each of its tool calls.
func nextRequest(t *testing.T, msg map[string]any) string {
	t.Helper()
	messages := []any{map[string]any{"role": "user", "content": "q"}, msg}
	calls, _ := msg["tool_calls"].([]map[string]any)
	for _, c := range calls {
		messages = append(messages, map[string]any{"role": "tool", "tool_call_id": c["id"], "content": "r"})
	}
	messages = append(messages, map[string]any{"role": "user", "content": "next"})
	body, err := jsonx.MarshalCanonical(map[string]any{"model": "m", "messages": messages})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// messagesBlocks are the blocks the upstream's Messages stream gave that a replay must bring back: each thinking
// block before the first block of another kind, with its signature, and each tool_use with its input.
func messagesBlocks(t *testing.T, upstream []byte) []map[string]any {
	t.Helper()
	var want []map[string]any
	leading := true
	for _, block := range gatherBlocks(t, upstream) {
		leading = leading && block["type"] == "thinking"
		switch {
		case block["type"] == "thinking" && leading:
			delete(block, "partial_json")
			want = append(want, block)
		case block["type"] == "tool_use":
			var v any
			if err := jsonx.Unmarshal([]byte(block["partial_json"].(string)), &v); err != nil {
				t.Fatal(err)
			}
			want = append(want, map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": v})
		}
	}
	return want
}

// gatherBlocks are a Messages stream's blocks, each with its deltas' thinking, signature and partial_json joined.
func gatherBlocks(t *testing.T, upstream []byte) []map[string]any {
	t.Helper()
	var blocks []map[string]any
	for _, e := range events(t, upstream) {
		switch e["type"] {
		case "content_block_start":
			block := e["content_block"].(map[string]any)
			block["partial_json"] = ""
			blocks = append(blocks, block)
		case "content_block_delta":
			d := e["delta"].(map[string]any)
			for _, f := range []string{"thinking", "signature", "partial_json"} {
				if s, ok := d[f].(string); ok {
					block := blocks[len(blocks)-1]
					block[f] = block[f].(string) + s
				}
			}
		}
	}
	return blocks
}

// responsesItems are the reasoning and function call items of the upstream's Responses stream, as its last snapshot
// gives them, with the members a replay brings back.
func responsesItems(t *testing.T, upstream []byte) []map[string]any {
	t.Helper()
	var want []map[string]any
	for _, e := range events(t, upstream) {
		if e["type"] != "response.completed" {
			continue
		}
		output, _ := e["response"].(map[string]any)["output"].([]any)
		for _, item := range output {
			it := item.(map[string]any)
			switch it["type"] {
			case "reasoning":
				want = append(want, it)
			case "function_call":
				want = append(want, map[string]any{
					"type": "function_call", "call_id": it["call_id"], "name": it["name"],
					"arguments": it["arguments"],
				})
			}
		}
	}
	return want
}

// flatten is every object in a request's history, the messages or items and the blocks within them.
func flatten(v any) []map[string]any {
	var out []map[string]any
	list, _ := v.([]any)
	for _, x := range list {
		o, ok := x.(map[string]any)
		if !ok {
			continue
		}
		out = append(append(out, o), flatten(o["content"])...)
	}
	return out
}

// containsSubset reports whether some object in objs has every member of want, equal.
func containsSubset(objs []map[string]any, want map[string]any) bool {
	for _, o := range objs {
		sub := map[string]any{}
		for k := range want {
			sub[k] = o[k]
		}
		if cmp.Equal(sub, want) {
			return true
		}
	}
	return false
}
