package protocol_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
)

//nolint:exhaustive // the legacy formats are the ones with a conversion
func legacyReplyStream(f protocol.Format) string {
	switch f {
	case protocol.Messages:
		return messageStart(`{}`) + startBlock(0, `{"type":"tool_use","id":"c1","name":"f","input":{}}`) + stopBlock(0) + stopWith("tool_use")
	case protocol.Responses:
		return rev("response.created", responseObject) + rev("response.output_item.done", `,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","arguments":"{}","call_id":"c1","name":"f"}`) + ended("response.completed", "")
	case protocol.Chat:
		return chatPiece(`{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"f","arguments":"{"}}]}`, "null") + chatPiece(`{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]}`, `"tool_calls"`) + ev("[DONE]")
	}
	panic("unknown format")
}

func runLegacyReply(t *testing.T, f protocol.Format, src io.Reader, ctx protocol.UsageContext, aggregate bool, mem *protocol.Memory) ([]byte, error) {
	t.Helper()
	before := mem.Left()
	o := options
	o.Aggregate = aggregate
	o.Memory = mem
	r := protocol.NewRelay(protocol.Chat, f, src, ctx, o)
	var out []byte
	for {
		step, err := r.Next()
		if err != nil {
			out = append(out, r.Fail(protocol.Error{Type: protocol.UpstreamFailed, Message: "failed"}, nil)...)
			r.Release()
			if mem.Left() != before {
				t.Fatalf("memory leaked: %d -> %d", before, mem.Left())
			}
			return out, err
		}
		out = append(out, step.Out...)
		if step.Kind == protocol.StepUpstreamDone {
			out = append(out, r.Finish(settled(r.Usage()))...)
			r.Release()
			if mem.Left() != before {
				t.Fatalf("memory leaked: %d -> %d", before, mem.Left())
			}
			return out, nil
		}
	}
}

func TestLegacyRepliesFollowClient(t *testing.T) {
	t.Parallel()
	for _, fields := range []struct {
		name, body string
		legacy     bool
	}{
		{"legacy", `"functions":[{"name":"f"}]`, true},
		{"legacy choice", `"function_call":"auto"`, true},
		{"modern", `"tools":[{"type":"function","function":{"name":"f"}}]`, false},
		{"mixed", `"tools":[{"type":"function","function":{"name":"g"}}],"functions":[{"name":"f"}]`, false},
	} {
		body := []byte(`{"model":"m","messages":[],` + fields.body + `}`)
		req := inspected(t, protocol.Chat, body)
		prepared, err := protocol.Prepare(&req, body, protocol.Target{Format: protocol.Chat, Model: "u"}, rewrites())
		if err != nil {
			t.Fatal(err)
		}
		checkLegacyReplyFormats(t, fields.name, prepared.Usage, fields.legacy)
	}
}

//nolint:exhaustive // the formats that can carry parallel calls are the ones tested
func TestLegacyReplyRejectsParallelCalls(t *testing.T) {
	t.Parallel()
	for _, aggregate := range []bool{false, true} {
		for _, f := range []protocol.Format{protocol.Chat, protocol.Messages, protocol.Responses} {
			stream := legacyReplyStream(f)
			switch f {
			case protocol.Chat:
				stream = strings.Replace(stream, `"index":0,"id":"c1"`, `"index":1,"id":"c1"`, 1)
			case protocol.Messages:
				stream = strings.Replace(stream, stopWith("tool_use"), startBlock(1, `{"type":"tool_use","id":"c2","name":"g","input":{}}`)+stopBlock(1)+stopWith("tool_use"), 1)
			case protocol.Responses:
				stream = strings.Replace(stream, ended("response.completed", ""), rev("response.output_item.done", `,"output_index":1,"item":{"id":"fc_2","type":"function_call","status":"completed","arguments":"{}","call_id":"c2","name":"g"}`)+ended("response.completed", ""), 1)
			}
			_, err := runLegacyReply(t, f, strings.NewReader(stream), protocol.UsageContext{LegacyFunctions: true}, aggregate, protocol.NewMemory(4<<20))
			if !errors.Is(err, protocol.ErrUpstream) {
				t.Fatalf("%d/%t: expected parallel-call failure, got %v", f, aggregate, err)
			}
		}
	}
}

func checkLegacyReply(t *testing.T, out []byte, legacy, aggregate bool) {
	t.Helper()
	field, finish := "tool_calls", "tool_calls"
	if legacy {
		field, finish = "function_call", "function_call"
	}
	if !bytes.Contains(out, []byte(`"`+field+`":`)) || !bytes.Contains(out, []byte(`"finish_reason":"`+finish+`"`)) {
		t.Fatalf("wrong reply shape: %s", out)
	}
	if legacy && bytes.Contains(out, []byte(`"tool_calls"`)) {
		t.Fatalf("modern call leaked: %s", out)
	}

	if !legacy {
		return
	}
	if aggregate {
		checkLegacyReplyBody(t, out)
	} else {
		checkLegacyReplyStream(t, out)
	}
}

func checkLegacyReplyBody(t *testing.T, out []byte) {
	t.Helper()

	msg := decode(t, out)["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	{
		call := msg["function_call"].(map[string]any)
		if call["name"] != "f" || call["arguments"] != "{}" {
			t.Fatalf("bad call: %v", call)
		}
	}
}

func checkLegacyReplyStream(t *testing.T, out []byte) {
	t.Helper()

	reader := sse.NewReader(bytes.NewReader(out), 1<<20)
	var name, args string
	for {
		data, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if string(data) == "[DONE]" {
			continue
		}
		n, a := legacyReplyPieces(t, data)
		name += n
		args += a
	}
	if name != "f" || args != "{}" {
		t.Fatalf("lost fragments: %q %q", name, args)
	}
}

func checkLegacyReplyFormats(t *testing.T, name string, ctx protocol.UsageContext, legacy bool) {
	t.Helper()
	for _, f := range []protocol.Format{protocol.Chat, protocol.Messages, protocol.Responses} {
		for _, aggregate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%d/aggregate=%t", name, f, aggregate), func(t *testing.T) {
				stream := legacyReplyStream(f)
				out, err := runLegacyReply(t, f, strings.NewReader(stream), ctx, aggregate, protocol.NewMemory(4<<20))
				if err != nil {
					t.Fatal(err)
				}
				split, err := runLegacyReply(t, f, iotest.OneByteReader(strings.NewReader(stream)), ctx, aggregate, protocol.NewMemory(4<<20))
				if err != nil || !bytes.Equal(out, split) {
					t.Fatalf("bytewise output differs: %v\n%s\n%s", err, out, split)
				}
				checkLegacyReply(t, out, legacy, aggregate)
			})
		}
	}
}

func TestLegacyEscapedReplyControls(t *testing.T) {
	t.Parallel()
	stream := strings.ReplaceAll(legacyReplyStream(protocol.Chat), `"tool_calls"`, `"tool_c\u0061lls"`)
	for _, aggregate := range []bool{false, true} {
		out, err := runLegacyReply(t, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{LegacyFunctions: true}, aggregate, protocol.NewMemory(4<<20))
		if err != nil {
			t.Fatal(err)
		}
		checkLegacyReply(t, out, true, aggregate)
	}
}

func TestLegacyReplyBudget(t *testing.T) {
	t.Parallel()
	stream := chatPiece(`{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"f","arguments":"`+strings.Repeat("x", 128<<10)+`"}}]}`, `"tool_calls"`) + ev("[DONE]")
	for _, aggregate := range []bool{false, true} {
		_, err := runLegacyReply(t, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{LegacyFunctions: true}, aggregate, protocol.NewMemory(0))
		if !errors.Is(err, protocol.ErrMemory) {
			t.Fatalf("got %v, want budget exhaustion", err)
		}
	}
}

func TestLegacyReplyToolType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind    string
		invalid bool
	}{
		{`"custom"`, true}, {`"funct\u0069on"`, false}, {`""`, false}, {`null`, false},
	} {
		for _, later := range []bool{false, true} {
			stream := legacyReplyStream(protocol.Chat)
			if later {
				stream = strings.Replace(stream, `"index":0,"function":{"arguments":"}"}`, `"index":0,"type":`+tc.kind+`,"function":{"arguments":"}"}`, 1)
			} else {
				stream = strings.Replace(stream, `"type":"function"`, `"type":`+tc.kind, 1)
			}
			checkLegacyReplyToolType(t, stream, tc.kind, tc.invalid)
		}
	}
}

func checkLegacyReplyToolType(t *testing.T, stream, kind string, invalid bool) {
	t.Helper()
	for _, aggregate := range []bool{false, true} {
		out, err := runLegacyReply(t, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{LegacyFunctions: true}, aggregate, protocol.NewMemory(4<<20))
		if errors.Is(err, protocol.ErrUpstream) != invalid {
			t.Fatalf("%s/%t: %v", kind, aggregate, err)
		}
		if !invalid {
			checkLegacyReply(t, out, true, aggregate)
		}
	}
}

func TestLegacyReplyDropsNullFunctionCall(t *testing.T) {
	t.Parallel()
	stream := strings.ReplaceAll(legacyReplyStream(protocol.Chat), `"tool_calls":`, `"function_call":null,"tool_calls":`)
	checkLegacyReplyToolType(t, stream, "null function_call", false)
}

func legacyReplyPieces(t *testing.T, data []byte) (name, args string) {
	t.Helper()
	choices, _ := decode(t, data)["choices"].([]any)
	for _, raw := range choices {
		delta, _ := raw.(map[string]any)["delta"].(map[string]any)
		call, _ := delta["function_call"].(map[string]any)
		if s, ok := call["name"].(string); ok {
			name += s
		}
		if s, ok := call["arguments"].(string); ok {
			args += s
		}
	}
	return name, args
}
