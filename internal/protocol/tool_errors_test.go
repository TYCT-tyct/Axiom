package protocol_test

import (
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// TestRelayCountsToolErrors: once an answer ends, its client tool calls are counted as checked and those whose
// arguments are not one JSON text as errors, natively and through every conversion, however the arguments were split
// across events; a call without arguments is no error, and an answer that stopped at its output cap reports neither,
// since its calls were cut rather than ended (docs/domains/quality.md「线上观测」).
func TestRelayCountsToolErrors(t *testing.T) {
	t.Parallel()
	messages := func(stop string, pieces ...string) string {
		s := messageStart(`{"input_tokens":10,"output_tokens":0}`) +
			toolMessageStart
		for _, p := range pieces {
			s += `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + p + `}}` + "\n\n"
		}
		return s + blockStop + `data: {"type":"message_delta","delta":{"stop_reason":"` + stop + `"},"usage":{"output_tokens":5}}` + "\n\n" + messageStop
	}
	chatChunk := func(choices string) string {
		return `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":` + choices + "}\n\n"
	}
	chat := func(finish string, calls ...string) string {
		s := ""
		for _, c := range calls {
			s += chatChunk(`[{"index":0,"delta":{"tool_calls":[` + c + `]}}]`)
		}
		return s + chatChunk(`[{"index":0,"delta":{},"finish_reason":"`+finish+`"}]`) + "data: [DONE]\n\n"
	}
	responses := func(status, args string) string {
		details := ""
		if status == "incomplete" {
			details = `,"incomplete_details":{"reason":"max_output_tokens"}`
		}
		return `event: response.created` + "\n" +
			toolResponseCreated +
			`event: response.output_item.added` + "\n" +
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"f","arguments":""}}` + "\n\n" +
			`event: response.output_item.done` + "\n" +
			`data: {"type":"response.output_item.done","output_index":0,"item":{"arguments":` + args + `,"type":"function_call","id":"fc_1","call_id":"call_1","name":"f"}}` + "\n\n" +
			`event: response.` + status + "\n" +
			`data: {"type":"response.` + status + `","response":{"id":"resp_1","object":"response","model":"m","status":"` + status + `"` + details + `,"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}` + "\n\n"
	}
	call := func(index, args string) string {
		return `{"index":` + index + `,"id":"call_` + index + `","type":"function","function":{"name":"f","arguments":` + args + `}}`
	}
	toChat, fromChat := []protocol.Format{protocol.Chat}, []protocol.Format{protocol.Messages, protocol.Responses}
	for _, tc := range []struct {
		name           string
		upstream       protocol.Format
		clients        []protocol.Format // the formats the stream also reaches converted
		stream         string
		checks, errors int64
	}{
		{"Messages arguments split mid-token", protocol.Messages, toChat, messages("tool_use", `"{\"city\":\"Pa"`, `"ris\",\"days\":1e"`, `"2}"`), 1, 0},
		{"Messages arguments cut short", protocol.Messages, toChat, messages("tool_use", `"{\"city\":\"Par"`), 1, 1},
		{"Messages arguments cut at max_tokens", protocol.Messages, toChat, messages("max_tokens", `"{\"city\":\"Par"`), 0, 0},
		{"Messages call without arguments", protocol.Messages, nil, messages("tool_use"), 1, 0},
		{
			"Chat arguments across deltas, index after function", protocol.Chat, fromChat,
			chat("tool_calls", `{"function":{"name":"f","arguments":"{\"a\":["},"index":0,"id":"call_0","type":"function"}`,
				`{"index":0,"function":{"arguments":"1,2]}"}}`), 1, 0,
		},
		// Messages needs the arguments as an object, so that conversion fails the stream rather than pass them on; the
		// gateway tells quality of that failure instead (TestToolErrorsReachQuality).
		{"one of two Chat calls is not JSON", protocol.Chat, fromChat[1:], chat("tool_calls", call("0", `"{\"a\":1}"`)+","+call("1", `"{a:1}"`)), 2, 1},
		{"a Chat call with text after its object", protocol.Chat, nil, chat("tool_calls", call("0", `"{} {}"`)), 1, 1},
		{"a Chat call with empty arguments", protocol.Chat, nil, chat("tool_calls", call("0", `""`)), 1, 0},
		{"Chat arguments cut at the length limit", protocol.Chat, fromChat, chat("length", call("0", `"{\"a\":"`)), 0, 0},
		{"Responses arguments before the item's type", protocol.Responses, toChat, responses("completed", `"{\"q\":\"\\u00e9\"}"`), 1, 0},
		{"Responses arguments cut short", protocol.Responses, toChat, responses("completed", `"{\"q\":"`), 1, 1},
		{"Responses arguments of an incomplete answer", protocol.Responses, toChat, responses("incomplete", `"{\"q\":"`), 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			toolErrorsAre(t, tc.upstream, tc.upstream, tc.stream, tc.checks, tc.errors)
			for _, client := range tc.clients {
				toolErrorsAre(t, client, tc.upstream, tc.stream, tc.checks, tc.errors)
			}
		})
	}
}

// toolErrorsAre relays stream from upstream to a client of format client and checks the calls it checked and the
// errors it counted.
func toolErrorsAre(t *testing.T, client, upstream protocol.Format, stream string, checks, errors int64) {
	t.Helper()
	got := relayTo(t, client, upstream, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
	if got.err != nil {
		t.Fatalf("relay from %v to %v: %v", upstream, client, got.err)
	}
	if p := got.r.Progress(); p.ToolChecks != checks || p.ToolErrors != errors {
		t.Errorf("from %v to %v: %d checked, %d errors; want %d, %d", upstream, client, p.ToolChecks, p.ToolErrors, checks, errors)
	}
}
