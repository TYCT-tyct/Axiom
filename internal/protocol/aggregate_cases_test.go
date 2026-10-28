package protocol_test

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/protocol"
)

// aggregateAll aggregates stream for a client of format f whose upstream speaks f, settled, and returns the decoded
// body.
func aggregateAll(t *testing.T, f protocol.Format, stream string) map[string]any {
	t.Helper()
	got := aggregate(t, f, f, strings.NewReader(stream), protocol.NewMemory(0), unsettled)
	if got.err != nil || got.status != http.StatusOK {
		t.Fatalf("error %v, status %d:\n%s", got.err, got.status, got.body)
	}
	checkShape(t, f, got.body)
	return decode(t, got.body)
}

func input(i int, partial string) string {
	return deltaOf(i, `{"type":"input_json_delta","partial_json":"`+partial+`"}`)
}

// TestAggregateMessages: every kind of block and delta makes the block the official reply has: strings in their
// escapes, a tool's input from its pieces, or as the block began when no piece came, or an empty object; unknown
// events and pings change nothing; the usage of message_start and the later usages are one object.
func TestAggregateMessages(t *testing.T) {
	stream := messageStart(`{"input_tokens":3,"cache_read_input_tokens":0,"output_tokens":1}`) + ping +
		startBlock(0, `{"type":"thinking","thinking":"","signature":""}`) +
		deltaOf(0, `{"type":"thinking_delta","thinking":"a\n"}`) + deltaOf(0, `{"type":"thinking_delta","thinking":"b\"c"}`) +
		deltaOf(0, `{"type":"signature_delta","signature":"SIG"}`) + stopBlock(0) +
		startBlock(1, `{"type":"text","text":"","citations":null}`) +
		deltaOf(1, `{"type":"text_delta","text":"Hel"}`) +
		deltaOf(1, `{"type":"citations_delta","citation":{"type":"char_location","cited_text":"x"}}`) +
		deltaOf(1, `{"type":"text_delta","text":"lo \u00e9\ud83d\ude00"}`) + stopBlock(1) + ev(`{"type":"unknown"}`) + ping +
		startBlock(2, `{"type":"tool_use","id":"toolu_1","name":"f","input":{}}`) + input(2, "") + input(2, `{\"a\":`) +
		input(2, `[1,\"\\u00e9\"]}`) + stopBlock(2) +
		startBlock(3, `{"type":"tool_use","id":"toolu_2","name":"g","input":{"x": 1}}`) + input(3, "") + stopBlock(3) +
		startBlock(4, `{"type":"tool_use","id":"toolu_3","name":"h"}`) + stopBlock(4) +
		startBlock(5, `{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}`) +
		input(5, `{\"query\":\"q\"}`) + stopBlock(5) +
		startBlock(6, `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"u"}]}`) +
		stopBlock(6) + startBlock(7, `{"type":"redacted_thinking","data":"R"}`) + stopBlock(7) +
		ev(`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":9}}`) + messageStop
	want := map[string]any{
		"id": "gen-01", "type": "message", "role": "assistant", "model": "claude-client-name",
		"stop_reason": "tool_use", "stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens": 3.0, "cache_read_input_tokens": 0.0, "output_tokens": 9.0, "cost": 3.703701, "cost_currency": "USD",
		},
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "a\nb\"c", "signature": "SIG"},
			map[string]any{
				"type": "text", "text": "Hello \u00e9\U0001F600",
				"citations": []any{map[string]any{"type": "char_location", "cited_text": "x"}},
			},
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "f", "input": map[string]any{"a": []any{1.0, "\u00e9"}}},
			map[string]any{"type": "tool_use", "id": "toolu_2", "name": "g", "input": map[string]any{"x": 1.0}},
			map[string]any{"type": "tool_use", "id": "toolu_3", "name": "h", "input": map[string]any{}},
			map[string]any{"type": "server_tool_use", "id": "srvtoolu_1", "name": "web_search", "input": map[string]any{"query": "q"}},
			map[string]any{
				"type": "web_search_tool_result", "tool_use_id": "srvtoolu_1",
				"content": []any{map[string]any{"type": "web_search_result", "url": "u"}},
			},
			map[string]any{"type": "redacted_thinking", "data": "R"},
		},
	}
	got := aggregate(t, protocol.Messages, protocol.Messages, strings.NewReader(stream), protocol.NewMemory(0), unsettled)
	if got.err != nil {
		t.Fatal(got.err)
	}
	checkShape(t, protocol.Messages, got.body)
	if diff := cmp.Diff(want, decode(t, got.body)); diff != "" {
		t.Errorf("message (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(foldStream(t, protocol.Messages, relay(t, protocol.Messages, strings.NewReader(stream),
		protocol.UsageContext{}, protocol.NewMemory(0), settled).out), want); diff != "" {
		t.Errorf("the reference fold disagrees (-fold +want):\n%s", diff)
	}
}

// TestAggregateMessagesFraming: an event of several data lines, which the stream reader joins with line feeds.
func TestAggregateMessagesFraming(t *testing.T) {
	stream := messageStart(`{"input_tokens":3}`) + startBlock(0, `{"type":"text","text":""}`) +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\n" +
		"data: \"delta\":{\"type\":\"text_delta\",\n" +
		"data: \"text\":\"Hi\"}}\n\n" + stopBlock(0) + messageDelta(`{"output_tokens":2}`) + messageStop
	body := aggregateAll(t, protocol.Messages, stream)
	if content := body["content"].([]any)[0].(map[string]any); content["text"] != "Hi" {
		t.Errorf("body %v", body)
	}
}

// TestAggregatePending: a reply whose settlement was not written is a 200 without a cost and with the flag in axiom,
// in each format, as the stream's ending has it.
func TestAggregatePending(t *testing.T) {
	for _, c := range []struct {
		sample string
		path   int // in aggregatePaths
	}{
		{"messages/text", 0}, {"responses/text", 1}, {"chat/text", 2}, {"messages/text", 3}, {"responses/text", 4},
	} {
		p := aggregatePaths[c.path]
		t.Run(p.name, func(t *testing.T) {
			o := options
			o.Memory, o.Aggregate = protocol.NewMemory(0), true
			r := protocol.NewRelay(p.client, p.upstream, bytes.NewReader(sample(t, c.sample+"/upstream.sse")), protocol.UsageContext{}, o)
			for step, err := r.Next(); step.Kind != protocol.StepUpstreamDone; step, err = r.Next() {
				if err != nil {
					t.Fatal(err)
				}
			}
			body := decode(t, r.Finish(protocol.Final{Pending: true}))
			if obj(body["axiom"])["billing_pending"] != true || obj(body["usage"])["cost"] != nil || r.Status() != http.StatusOK {
				t.Errorf("body %v, status %d", body, r.Status())
			}
		})
	}
}

// TestAggregateRejects: an upstream that sends what no reply can be made of fails the call as the upstream's, with
// the answer the status table gives, since it has not got as far as content, or Messages' 502 when it has.
func TestAggregateRejects(t *testing.T) {
	start := messageStart(`{"input_tokens":3}`)
	for name, stream := range map[string]string{
		"a delta out of turn":           start + deltaOf(0, `{"type":"text_delta","text":"x"}`),
		"a block out of order":          start + startBlock(1, `{"type":"text","text":""}`),
		"a text delta for a tool":       start + startBlock(0, `{"type":"tool_use","id":"t","name":"f"}`) + deltaOf(0, `{"type":"text_delta","text":"x"}`),
		"a tool input that is not JSON": start + startBlock(0, `{"type":"tool_use","id":"t","name":"f"}`) + input(0, `{\"a\":`) + stopBlock(0),
		"a second message_start":        start + start,
		"message_delta without start":   messageDelta(`{"output_tokens":2}`) + messageStop, // held, so the ending fails
	} {
		t.Run(name, func(t *testing.T) {
			got := aggregate(t, protocol.Messages, protocol.Messages, strings.NewReader(stream), protocol.NewMemory(0), unsettled)
			if errors.Is(got.err, protocol.ErrMemory) || (got.err != nil) != (name != "message_delta without start") {
				t.Errorf("error %v, want the upstream's", got.err)
			}
			if got.status != http.StatusBadGateway || decode(t, got.body)["type"] != "error" {
				t.Errorf("status %d:\n%s", got.status, got.body)
			}
		})
	}
}

func chatChunk(choices string) string { return chatHead + choices + "}\n\n" }

// TestAggregateChat: choices interleave and each keeps its own pieces; a tool call's pieces follow its index; the
// reasoning details of a type and index are one entry, with the signature that came apart from the text; a message
// with tool calls and no text has no content.
func TestAggregateChat(t *testing.T) {
	stream := chatChunk(`[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null},`+
		`{"index":1,"delta":{"role":"assistant","content":""},"finish_reason":null}]`) +
		chatChunk(`[{"index":0,"delta":{"reasoning":"th","reasoning_details":[{"type":"reasoning.text","text":"th","format":"f","index":0}]},"finish_reason":null}]`) +
		chatChunk(`[{"index":1,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get","arguments":""}},`+
			`{"index":1,"id":"call_2","type":"function","function":{"name":"put","arguments":"{\"a\""}}]},"finish_reason":null}]`) +
		chatChunk(`[{"index":0,"delta":{"content":"a\"b","reasoning_details":[{"type":"reasoning.text","text":"ink","index":0}]},`+
			`"logprobs":{"content":[{"token":"a"}],"refusal":null},"finish_reason":null}]`) +
		chatChunk(`[{"index":1,"delta":{"tool_calls":[{"index":1,"function":{"arguments":":1}"}},{"index":0,"function":{"arguments":"{}"}}]},"finish_reason":null}]`) +
		chatChunk(`[{"index":0,"delta":{"content":"c","refusal":"no","annotations":[{"type":"url_citation","url_citation":{"url":"u"}}],`+
			`"reasoning_details":[{"type":"reasoning.text","text":"","signature":"S","index":0},`+
			`{"type":"reasoning.encrypted","data":"E","id":"rs_1","format":"g","index":1}]},"finish_reason":"stop"}]`) +
		chatChunk(`[{"index":1,"delta":{},"finish_reason":"tool_calls"}]`) + chatDone
	got := aggregateAll(t, protocol.Chat, stream)
	want := map[string]any{
		"id": "gen-01", "object": "chat.completion", "created": 7.0, "model": "claude-client-name",
		"usage": got["usage"],
		"choices": []any{
			map[string]any{
				"index": 0.0, "logprobs": map[string]any{"content": []any{map[string]any{"token": "a"}}, "refusal": nil},
				"finish_reason": "stop",
				"message": map[string]any{
					"role": "assistant", "content": "a\"bc", "refusal": "no", "reasoning": "th", "reasoning_content": "th",
					"annotations": []any{map[string]any{"type": "url_citation", "url_citation": map[string]any{"url": "u"}}},
					"reasoning_details": []any{
						map[string]any{"type": "reasoning.text", "text": "think", "signature": "S", "format": "f", "index": 0.0},
						map[string]any{"type": "reasoning.encrypted", "data": "E", "id": "rs_1", "format": "g", "index": 1.0},
					},
				},
			},
			map[string]any{
				"index": 1.0, "logprobs": nil, "finish_reason": "tool_calls",
				"message": map[string]any{
					"role": "assistant", "content": nil, "refusal": nil,
					"tool_calls": []any{
						map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "get", "arguments": "{}"}},
						map[string]any{"id": "call_2", "type": "function", "function": map[string]any{"name": "put", "arguments": `{"a":1}`}},
					},
				},
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("completion (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(foldStream(t, protocol.Chat, relay(t, protocol.Chat, strings.NewReader(stream),
		protocol.UsageContext{}, protocol.NewMemory(0), settled).out), got); diff != "" {
		t.Errorf("the reference fold disagrees (-fold +got):\n%s", diff)
	}
}

// TestAggregateChatFail: a choice the failure cut short finishes with the error, though the error chunk names only the
// first.
func TestAggregateChatFail(t *testing.T) {
	stream := chatChunk(`[{"index":0,"delta":{"content":"a"},"finish_reason":null},{"index":1,"delta":{"content":"b"},"finish_reason":null}]`) +
		chatChunk(`[{"index":1,"delta":{"content":"c"},"finish_reason":"stop"}]`)
	got := aggregate(t, protocol.Chat, protocol.Chat, strings.NewReader(stream), protocol.NewMemory(0), unsettled)
	choices := objects(decode(t, got.body)["choices"])
	if got.status != http.StatusOK || len(choices) != 2 || choices[0]["finish_reason"] != "error" || choices[1]["finish_reason"] != "error" ||
		obj(choices[1]["message"])["content"] != "bc" {
		t.Errorf("status %d:\n%s", got.status, got.body)
	}
}

// TestAggregateResponsesFail: the response of a reply that failed holds the items that were done as they were, and
// the ones still open as far as they got: a reasoning item whose summary part has some text, a message with a part
// that has text and an annotation, a function call with the arguments so far, not mended; an open item is incomplete.
func TestAggregateResponsesFail(t *testing.T) {
	item := func(i int, rest string) string {
		return `,"output_index":` + strconv.Itoa(i) + `,"item":` + rest
	}
	reasoningDone := `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Checking"}],"encrypted_content":"E"}`
	stream := responsesEvent("response.created", 0, strings.Replace(responsesObject, `"id"`, `"created_at":1700000000,"id"`, 1)) +
		responsesEvent("response.output_item.added", 1, item(0, `{"id":"rs_1","type":"reasoning","summary":[]}`)) +
		responsesEvent("response.reasoning_summary_part.added", 2, `,"output_index":0,"summary_index":0,"item_id":"rs_1","part":{"type":"summary_text","text":""}`) +
		responsesEvent("response.reasoning_summary_text.delta", 3, `,"output_index":0,"summary_index":0,"item_id":"rs_1","delta":"Check"`) +
		responsesEvent("response.output_item.done", 4, item(0, reasoningDone)) +
		responsesEvent("response.output_item.added", 5, item(1, `{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}`)) +
		responsesEvent("response.content_part.added", 6, `,"output_index":1,"content_index":0,"item_id":"msg_1","part":{"type":"output_text","text":"","annotations":[]}`) +
		responsesEvent("response.output_text.delta", 7, `,"output_index":1,"content_index":0,"item_id":"msg_1","delta":"Hel"`) +
		responsesEvent("response.output_text.delta", 8, `,"output_index":1,"content_index":0,"item_id":"msg_1","delta":"lo\n"`) +
		responsesEvent("response.output_text.annotation.added", 9, `,"output_index":1,"content_index":0,"item_id":"msg_1","annotation_index":0,"annotation":{"type":"url_citation","url":"u"}`) +
		responsesEvent("response.output_item.added", 10, item(2, `{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"c1","name":"f","arguments":""}`)) +
		responsesEvent("response.function_call_arguments.delta", 11, `,"output_index":2,"item_id":"fc_1","delta":"{\"a\":"`)
	got := aggregate(t, protocol.Responses, protocol.Responses, strings.NewReader(stream), protocol.NewMemory(0), settledFinal)
	if !errors.Is(got.err, protocol.ErrUpstream) || got.status != http.StatusOK {
		t.Fatalf("error %v, status %d:\n%s", got.err, got.status, got.body)
	}
	body := decode(t, got.body)
	want := []any{
		decode(t, []byte(reasoningDone)),
		map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "status": "incomplete",
			"content": []any{map[string]any{
				"type": "output_text", "text": "Hello\n", "annotations": []any{map[string]any{"type": "url_citation", "url": "u"}},
			}},
		},
		map[string]any{
			"id": "fc_1", "type": "function_call", "call_id": "c1", "name": "f", "arguments": `{"a":`, "status": "incomplete",
		},
	}
	if diff := cmp.Diff(want, body["output"]); diff != "" {
		t.Errorf("output (-want +got):\n%s", diff)
	}
	if body["status"] != "failed" || body["object"] != "response" || body["id"] != "gen-01" || body["model"] != "claude-client-name" {
		t.Errorf("response %s", got.body)
	}
}

// TestAggregateResponsesFailOpenReasoning: a reasoning item that is still open has its summary parts and its reasoning
// text parts; a refusal part keeps its text in refusal.
func TestAggregateResponsesFailOpenReasoning(t *testing.T) {
	part := func(i int, kind, field string) string {
		return `,"output_index":` + strconv.Itoa(i) + `,"content_index":0,"item_id":"x","part":{"type":"` + kind + `","` + field + `":""}`
	}
	stream := responsesEvent("response.created", 0, responsesObject) +
		responsesEvent("response.output_item.added", 1, `,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}`) +
		responsesEvent("response.reasoning_summary_part.added", 2, `,"output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}`) +
		responsesEvent("response.reasoning_summary_text.delta", 3, `,"output_index":0,"summary_index":0,"delta":"S"`) +
		responsesEvent("response.content_part.added", 4, part(0, "reasoning_text", "text")) +
		responsesEvent("response.reasoning_text.delta", 5, `,"output_index":0,"content_index":0,"delta":"R"`) +
		responsesEvent("response.output_item.added", 6, `,"output_index":1,"item":{"id":"m","type":"message","content":[]}`) +
		responsesEvent("response.content_part.added", 7, part(1, "refusal", "refusal")) +
		responsesEvent("response.refusal.delta", 8, `,"output_index":1,"content_index":0,"delta":"no"`)
	got := aggregate(t, protocol.Responses, protocol.Responses, strings.NewReader(stream), protocol.NewMemory(0), unsettled)
	want := []any{
		map[string]any{
			"id": "rs_1", "type": "reasoning", "status": "incomplete",
			"summary": []any{map[string]any{"type": "summary_text", "text": "S"}},
			"content": []any{map[string]any{"type": "reasoning_text", "text": "R"}},
		},
		map[string]any{
			"id": "m", "type": "message", "status": "incomplete",
			"content": []any{map[string]any{"type": "refusal", "refusal": "no"}},
		},
	}
	if diff := cmp.Diff(want, decode(t, got.body)["output"]); diff != "" {
		t.Errorf("output (-want +got):\n%s", diff)
	}
}

// TestAggregateChatFraming: a chunk is JSON however it is spaced: whitespace before it, a tab, and a chunk of several
// lines all aggregate to the same reply; [DONE] and an event without data are the only events skipped.
func TestAggregateChatFraming(t *testing.T) {
	content := chatChunk(`[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]`)
	for name, chunk := range map[string]string{
		"plain":         content,
		"spaces":        strings.Replace(content, "data: {", "data:   {", 1),
		"a tab":         strings.Replace(content, "data: {", "data: \t{", 1),
		"several lines": strings.Replace(content, `,"choices":`, ",\ndata:  \"choices\":", 1),
	} {
		t.Run(name, func(t *testing.T) {
			got := aggregateAll(t, protocol.Chat, chatRole+chunk+chatStop+chatDone)
			choices := objects(got["choices"])
			if len(choices) != 1 || obj(choices[0]["message"])["content"] != "Hi" || choices[0]["finish_reason"] != "stop" {
				t.Errorf("choices %v", got["choices"])
			}
		})
	}
}

// TestAggregateResponsesTypes: a part or an item whose type is not a string is the upstream's failure when it arrives,
// not a panic when the failure is written from it; an escaped type is the type it spells.
func TestAggregateResponsesTypes(t *testing.T) {
	added := func(item string) string {
		return responsesEvent("response.output_item.added", 1, `,"output_index":0,"item":`+item)
	}
	message := added(`{"id":"msg_1","type":"message","content":[]}`)
	part := func(kind string) string {
		return responsesEvent("response.content_part.added", 2, `,"output_index":0,"content_index":0,"item_id":"msg_1","part":{"type":`+kind+`,"text":""}`) +
			responsesEvent("response.output_text.delta", 3, `,"output_index":0,"content_index":0,"item_id":"msg_1","delta":"x"`)
	}
	head := responsesEvent("response.created", 0, responsesObject)
	for name, c := range map[string]struct {
		stream  string
		wantErr bool
	}{
		"a part of a number":   {head + message + part(`1`), true},
		"a part of null":       {head + message + part(`null`), true},
		"a part of an object":  {head + message + part(`{}`), true},
		"an item of a number":  {head + added(`{"id":"msg_1","type":7,"content":[]}`), true},
		"an escaped part type": {head + message + part(`"output\u005ftext"`), false},
	} {
		t.Run(name, func(t *testing.T) {
			got := aggregate(t, protocol.Responses, protocol.Responses, strings.NewReader(c.stream), protocol.NewMemory(0), unsettled)
			if !errors.Is(got.err, protocol.ErrUpstream) {
				t.Fatalf("error %v, want the upstream's", got.err)
			}
			if c.wantErr {
				return
			}
			output := listOf(decode(t, got.body)["output"])
			parts := listOf(obj(output[0])["content"])
			if len(parts) != 1 || text(obj(parts[0])["text"]) != "x" || obj(parts[0])["annotations"] == nil {
				t.Errorf("output %v", output)
			}
		})
	}
}

// TestAggregateMessagesTypes: a block's type counts as the type it spells, escapes included, so a delta of that kind
// is the block's own; the type reaches the reply as the upstream wrote it, and one that is null names no kind of block
// and is written as it came (README.md「非流式聚合」).
func TestAggregateMessagesTypes(t *testing.T) {
	head := messageStart(`{"input_tokens":3}`)
	tail := messageDelta(`{"output_tokens":2}`) + messageStop
	for _, c := range []struct {
		name   string
		stream string
		want   map[string]any
	}{
		{
			"an escaped text type",
			head + startBlock(0, `{"type":"te\u0078t","text":""}`) + deltaOf(0, `{"type":"text_delta","text":"Hi"}`) + stopBlock(0) + tail,
			map[string]any{"type": "text", "text": "Hi"},
		},
		{
			"an escaped thinking type",
			head + startBlock(0, `{"type":"think\u0069ng","thinking":"","signature":""}`) +
				deltaOf(0, `{"type":"thinking_delta","thinking":"hm"}`) + deltaOf(0, `{"type":"signature_delta","signature":"S"}`) + stopBlock(0) + tail,
			map[string]any{"type": "thinking", "thinking": "hm", "signature": "S"},
		},
		{
			"an escaped tool use type",
			head + startBlock(0, `{"type":"tool_\u0075se","id":"t","name":"f","input":{}}`) + input(0, `{\"q\":1}`) + stopBlock(0) + tail,
			map[string]any{"type": "tool_use", "id": "t", "name": "f", "input": map[string]any{"q": 1.0}},
		},
		{
			"an escaped server tool use type",
			head + startBlock(0, `{"type":"server_tool_\u0075se","id":"s","name":"web_search","input":{}}`) + input(0, `{\"query\":\"q\"}`) + stopBlock(0) + tail,
			map[string]any{"type": "server_tool_use", "id": "s", "name": "web_search", "input": map[string]any{"query": "q"}},
		},
		{
			"a type that is null",
			head + startBlock(0, `{"type":null,"text":"x"}`) + stopBlock(0) + tail,
			map[string]any{"type": nil, "text": "x"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			content := objects(aggregateAll(t, protocol.Messages, c.stream)["content"])
			if len(content) != 1 || !cmp.Equal(content[0], c.want) {
				t.Errorf("content %v, want %v", content, c.want)
			}
		})
	}
}

// finishOnly is a Chat stream of n choices that only finish, per chunk of them.
func finishOnly(n, per int) string {
	var s strings.Builder
	for first := 0; first < n; first += per {
		list := make([]string, 0, per)
		for i := first; i < min(first+per, n); i++ {
			list = append(list, `{"index":`+strconv.Itoa(i)+`,"delta":{},"finish_reason":"stop"}`)
		}
		s.WriteString(chatChunk("[" + strings.Join(list, ",") + "]"))
	}
	return s.String() + chatDone
}

// TestAggregateResponsesBadItem: an item the upstream sends with a type that is not a string fails the call, and the
// reply keeps what it had generated: the failure is still a 200 with the output so far.
func TestAggregateResponsesBadItem(t *testing.T) {
	stream := responsesEvent("response.created", 0, responsesObject) +
		responsesEvent("response.output_item.added", 1, `,"output_index":0,"item":{"id":"msg_1","type":"message","content":[]}`) +
		responsesEvent("response.content_part.added", 2, `,"output_index":0,"content_index":0,"item_id":"msg_1","part":{"type":"output_text","text":""}`) +
		responsesEvent("response.output_text.delta", 3, `,"output_index":0,"content_index":0,"item_id":"msg_1","delta":"Hi"`) +
		responsesEvent("response.output_item.added", 4, `,"output_index":1,"item":{"id":"x","type":null}`)
	got := aggregate(t, protocol.Responses, protocol.Responses, strings.NewReader(stream), protocol.NewMemory(0), unsettled)
	if !errors.Is(got.err, protocol.ErrUpstream) || got.status != http.StatusOK {
		t.Fatalf("error %v, status %d:\n%s", got.err, got.status, got.body)
	}
	output := listOf(decode(t, got.body)["output"])
	if len(output) != 1 || text(obj(listOf(obj(output[0])["content"])[0])["text"]) != "Hi" {
		t.Errorf("output %v", output)
	}
}

// TestAggregateChatHeld: the choices a Chat upstream finishes are held until its end and made into the reply after the
// charging has ended, so they are charged when they are held: finish-only choices past the cap fail the stream with
// ErrMemory before its end, and give back what they drew, though no chunk comes near the size of an event.
func TestAggregateChatHeld(t *testing.T) {
	const node, perChunk = 256 << 20, 10000
	for _, c := range []struct {
		name    string
		choices int
		memory  bool
	}{{"within the cap", perChunk, false}, {"past the cap", 12 * perChunk, true}} {
		t.Run(c.name, func(t *testing.T) {
			mem := protocol.NewMemory(node)
			got := aggregate(t, protocol.Chat, protocol.Chat, strings.NewReader(finishOnly(c.choices, perChunk)), mem, unsettled)
			if errors.Is(got.err, protocol.ErrMemory) != c.memory || errors.Is(got.err, protocol.ErrUpstream) || (got.err != nil) != c.memory {
				t.Errorf("error %v, want a memory error: %v", got.err, c.memory)
			}
			if n := len(objects(decode(t, got.body)["choices"])); !c.memory && n != c.choices {
				t.Errorf("%d choices, want %d", n, c.choices)
			}
			if mem.Left() != node {
				t.Errorf("the node's budget is %d after the reply, was %d", mem.Left(), node)
			}
		})
	}
}

// TestAggregateChatKeepsTheCallIdentity: DashScope and DeepSeek write "id":"" and "name":"" on the fragments of a call
// after the first, which carry only arguments; the call keeps the id and name it started with (sub2api drops the same
// filler, openai_gateway_cc_tool_call_identity.go).
func TestAggregateChatKeepsTheCallIdentity(t *testing.T) {
	stream := chatChunk(`[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_1","type":"function",`+
		`"function":{"name":"get","arguments":""}}]},"finish_reason":null}]`) +
		chatChunk(`[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"","function":{"name":"","arguments":"{\"a\""}}]},"finish_reason":null}]`) +
		chatChunk(`[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":":1}"}}]},"finish_reason":null}]`) +
		chatChunk(`[{"index":0,"delta":{},"finish_reason":"tool_calls"}]`) + chatDone
	got := aggregateAll(t, protocol.Chat, stream)
	calls := listOf(obj(obj(listOf(got["choices"])[0])["message"])["tool_calls"])
	want := []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "get", "arguments": `{"a":1}`}}}
	if diff := cmp.Diff(want, calls); diff != "" {
		t.Errorf("tool calls (-want +got):\n%s", diff)
	}
}
