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
		{"tool arguments with other spaces", "chat-claude-tools-whole", "json.choices[].message.tool_calls[].function.arguments",
			`"{\"location\":\"SF\"}"`, `"{\"location\": \"SF\"}"`, true},
		{"tool arguments emptied", "chat-claude-tools-whole", "json.choices[].message.tool_calls[].function.arguments",
			`"{\"location\":\"SF\"}"`, `"{}"`, false},
		{"tool arguments of another place", "chat-claude-tools-whole", "json.choices[].message.tool_calls[].function.arguments",
			`"{\"location\":\"SF\"}"`, `"{\"location\":\"LA\"}"`, false},
		{"reasoning tokens counted", "chat-gpt-reasoning-whole", "json.usage.completion_tokens_details.reasoning_tokens", "0", "12", true},
		{"reasoning tokens made up", "chat-gpt-reasoning-whole", "json.usage.completion_tokens_details.reasoning_tokens", "0", "99", false},
		{"unified wording", "chat-claude-429-whole", "json.error.message", `"upstream said"`, `"The model provider failed to complete the request."`, true},
		{"other wording", "chat-claude-429-whole", "json.error.message", `"upstream said"`, `"boom"`, false},
		{"ping before content", "messages-claude-text-stream", "sse[ping]", `{"type":"ping"}`, absent, true},
		{"another event dropped", "messages-claude-text-stream", "sse[content_block_delta]", `{"type":"x"}`, absent, false},
		{"reasoning with a newline more", "chat-claude-reasoning-stream", "chat.choices[].delta.reasoning_text", `"abc\n"`, `"abc"`, true},
		{"reasoning changed", "chat-claude-reasoning-stream", "chat.choices[].delta.reasoning_text", `"abd\n"`, `"abc"`, false},
		{"times", "chat-claude-text-whole", "json.created", "1741290958", "1792390509", true},
		{"time that is no time", "chat-claude-text-whole", "json.created", "1741290958", `"soon"`, false},
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
