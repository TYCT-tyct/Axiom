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
	t.Parallel()
	messagesTool := messageStart(`{"input_tokens":10,"output_tokens":0}`) +
		toolMessageStart +
		blockStop + messageDelta(`{}`) + messageStop
	responsesTool := `event: response.created` + "\n" +
		toolResponseCreated +
		`event: response.output_item.added` + "\n" +
		toolResponseAdded +
		`event: response.completed` + "\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"m","status":"completed"}}` + "\n\n"
	chatTool := toolChatStart +
		toolChatStop +
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
