package protocol_test

import (
	"fmt"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// TestRelayCountsToolCalls: the output estimate adds a framework cost per client tool call, so the relay counts the
// call once per format even though Chat and Responses repeat pieces of it across events.
func TestRelayCountsToolCalls(t *testing.T) {
	messagesTool := messageStart(`{"input_tokens":10,"output_tokens":0}`) +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}` + "\n\n" +
		blockStop + messageDelta(`{"output_tokens":5}`) + messageStop
	messagesServerTool := messageStart(`{"input_tokens":10,"output_tokens":0}`) +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{}}}` + "\n\n" +
		blockStop + messageDelta(`{"output_tokens":1}`) + messageStop
	chatTool := `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Paris\"}"}}]}}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n" +
		"data: [DONE]\n\n"
	chatTwoTools := `data: {"id":"c2","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"a","arguments":""}},{"index":1,"id":"call_2","type":"function","function":{"name":"b","arguments":""}}]}}]}` + "\n\n" +
		`data: {"id":"c2","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		`data: {"id":"c2","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n" +
		"data: [DONE]\n\n"
	chatTwoChoices := `data: {"id":"c3","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"a","arguments":""}}]}},{"index":1,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"b","arguments":""}}]}}]}` + "\n\n" +
		`data: {"id":"c3","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"},{"index":1,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	chatIndexAfterDelta := `data: {"id":"c4","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"a","arguments":""}},{"index":1,"id":"call_2","type":"function","function":{"name":"b","arguments":""}}]},"index":0}]}` + "\n\n" +
		`data: {"id":"c4","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	var many strings.Builder
	many.WriteString(`data: {"id":"c5","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[`)
	for i := range 65 {
		if i > 0 {
			many.WriteByte(',')
		}
		fmt.Fprintf(&many, `{"index":%d,"id":"call_%d","type":"function","function":{"name":"f","arguments":""}}`, i, i)
	}
	many.WriteString(`]}}]}` + "\n\n" +
		`data: {"id":"c5","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n")
	chatManyTools := many.String()
	responsesTool := `event: response.created` + "\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","model":"m","status":"in_progress"}}` + "\n\n" +
		`event: response.output_item.added` + "\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":""}}` + "\n\n" +
		`event: response.function_call_arguments.delta` + "\n" +
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"city\":\"Paris\"}"}` + "\n\n" +
		`event: response.output_item.done` + "\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}` + "\n\n" +
		`event: response.completed` + "\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"m","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}` + "\n\n"
	for _, tc := range []struct {
		name   string
		format protocol.Format
		stream string
		want   int64
	}{
		{"a Messages client tool", protocol.Messages, messagesTool, 1},
		{"a Messages server tool", protocol.Messages, messagesServerTool, 0},
		{"a Chat tool across deltas", protocol.Chat, chatTool, 1},
		{"two Chat tools in one delta", protocol.Chat, chatTwoTools, 2},
		{"two Chat choices sharing a tool index", protocol.Chat, chatTwoChoices, 2},
		{"a Chat choice index after the delta", protocol.Chat, chatIndexAfterDelta, 2},
		{"65 Chat tools in one choice", protocol.Chat, chatManyTools, 65},
		{"a Responses function call", protocol.Responses, responsesTool, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := relay(t, tc.format, strings.NewReader(tc.stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if got.err != nil {
				t.Fatalf("relay: %v", got.err)
			}
			if n := got.r.Progress().ToolCalls; n != tc.want {
				t.Errorf("tool calls %d, want %d", n, tc.want)
			}
		})
	}
}
