package protocol_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
)

// contents gathers what a stream of format f says, in order: for each Messages block, Responses part and Chat choice,
// its text, reasoning and tool calls. A relay that ends normally must hand the client exactly what the upstream sent.
func contents(t testing.TB, f protocol.Format, stream []byte) map[string]string {
	t.Helper()
	got := map[string]string{}
	add := func(key string, v any) {
		if s, ok := v.(string); ok && s != "" {
			got[key] += s
		}
	}
	r := sse.NewReader(bytes.NewReader(stream), 32<<20)
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		if string(data) == sse.Done {
			continue
		}
		var e map[string]any
		if err := jsonx.Unmarshal(data, &e); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		switch f {
		case protocol.Messages:
			messagesContents(e, add)
		case protocol.Responses:
			if typ, _ := e["type"].(string); strings.HasSuffix(typ, ".delta") {
				add(fmt.Sprintf("%v/%v%v/%s", e["output_index"], e["content_index"], e["summary_index"], typ), e["delta"])
			}
		case protocol.Chat:
			chatContents(e, add)
		}
	}
	return got
}

func messagesContents(e map[string]any, add func(string, any)) {
	for _, member := range []string{"content_block", "delta"} {
		if o, ok := e[member].(map[string]any); ok {
			for _, field := range []string{"text", "thinking", "partial_json"} {
				add(fmt.Sprintf("%v/%s", e["index"], field), o[field])
			}
		}
	}
}

func chatContents(e map[string]any, add func(string, any)) {
	choices, _ := e["choices"].([]any)
	for _, c := range choices {
		choice, _ := c.(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		for _, field := range []string{"content", "reasoning", "reasoning_content", "refusal"} {
			add(fmt.Sprintf("%v/%s", choice["index"], field), delta[field])
		}
		calls, _ := delta["tool_calls"].([]any)
		for _, call := range calls {
			call, _ := call.(map[string]any)
			function, _ := call["function"].(map[string]any)
			for _, field := range []string{"name", "arguments"} {
				add(fmt.Sprintf("%v/tool%v/%s", choice["index"], call["index"], field), function[field])
			}
		}
	}
}

// endsNormally reports whether a stream its format's check accepts ends the way that format ends normally, not with
// an error.
func endsNormally(f protocol.Format, stream []byte) bool {
	r := sse.NewReader(bytes.NewReader(stream), 32<<20)
	var last []byte
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		if f == protocol.Chat && bytes.Contains(data, []byte(`"error"`)) && !bytes.Contains(data, []byte(`"error":null`)) {
			return false
		}
		last = append(last[:0], data...)
	}
	switch f {
	case protocol.Messages:
		return bytes.Contains(last, []byte(`"message_stop"`))
	case protocol.Responses:
		return bytes.Contains(last, []byte(`"response.completed"`)) || bytes.Contains(last, []byte(`"response.incomplete"`))
	case protocol.Chat:
		return string(last) == sse.Done
	}
	return false
}
