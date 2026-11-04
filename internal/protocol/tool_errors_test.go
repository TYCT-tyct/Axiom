package protocol_test

import (
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// TestRelayCountsToolErrors: a client tool call whose arguments, once the call ended, are not one JSON text counts as a
// tool error, natively and through every conversion, however the arguments were split across events; a call without
// arguments does not (docs/domains/quality.md「线上观测」).
func TestRelayCountsToolErrors(t *testing.T) {
	t.Parallel()
	messages := func(pieces ...string) string {
		s := messageStart(`{"input_tokens":10,"output_tokens":0}`) +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}` + "\n\n"
		for _, p := range pieces {
			s += `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + p + `}}` + "\n\n"
		}
		return s + blockStop + messageDelta(`{"output_tokens":5}`) + messageStop
	}
	chatChunk := func(choices string) string {
		return `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":` + choices + "}\n\n"
	}
	chat := func(calls ...string) string {
		s := ""
		for _, c := range calls {
			s += chatChunk(`[{"index":0,"delta":{"tool_calls":[` + c + `]}}]`)
		}
		return s + chatChunk(`[{"index":0,"delta":{},"finish_reason":"tool_calls"}]`) + "data: [DONE]\n\n"
	}
	responses := func(args string) string {
		return `event: response.created` + "\n" +
			`data: {"type":"response.created","response":{"id":"resp_1","object":"response","model":"m","status":"in_progress"}}` + "\n\n" +
			`event: response.output_item.added` + "\n" +
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"f","arguments":""}}` + "\n\n" +
			`event: response.output_item.done` + "\n" +
			`data: {"type":"response.output_item.done","output_index":0,"item":{"arguments":` + args + `,"type":"function_call","id":"fc_1","call_id":"call_1","name":"f"}}` + "\n\n" +
			`event: response.completed` + "\n" +
			`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"m","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}` + "\n\n"
	}
	call := func(index, args string) string {
		return `{"index":` + index + `,"id":"call_` + index + `","type":"function","function":{"name":"f","arguments":` + args + `}}`
	}
	for _, tc := range []struct {
		name     string
		upstream protocol.Format
		clients  []protocol.Format // the formats the stream also reaches converted
		stream   string
		want     int64
	}{
		{
			"Messages arguments split mid-token", protocol.Messages,
			[]protocol.Format{protocol.Chat},
			messages(`"{\"city\":\"Pa"`, `"ris\",\"days\":1e"`, `"2}"`), 0,
		},
		{
			"Messages arguments cut short", protocol.Messages,
			[]protocol.Format{protocol.Chat},
			messages(`"{\"city\":\"Par"`), 1,
		},
		{"Messages call without arguments", protocol.Messages, nil, messages(), 0},
		{
			"Chat arguments across deltas, index after function", protocol.Chat,
			[]protocol.Format{protocol.Messages, protocol.Responses},
			chat(`{"function":{"name":"f","arguments":"{\"a\":["},"index":0,"id":"call_0","type":"function"}`,
				`{"index":0,"function":{"arguments":"1,2]}"}}`), 0,
		},
		// Messages needs the arguments as an object, so that conversion fails the stream rather than pass them on.
		{
			"one of two Chat calls is not JSON", protocol.Chat,
			[]protocol.Format{protocol.Responses},
			chat(call("0", `"{\"a\":1}"`) + "," + call("1", `"{a:1}"`)), 1,
		},
		{"a Chat call with text after its object", protocol.Chat, nil, chat(call("0", `"{} {}"`)), 1},
		{"a Chat call with empty arguments", protocol.Chat, nil, chat(call("0", `""`)), 0},
		{
			"Responses arguments before the item's type", protocol.Responses,
			[]protocol.Format{protocol.Chat},
			responses(`"{\"q\":\"\\u00e9\"}"`), 0,
		},
		{"Responses arguments cut short", protocol.Responses, []protocol.Format{protocol.Chat}, responses(`"{\"q\":"`), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			toolErrorsAre(t, tc.upstream, tc.upstream, tc.stream, tc.want)
			for _, client := range tc.clients {
				toolErrorsAre(t, client, tc.upstream, tc.stream, tc.want)
			}
		})
	}
}

// toolErrorsAre relays stream from upstream to a client of format client and checks the tool errors it counted.
func toolErrorsAre(t *testing.T, client, upstream protocol.Format, stream string, want int64) {
	t.Helper()
	got := relayTo(t, client, upstream, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
	if got.err != nil {
		t.Fatalf("relay from %v to %v: %v", upstream, client, got.err)
	}
	if n := got.r.Progress().ToolErrors; n != want {
		t.Errorf("tool errors %d from %v to %v, want %d", n, upstream, client, want)
	}
}
