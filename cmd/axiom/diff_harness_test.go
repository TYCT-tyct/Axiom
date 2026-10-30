//go:build diff

package main

import (
	"net/http"
	"strings"
	"testing"
)

// These tests are about the comparison, not about the gateways: they need no container. A comparison that lets a
// degraded gateway through is worse than none, so each rule is given a difference it explains and one it must not.

// TestDiffHarnessRulesRefuseDegradedValues: a rule explains the difference it is about and nothing else at the same path.
func TestDiffHarnessRulesRefuseDegradedValues(t *testing.T) {
	t.Parallel()
	const callID = `"01a152cc-b523-7cb6-a0da-e4134209295f"`
	for _, tc := range []struct {
		name, kase, path, newAPI, axiom string
		explained                       bool
	}{
		{"429 is surfaced as 502", "chat-claude-429-whole", "status", "429", "502", true},
		{"429 that Axiom answers 200", "chat-claude-429-whole", "status", "429", "200", false},
		{"429 that Axiom answers 500", "chat-claude-429-whole", "status", "429", "500", false},
		{"a 500 case is not excused a 429", "chat-claude-500-whole", "status", "429", "502", false},
		{"unknown model is 404", "messages-claude-unknown-model", "status", "503", "404", true},
		{"unknown model that is served", "messages-claude-unknown-model", "status", "503", "200", false},
		{"upstream header dropped", "messages-claude-text-whole", "header.cf-ray", "0000-AMS", absent, true},
		{"upstream header passed on", "messages-claude-text-whole", "header.cf-ray", "0000-AMS", "0000-FRA", false},
		{"upstream header made up", "messages-claude-text-whole", "header.x-upstream", absent, "secret", false},
		{"call id header", "messages-claude-text-whole", "header.x-request-id", absent, "abc", true},
		{"call id header on both", "messages-claude-text-whole", "header.x-request-id", "abc", "abd", false},
		{"call id as id", "responses-gpt-text-whole", "json.id", `"resp_1"`, callID, true},
		{"upstream id as id", "responses-gpt-text-whole", "json.id", `"resp_1"`, `"resp_2"`, false},
		{"cost added", "responses-gpt-text-whole", "json.usage.cost", absent, "0.0004", true},
		{"cost where new-api has another", "responses-gpt-text-whole", "json.usage.cost", "0.0003", "0.0004", false},
		{
			"tool arguments with other spaces", "chat-claude-tools-whole", "json.choices[].message.tool_calls[].function.arguments",
			`"{\"location\":\"SF\"}"`, `"{\"location\": \"SF\"}"`, true,
		},
		{
			"tool arguments emptied", "chat-claude-tools-whole", "json.choices[].message.tool_calls[].function.arguments",
			`"{\"location\":\"SF\"}"`, `"{}"`, false,
		},
		{
			"tool arguments of another place", "chat-claude-tools-whole", "json.choices[].message.tool_calls[].function.arguments",
			`"{\"location\":\"SF\"}"`, `"{\"location\":\"LA\"}"`, false,
		},
		{"reasoning tokens counted", "chat-gpt-reasoning-whole", "json.usage.completion_tokens_details.reasoning_tokens", "0", "12", true},
		{"reasoning tokens made up", "chat-gpt-reasoning-whole", "json.usage.completion_tokens_details.reasoning_tokens", "0", "99", false},
		{"unified wording", "chat-claude-429-whole", "json.error.message", `"upstream said"`, `"The model provider failed to complete the request."`, true},
		{"other wording", "chat-claude-429-whole", "json.error.message", `"upstream said"`, `"boom"`, false},
		{"ping before content", "messages-claude-text-stream", "sse[ping]", `{"type":"ping"}`, absent, true},
		{"another event dropped", "messages-claude-text-stream", "sse[content_block_delta]", `{"type":"x"}`, absent, false},
		{"reasoning_content with a newline more", "chat-claude-reasoning-stream", "chat.choices[].delta.reasoning_content", `"abc\n"`, `"abc"`, true},
		{"reasoning_content changed", "chat-claude-reasoning-stream", "chat.choices[].delta.reasoning_content", `"abd\n"`, `"abc"`, false},
		{"own error is not the upstream's refusal", "chat-claude-invalid-json", "json.error.type", `"new_api_error"`, `"invalid_request_error"`, true},
		{"a refusal whose type differs", "chat-claude-400-whole", "json.error.type", `"invalid_request_error"`, `"api_error"`, false},
		{"empty code of a wrong key", "chat-claude-wrong-key", "json.error.code", `""`, `"authentication"`, true},
		{"empty code where Axiom has a number", "chat-claude-wrong-key", "json.error.code", `""`, "401", false},
		{"a refusal's message differs", "chat-claude-400-whole", "json.error.message", `"max_tokens: too small"`, `"The request is not valid."`, false},
		{"a refusal's code differs", "responses-gpt-400-whole", "json.error.code", `"invalid_value"`, `"upstream_error"`, false},
		{"upstream 502's code", "responses-gpt-500-whole", "json.error.code", "null", `"upstream_error"`, true},
		{"unified error type", "chat-claude-400-whole", "json.error.metadata.error_type", absent, `"invalid_request"`, true},
		{"stream header on a failed stream", "chat-claude-429-stream", "header.x-accel-buffering", "no", absent, true},
		{"stream header missing from a stream that starts", "chat-claude-text-stream", "header.x-accel-buffering", "no", absent, false},
		{"reasoning beside reasoning_content", "chat-claude-reasoning-whole", "json.choices[].message.reasoning", absent, `"abc"`, true},
		{"reasoning_content missing", "chat-claude-reasoning-whole", "json.choices[].message.reasoning_content", `"abc"`, absent, false},
		{"usage null dropped", "responses-gpt-text-stream", "sse[response.created].response.usage", "null", absent, false},
		{"deepseek details made 0", "chat-deepseek-text-whole", "json.usage.prompt_tokens_details.cached_tokens", absent, "0", true},
		{"deepseek details made up", "chat-deepseek-text-whole", "json.usage.prompt_tokens_details.cached_tokens", absent, "5", false},
		{"deepseek details lost", "chat-deepseek-tools-whole", "json.usage.prompt_tokens_details.cached_tokens", "64", "0", false},
		{"deepseek cache names dropped", "chat-deepseek-tools-whole", "json.usage.prompt_cache_hit_tokens", "64", absent, true},
		{"deepseek cache names changed", "chat-deepseek-tools-whole", "json.usage.prompt_cache_hit_tokens", "64", "65", false},
		{"deepseek cache names made up", "chat-deepseek-tools-whole", "json.usage.prompt_cache_hit_tokens", absent, "64", false},
		{"deepseek cache names in another case", "chat-gpt-tools-whole", "json.usage.prompt_cache_hit_tokens", "64", absent, false},
		{"converted item identity", "responses-deepseek-text-whole", "json.output[].id", `"msg_up"`, `"12345678-1234-1234-1234-123456789abc_0"`, true},
		{"converted identity cannot leak", "responses-deepseek-text-whole", "json.output[].id", `"msg_up"`, `"msg_other_up"`, false},
		{"converted identity cannot disappear", "responses-deepseek-text-whole", "json.output[].id", `"msg_up"`, absent, false},
		{"times", "chat-claude-text-whole", "json.created", "1741290958", "1792390509", true},
		{"time that is no time", "chat-claude-text-whole", "json.created", "1741290958", `"soon"`, false},
		{
			"OpenAI names in an Anthropic usage", "messages-deepseek-text-whole", "json.usage.prompt_tokens", "9", absent, true,
		},
		{
			"an Anthropic member new-api left out", "messages-deepseek-text-whole", "json.usage.input_tokens", absent, "9", true,
		},
		{
			"an Anthropic member both have", "messages-deepseek-text-whole", "json.usage.output_tokens", "12", "9", false,
		},
		{
			"the prompt new-api counts itself", "messages-deepseek-text-stream",
			"sse[message_start].message.usage.input_tokens", "532", "0", true,
		},
		{
			"an input count that is Axiom's", "messages-deepseek-text-stream",
			"sse[message_start].message.usage.input_tokens", "532", "9", false,
		},
		{
			"a stop sequence neither matched", "messages-deepseek-text-whole", "json.stop_sequence", absent, "null", true,
		},
		{
			"a stop sequence new-api has", "messages-deepseek-text-whole", "json.stop_sequence", `"\n\nHuman:"`, "null", false,
		},
		{
			"the physical input total", "responses-claude-reasoning-stream",
			"sse[response.completed].response.usage.input_tokens", "18", "29", true,
		},
		{
			"an input total zeroed", "responses-claude-reasoning-stream",
			"sse[response.completed].response.usage.input_tokens", "18", "0", false,
		},
		{
			"an input total inflated", "responses-claude-reasoning-stream",
			"sse[response.completed].response.usage.input_tokens", "18", "30", false,
		},
		{
			"an output total", "responses-claude-reasoning-stream",
			"sse[response.completed].response.usage.output_tokens", "123", "147", true,
		},
		{
			"an output total inflated", "responses-claude-reasoning-stream",
			"sse[response.completed].response.usage.output_tokens", "123", "148", false,
		},
		{
			"a total", "responses-claude-reasoning-stream",
			"sse[response.completed].response.usage.total_tokens", "141", "176", true,
		},
		{
			"a total that is not the sum", "responses-claude-reasoning-stream",
			"sse[response.completed].response.usage.total_tokens", "141", "175", false,
		},
		{
			"the whole reply's input total", "responses-claude-reasoning-whole", "json.usage.input_tokens", "0", "29", true,
		},
		{
			"the whole reply's input total zeroed", "responses-claude-reasoning-whole", "json.usage.input_tokens", "0", "0", false,
		},
		{
			"the whole reply's input total inflated", "responses-claude-reasoning-whole", "json.usage.input_tokens", "0", "30", false,
		},
		{
			"a block stopped elsewhere", "messages-deepseek-tools-stream",
			"sse[content_block_stop]", `{"index":1,"type":"content_block_stop"}`, absent, true,
		},
		{
			"a block stopped in the other stream", "messages-deepseek-tools-stream",
			"sse[content_block_stop]", absent, `{"index":2,"type":"content_block_stop"}`, true,
		},
		{
			"a block stop that became something else", "messages-deepseek-tools-stream",
			"sse[content_block_stop]", `{"index":1,"type":"content_block_stop"}`, `{"index":1,"type":"content_block_delta"}`, false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := difference{path: tc.path, at: tc.path, newAPI: tc.newAPI, axiom: tc.axiom}
			if got := explain(diffRules(), tc.kase, d) != nil; got != tc.explained {
				t.Errorf("%s: %s -> %s explained = %v, want %v", tc.path, tc.newAPI, tc.axiom, got, tc.explained)
			}
		})
	}
}

// sseReply is a reply made of a stream.
func sseReply(body string) reply {
	return reply{status: 200, header: http.Header{"Content-Type": {"text/event-stream"}}, body: []byte(body)}
}

// chatChunks writes deltas as the chunks of a Chat stream.
func chatChunks(deltas ...string) string {
	var b strings.Builder
	for _, d := range deltas {
		b.WriteString(`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":` + d + `,"finish_reason":null}]}` + "\n\n")
	}
	return b.String()
}

// TestDiffHarnessChatStreamEnd: a stream that ends differently is a different stream, though its chunks add up alike.
func TestDiffHarnessChatStreamEnd(t *testing.T) {
	t.Parallel()
	const done = "data: [DONE]\n\n"
	first, second := chatChunks(`{"content":"Hel"}`), chatChunks(`{"content":"lo"}`)
	normal := first + second + done
	for _, tc := range []struct {
		name, body string
		differs    bool
	}{
		{"the same stream", first + second + done, false},
		{"[DONE] before the text", first + done + second, true},
		{"[DONE] twice", first + second + done + done, true},
		{"data after [DONE]", first + second + done + chatChunks(`{"content":"!"}`), true},
		{"no [DONE]", first + second, true},
		{"stray data that is not JSON", first + second + "data: oops\n\n" + done, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := compareReplies(sseReply(normal), sseReply(tc.body))
			if (len(got) > 0) != tc.differs {
				t.Errorf("differences %v, want some: %v", got, tc.differs)
			}
		})
	}
}

// TestDiffHarnessToolArgumentsAddUp: the arguments of a tool call are the text its pieces make, wherever the cuts fall,
// including inside an escape.
func TestDiffHarnessToolArgumentsAddUp(t *testing.T) {
	t.Parallel()
	const arguments = "{\"x\":\"a\\\"b\\c\nd\",\"y\":1}" // {"x":"a\"b\c\nd","y":1}, with the escapes the JSON text has
	call := func(piece string) string {
		return `{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":` + canonical(piece) + `}}]}`
	}
	stream := func(cuts ...int) string {
		var deltas []string
		last := 0
		for _, cut := range append(cuts, len(arguments)) {
			deltas = append(deltas, call(arguments[last:cut]))
			last = cut
		}
		return chatChunks(deltas...) + "data: [DONE]\n\n"
	}
	whole := sseReply(stream())
	for cut := 1; cut < len(arguments); cut++ {
		if got := compareReplies(whole, sseReply(stream(cut))); len(got) > 0 {
			t.Fatalf("cut at %d (%q|%q): %v", cut, arguments[:cut], arguments[cut:], got)
		}
	}
	if got := compareReplies(whole, sseReply(stream(3, 9, 14))); len(got) > 0 {
		t.Errorf("three cuts: %v", got)
	}
	damaged := strings.Replace(stream(5), `\\c`, `\\d`, 1)
	if damaged == stream(5) {
		t.Fatal("the damage was not done")
	}
	if got := compareReplies(whole, sseReply(damaged)); len(got) == 0 {
		t.Error("arguments with a letter changed are not a difference")
	}
}

// TestDiffHarnessReasoningNamesAgree: Axiom's two names for the reasoning of a Chat reply carry the same text.
func TestDiffHarnessReasoningNamesAgree(t *testing.T) {
	t.Parallel()
	whole := func(body string) reply {
		return reply{status: 200, header: http.Header{"Content-Type": {"application/json"}}, body: []byte(body)}
	}
	for _, tc := range []struct {
		name string
		r    reply
		ok   bool
	}{
		{"whole, the same", whole(`{"choices":[{"message":{"reasoning":"abc","reasoning_content":"abc"}}]}`), true},
		{"whole, none", whole(`{"choices":[{"message":{"content":"x"}}]}`), true},
		{"whole, another text", whole(`{"choices":[{"message":{"reasoning":"WRONG","reasoning_content":"abc"}}]}`), false},
		{"whole, one name", whole(`{"choices":[{"message":{"reasoning_content":"abc"}}]}`), false},
		{"stream, the same", sseReply(chatChunks(`{"reasoning":"a","reasoning_content":"a"}`, `{"reasoning":"b","reasoning_content":"b"}`)), true},
		{"stream, another text in one delta", sseReply(chatChunks(`{"reasoning":"a","reasoning_content":"a"}`, `{"reasoning":"x","reasoning_content":"b"}`)), false},
		{"stream, one name", sseReply(chatChunks(`{"reasoning":"a"}`)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := reasoningNamesAgree(tc.r); (err == nil) != tc.ok {
				t.Errorf("reasoningNamesAgree = %v, want ok %v", err, tc.ok)
			}
		})
	}
}
