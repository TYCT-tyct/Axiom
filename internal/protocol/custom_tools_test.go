package protocol_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
)

// Codex's custom (freeform) tools reach a Chat or Messages endpoint as functions taking one input string, with the
// grammar in the description; their calls and outputs in the history go the same way.
func TestCustomToolsToChat(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		f       protocol.Format
		members string
		want    string
	}{
		{
			"a freeform tool with its grammar, chosen by name",
			protocol.Responses,
			`"input":"Go","tools":[{"type":"custom","name":"apply_patch","description":"Patch files.","format":{"type":"grammar","syntax":"lark","definition":"start: x"}}],"tool_choice":{"type":"custom","name":"apply_patch"}`,
			`"messages":[{"role":"user","content":"Go"}],"max_tokens":4096,"tools":[{"type":"function","function":{"name":"apply_patch","description":"Patch files.\n\nThe input must follow this lark grammar:\nstart: x","parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false},"strict":false}}],"tool_choice":{"type":"function","function":{"name":"apply_patch"}}`,
		},
		{
			"a plain text tool",
			protocol.Responses,
			`"input":"Go","tools":[{"type":"custom","name":"note","format":{"type":"text"}}]`,
			`"messages":[{"role":"user","content":"Go"}],"max_tokens":4096,"tools":[{"type":"function","function":{"name":"note","parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false},"strict":false}}]`,
		},
		{
			"a custom call and its output in the history",
			protocol.Responses,
			`"input":[{"role":"user","content":"Go"},{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"*** Begin Patch\n\"q\""},{"type":"custom_tool_call_output","call_id":"c1","output":"done"}]`,
			`"messages":[{"role":"user","content":"Go"},{"role":"assistant","content":"","tool_calls":[{"type":"function","id":"c1","function":{"name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\\n\\\"q\\\"\"}"}}]},{"role":"tool","tool_call_id":"c1","content":"done"}],"max_tokens":4096`,
		},
		{
			"a Messages tool typed custom",
			protocol.Messages,
			`"messages":[{"role":"user","content":"Go"}],"tools":[{"type":"custom","name":"f","input_schema":{"type":"object"}}]`,
			`"messages":[{"role":"user","content":"Go"}],"max_tokens":4096,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			up, err := prepareNativeChat(c.f, chatBody(c.members), "up", nil)
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, `{"model":"up","stream":true,"stream_options":{"include_usage":true},`+c.want+`}`, up.Body)
		})
	}
}

// A tool no function can stand for is refused by its type, so the gateway can say which one and try another endpoint.
func TestToolsWithoutACounterpartAreNamed(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		f             protocol.Format
		members, name string
	}{
		{protocol.Messages, `"messages":[],"tools":[{"type":"web_search_20250305","name":"web_search"}]`, "web_search_20250305"},
		{protocol.Messages, `"messages":[],"tools":[{"type":"bash_20250124","name":"bash"}]`, "bash_20250124"},
		{protocol.Responses, `"input":"Hi","tools":[{"type":"local_shell"}]`, "local_shell"},
		{protocol.Responses, `"input":"Hi","tools":[{"type":"custom","name":"x","format":{"type":"regex"}}]`, "regex"},
		{protocol.Responses, `"input":[{"type":"local_shell_call","call_id":"c","action":{}}]`, "local_shell_call"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := prepareNativeChat(c.f, chatBody(c.members), "up", nil)
			if ns, _ := errors.AsType[*protocol.NotSupported](err); ns == nil || !strings.Contains(ns.Reason, c.name) {
				t.Fatalf("want NotSupported naming %s, got %v", c.name, err)
			}
		})
	}
}

// A call of a custom tool comes back to a Responses client as a custom tool call holding the input, streamed and whole,
// from a Chat upstream and from a Messages one; a function's call beside it stays a function call.
func TestCustomToolCallsReturnAsCustom(t *testing.T) {
	t.Parallel()
	patch := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(`*** Begin Patch\n\"x\"`) // the input's JSON, quoted once more
	chat := chatPiece(`{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"apply_patch","arguments":"{\"input\":"}}]}`, "null") +
		chatPiece(`{"tool_calls":[{"index":0,"function":{"arguments":"\"`+patch+`\"}"}}]}`, "null") +
		chatPiece(`{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"f","arguments":"{}"}}]}`, `"tool_calls"`) +
		ev(`{"id":"c","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`) + ev("[DONE]")
	messages := ev(`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"up","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":0}}}`) +
		startBlock(0, `{"type":"tool_use","id":"c1","name":"apply_patch","input":{}}`) +
		deltaOf(0, `{"type":"input_json_delta","partial_json":"{\"input\":\"`+patch+`\"}"}`) + stopBlock(0) +
		startBlock(1, `{"type":"tool_use","id":"c2","name":"f","input":{}}`) + deltaOf(1, `{"type":"input_json_delta","partial_json":"{}"}`) + stopBlock(1) +
		ev(`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":2}}`) +
		ev(`{"type":"message_stop"}`)
	o := options
	o.CustomTools = []string{"apply_patch"}
	for _, c := range []struct {
		name     string
		upstream protocol.Format
		stream   string
	}{{"chat", protocol.Chat, chat}, {"messages", protocol.Messages, messages}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := relayWith(t, protocol.Responses, c.upstream, strings.NewReader(c.stream), protocol.UsageContext{}, protocol.NewMemory(64<<20), o, settled)
			if got.err != nil {
				t.Fatal(got.err)
			}
			var events []string
			var done any
			r := sse.NewReader(bytes.NewReader(got.out), 32<<20)
			for data, err := r.Next(); err == nil; data, err = r.Next() {
				kind := member(t, data, "type").(string)
				events = append(events, kind)
				if kind == "response.completed" {
					done = member(t, data, "response").(map[string]any)["output"]
				}
			}
			joined := strings.Join(events, ",")
			if !strings.Contains(joined, "response.custom_tool_call_input.delta,response.custom_tool_call_input.done") ||
				strings.Count(joined, "response.function_call_arguments.delta") != 1 {
				t.Errorf("events: %s", joined)
			}
			sameJSON(t, `[{"id":"gen-01_0","type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"*** Begin Patch\n\"x\"","status":"completed"},`+
				`{"id":"gen-01_1","type":"function_call","call_id":"c2","name":"f","arguments":"{}","status":"completed"}]`, mustMarshal(t, done))
		})
	}
}
