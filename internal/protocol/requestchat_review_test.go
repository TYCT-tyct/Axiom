package protocol_test

import (
	"testing"

	"axiom/internal/protocol"
)

func TestResponsesOmittedStrictKeepsSchema(t *testing.T) {
	t.Parallel()
	for _, schema := range []string{
		`{"type":"object","additionalProperties":true}`,
		`{"type":"object","additionalProperties":{"type":"string"}}`,
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","oneOf":[{"type":"string"},{"type":"number"}],"examples":["x"],"default":"x"}`,
		`{"type":"object","properties":{"x":{"type":"string","default":"X"}}}`,
	} {
		body := chatBody(`"input":"Go","tools":[{"type":"function","name":"f","parameters":` + schema + `}]`)
		up, err := prepareNativeChat(protocol.Responses, body, "up", nil)
		if err != nil {
			t.Fatal(err)
		}
		tool := member(t, up.Body, "tools").([]any)[0].(map[string]any)["function"].(map[string]any)
		if tool["strict"] != false {
			t.Fatalf("implicit strict did not fall back: %s", up.Body)
		}
		sameJSON(t, schema, mustMarshal(t, tool["parameters"]))
	}
}

func TestMessagesToolResultsToChat(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		content, isError, want string
	}{
		{`"failed"`, "true", `"Error: failed"`},
		{`[{"type":"text","text":"failed"}]`, "true", `[{"type":"text","text":"Error: "},{"type":"text","text":"failed"}]`},
		{`[]`, "true", `"Error: "`},
		{`null`, "true", `"Error: "`},
		{`"ok"`, "false", `"ok"`},
	} {
		body := chatBody(`"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","is_error":` + tc.isError + `,"content":` + tc.content + `}]}]`)
		up, err := prepareNativeChat(protocol.Messages, body, "up", nil)
		if err != nil {
			t.Fatal(err)
		}
		tool := member(t, up.Body, "messages").([]any)[1].(map[string]any)
		sameJSON(t, tc.want, mustMarshal(t, tool["content"]))
		if tool["role"] != "tool" || tool["tool_call_id"] != "c1" {
			t.Fatalf("content %s, is_error %s: lost output or pairing: %s", tc.content, tc.isError, up.Body)
		}
	}
}

func TestResponsesChatExplicitDefaults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		members string
		ok      bool
	}{
		{`"truncation":"disabled","background":false,"include":["reasoning.encrypted_content"]`, true},
		{`"truncation":null,"background":null,"include":[]`, true},
		{`"truncation":"auto"`, false},
		{`"background":true`, false},
		{`"include":["reasoning.encrypted_content","message.output_text.logprobs"]`, false},
		{`"include":[1]`, false},
		{`"include":{}`, false},
	} {
		up, err := prepareNativeChat(protocol.Responses, chatBody(`"input":"Go",`+tc.members), "up", nil)
		if (err == nil) != tc.ok {
			t.Fatalf("%s: %v", tc.members, err)
		}
		if tc.ok {
			sameJSON(t, `{"model":"up","stream":true,"stream_options":{"include_usage":true},"max_tokens":4096,"messages":[{"role":"user","content":"Go"}]}`, up.Body)
		}
	}
}

func TestResponsesReasoningKeepsItsHistoryPosition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ items, want string }{
		{
			`{"role":"user","content":"old"},{"type":"reasoning","summary":[{"type":"summary_text","text":"why"}]},{"role":"user","content":"new"}`,
			`[{"role":"user","content":"old"},{"role":"assistant","content":"","reasoning_content":"why"},{"role":"user","content":"new"}]`,
		},
		{
			`{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},{"type":"reasoning","summary":[{"type":"summary_text","text":"why"}]},{"type":"function_call_output","call_id":"c1","output":"ok"},{"role":"user","content":"new"}`,
			`[{"role":"assistant","content":"","tool_calls":[{"type":"function","id":"c1","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"},{"role":"user","content":"new"}]`,
		},
	} {
		up, err := prepareNativeChat(protocol.Responses, chatBody(`"input":[`+tc.items+`]`), "up", nil)
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, tc.want, mustMarshal(t, member(t, up.Body, "messages")))
	}
}

func TestResponsesDanglingReasoningInResolvedHistory(t *testing.T) {
	t.Parallel()
	history := [][]byte{
		[]byte(`{"role":"user","content":"old"}`),
		[]byte(`{"type":"reasoning","summary":[{"type":"summary_text","text":"why"}]}`),
	}
	up, err := prepareNativeChat(protocol.Responses, chatBody(`"previous_response_id":"last","input":"next"`), "up", history)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, `[{"role":"user","content":"old"},{"role":"assistant","content":"","reasoning_content":"why"},{"role":"user","content":"next"}]`, mustMarshal(t, member(t, up.Body, "messages")))
}
