package protocol_test

import (
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// TestConvertedStreamsKeepToolCalls: a converter rebuilds the event it passes on, so it must copy the tool-call count
// with the content; otherwise the same Claude reply is estimated 45 tokens lower for a Chat client than for a native
// one. Each stream stops before its final usage, the case where the estimate stands in for the report.
func TestConvertedStreamsKeepToolCalls(t *testing.T) {
	messagesTool := messageStart(`{"input_tokens":10,"output_tokens":0}`) +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}` + "\n\n" +
		blockStop + messageDelta(`{}`) + messageStop
	responsesTool := `event: response.created` + "\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","model":"m","status":"in_progress"}}` + "\n\n" +
		`event: response.output_item.added` + "\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":""}}` + "\n\n" +
		`event: response.completed` + "\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"m","status":"completed"}}` + "\n\n"
	chatTool := `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	for _, tc := range []struct {
		name             string
		client, upstream protocol.Format
		stream           string
	}{
		{"Messages to Chat", protocol.Chat, protocol.Messages, messagesTool},
		{"Responses to Chat", protocol.Chat, protocol.Responses, responsesTool},
		{"Chat to Messages", protocol.Messages, protocol.Chat, chatTool},
		{"Chat to Responses", protocol.Responses, protocol.Chat, chatTool},
	} {
		t.Run(tc.name, func(t *testing.T) {
			converted := relayTo(t, tc.client, tc.upstream, strings.NewReader(tc.stream),
				protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if converted.err != nil {
				t.Fatalf("converted: %v", converted.err)
			}
			native := relay(t, tc.upstream, strings.NewReader(tc.stream),
				protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if native.err != nil {
				t.Fatalf("native: %v", native.err)
			}
			if conv, want := converted.r.Progress().ToolCalls, native.r.Progress().ToolCalls; conv != want || conv != 1 {
				t.Errorf("tool calls %d converted, %d native, want 1", conv, want)
			}
		})
	}
}
