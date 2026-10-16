package protocol_test

import (
	"bytes"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
	"axiom/internal/protocol/internal/streamcheck"
)

// TestConvertStreams converts every Messages and Responses sample, and a Responses stream assembled from the event
// shapes with reasoning and a function call, for a Chat client, whole and byte by byte, and compares the client's
// stream with its golden copy, to-chat.sse, which go test -update rewrites. Every client stream passes Chat's
// stream check.
func TestConvertStreams(t *testing.T) {
	type stream struct {
		name, golden string
		from         protocol.Format
	}
	streams := []stream{{"assembled/responses-reasoning-tool.sse", "assembled/responses-reasoning-tool.to-chat.sse", protocol.Responses}}
	for _, format := range formats[:2] {
		dirs, err := fs.Glob(samples, format.dir+"/*")
		if err != nil || len(dirs) == 0 {
			t.Fatalf("no %s samples: %v", format.dir, err)
		}
		for _, dir := range dirs {
			streams = append(streams, stream{dir + "/upstream.sse", dir + "/to-chat.sse", format.f})
		}
	}
	for _, c := range streams {
		t.Run(c.name, func(t *testing.T) {
			upstream := sample(t, c.name)
			got := relayTo(t, protocol.Chat, c.from, bytes.NewReader(upstream), protocol.UsageContext{},
				protocol.NewMemory(0), settled)
			bytewise := relayTo(t, protocol.Chat, c.from, iotest.OneByteReader(bytes.NewReader(upstream)),
				protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if got.err != nil || !bytes.Equal(bytewise.out, got.out) {
				t.Fatalf("error %v; byte by byte:\n%s\nwhole:\n%s", got.err, bytewise.out, got.out)
			}
			if err := streamcheck.Chat(bytes.NewReader(got.out)); err != nil {
				t.Error(err)
			}
			golden(t, c.golden, got.out)
		})
	}
}

// ev is one SSE event with data d.
func ev(d string) string { return "data: " + d + "\n\n" }

// Messages events to build converted streams from.
func startBlock(i int, block string) string {
	return ev(`{"type":"content_block_start","index":` + strconv.Itoa(i) + `,"content_block":` + block + `}`)
}

func deltaOf(i int, delta string) string {
	return ev(`{"type":"content_block_delta","index":` + strconv.Itoa(i) + `,"delta":` + delta + `}`)
}

func stopBlock(i int) string {
	return ev(`{"type":"content_block_stop","index":` + strconv.Itoa(i) + `}`)
}

func stopWith(reason string) string {
	return ev(`{"type":"message_delta","delta":{"stop_reason":"`+reason+`"},"usage":{"output_tokens":2}}`) +
		ev(`{"type":"message_stop"}`)
}

func thinkingBlock(i int, text, sign string) string {
	return startBlock(i, `{"type":"thinking","thinking":"","signature":""}`) +
		deltaOf(i, `{"type":"thinking_delta","thinking":"`+text+`"}`) +
		deltaOf(i, `{"type":"signature_delta","signature":"`+sign+`"}`) + stopBlock(i)
}

func textBlockAt(i int, text string) string {
	return startBlock(i, `{"type":"text","text":""}`) + deltaOf(i, `{"type":"text_delta","text":"`+text+`"}`) +
		stopBlock(i)
}

// TestConvertMessages: each case is a Messages stream after message_start and what a Chat client gets of it, by
// the keys of contents, or the error the conversion ends with.
func TestConvertMessages(t *testing.T) {
	start := messageStart(`{"input_tokens":3}`)
	for name, c := range map[string]struct {
		stream string
		want   map[string]string
	}{
		"only leading thinking keeps its signature and redacted thinking": {
			thinkingBlock(0, "a", "S1") + startBlock(1, `{"type":"redacted_thinking","data":"R1"}`) + stopBlock(1) +
				textBlockAt(2, "hi") + thinkingBlock(3, "b", "S2") +
				startBlock(4, `{"type":"redacted_thinking","data":"R2"}`) + stopBlock(4) + stopWith("end_turn"),
			map[string]string{
				"0/role": "assistant", "0/content": "hi", "0/reasoning": "ab", "0/finish_reason": "stop",
				"0/reasoning_details": `[map[format:anthropic-claude-v1 index:0 text:a type:reasoning.text]]` +
					`[map[format:anthropic-claude-v1 index:0 signature:S1 text: type:reasoning.text]]` +
					`[map[data:R1 format:anthropic-claude-v1 index:1 type:reasoning.encrypted]]` +
					`[map[format:anthropic-claude-v1 index:2 text:b type:reasoning.text]]`,
			},
		},
		"a tool's input without deltas is its arguments": {
			startBlock(0, `{"type":"tool_use","id":"toolu_1","name":"f","input":{"a":[1,2]}}`) + stopBlock(0) +
				startBlock(1, `{"type":"tool_use","id":"toolu_2","name":"g","input":{}}`) +
				deltaOf(1, `{"type":"input_json_delta","partial_json":"{\"b\":"}`) +
				deltaOf(1, `{"type":"input_json_delta","partial_json":"2}"}`) + stopBlock(1) + stopWith("tool_use"),
			map[string]string{
				"0/role": "assistant", "0/tool0/id": "toolu_1", "0/tool0/type": "function", "0/tool0/name": "f",
				"0/tool0/arguments": `{"a":[1,2]}`, "0/tool1/id": "toolu_2", "0/tool1/type": "function",
				"0/tool1/name": "g", "0/tool1/arguments": `{"b":2}`, "0/finish_reason": "tool_calls",
			},
		},
		"stop_sequence":                 {textBlockAt(0, "x") + stopWith("stop_sequence"), finished("stop")},
		"max_tokens":                    {textBlockAt(0, "x") + stopWith("max_tokens"), finished("length")},
		"model_context_window_exceeded": {textBlockAt(0, "x") + stopWith("model_context_window_exceeded"), finished("length")},
		"refusal":                       {textBlockAt(0, "x") + stopWith("refusal"), finished("content_filter")},
		"pause_turn":                    {textBlockAt(0, "x") + stopWith("pause_turn"), nil},
		"an unknown stop reason":        {textBlockAt(0, "x") + stopWith("compaction"), nil},
		"no stop reason":                {textBlockAt(0, "x") + ev(`{"type":"message_stop"}`), nil},
		"a server tool's block": {
			startBlock(0, `{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}`) + stopBlock(0) +
				stopWith("end_turn"), nil,
		},
		"a citation": {
			startBlock(0, `{"type":"text","text":""}`) + deltaOf(0, `{"type":"citations_delta","citation":{}}`) +
				stopBlock(0) + stopWith("end_turn"), nil,
		},
		"content after the stop reason": {
			textBlockAt(0, "x") + ev(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{}}`) +
				textBlockAt(1, "y") + ev(`{"type":"message_stop"}`), nil,
		},
		"a delta for a block that is not open": {
			deltaOf(0, `{"type":"text_delta","text":"x"}`) + stopWith("end_turn"), nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := relayTo(t, protocol.Chat, protocol.Messages, strings.NewReader(start+c.stream),
				protocol.UsageContext{}, protocol.NewMemory(0), settled)
			checkConverted(t, got, c.want)
		})
	}
}

// finished is what a client gets of a text block x that finished with reason.
func finished(reason string) map[string]string {
	return map[string]string{"0/role": "assistant", "0/content": "x", "0/finish_reason": reason}
}

// checkConverted checks a converted stream: with want, it ends normally and says what want says; without, it fails
// as the upstream's failure. Either way the client's stream passes Chat's check.
func checkConverted(t *testing.T, got relayed, want map[string]string) {
	t.Helper()
	if err := streamcheck.Chat(bytes.NewReader(got.out)); err != nil {
		t.Errorf("%v:\n%s", err, got.out)
	}
	if want == nil {
		if !errors.Is(got.err, protocol.ErrUpstream) {
			t.Errorf("error %v, want the upstream's failure:\n%s", got.err, got.out)
		}
		return
	}
	if got.err != nil {
		t.Fatalf("error %v:\n%s", got.err, got.out)
	}
	if diff := cmp.Diff(want, contents(t, protocol.Chat, got.out)); diff != "" {
		t.Errorf("contents (-want +got):\n%s\n%s", diff, got.out)
	}
}

// TestConvertKeepAlive: a Messages ping after the commit point reaches a Chat client as Chat's keep-alive, a comment.
func TestConvertKeepAlive(t *testing.T) {
	stream := messageStart(`{}`) + textBlockAt(0, "x") + ev(`{"type":"ping"}`) + stopWith("end_turn")
	got := relayTo(t, protocol.Chat, protocol.Messages, strings.NewReader(stream), protocol.UsageContext{},
		protocol.NewMemory(0), settled)
	if got.err != nil || !bytes.Contains(got.out, protocol.KeepAlive(protocol.Chat)) || bytes.Contains(got.out, []byte("ping")) {
		t.Errorf("error %v:\n%s", got.err, got.out)
	}
}

// Responses events to build converted streams from.
func rev(typ, rest string) string { return ev(`{"type":"` + typ + `"` + rest + `}`) }

const (
	responseObject = `,"response":{"id":"resp_1","object":"response","status":"in_progress","model":"m","output":[],"usage":null}`
	messageAdded   = `,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}`
)

func textDone(text string) string {
	return rev("response.output_text.done", `,"output_index":0,"content_index":0,"text":"`+text+`"`)
}

func messageDone(text string) string {
	return rev("response.output_item.done", `,"output_index":0,"item":{"id":"msg_1","type":"message","status":"completed",`+
		`"role":"assistant","content":[{"type":"output_text","text":"`+text+`","annotations":[]}]}`)
}

func textDelta(text string) string {
	return rev("response.output_text.delta", `,"output_index":0,"content_index":0,"delta":"`+text+`"`)
}

func ended(typ, rest string) string {
	return rev(typ, strings.Replace(responseObject, `"usage":null`, `"usage":{"input_tokens":3,"output_tokens":2}`+rest, 1))
}

// TestConvertResponses: each case is a Responses stream after response.created and what a Chat client gets of it,
// or the error the conversion ends with. A part given whole is compared with its deltas.
func TestConvertResponses(t *testing.T) {
	completed := ended("response.completed", "")
	for name, c := range map[string]struct {
		stream string
		want   map[string]string
	}{
		"snapshots that repeat the deltas": {
			rev("response.output_item.added", messageAdded) + textDelta("ab") + textDelta("c") + textDone("abc") +
				messageDone("abc") + completed, map[string]string{"0/role": "assistant", "0/content": "abc", "0/finish_reason": "stop"},
		},
		"a snapshot that goes past the deltas": {
			rev("response.output_item.added", messageAdded) + textDelta("ab") + textDone("abc") + messageDone("abc") + completed,
			map[string]string{"0/role": "assistant", "0/content": "abc", "0/finish_reason": "stop"},
		},
		"an item given only whole": {
			messageDone("whole") + completed, map[string]string{"0/role": "assistant", "0/content": "whole", "0/finish_reason": "stop"},
		},
		"a snapshot that differs from the deltas": {
			rev("response.output_item.added", messageAdded) + textDelta("ab") + textDone("xbc") + completed, nil,
		},
		"a snapshot shorter than the deltas": {
			rev("response.output_item.added", messageAdded) + textDelta("abc") + textDone("ab") + completed, nil,
		},
		"incomplete for max_output_tokens": {
			messageDone("x") + ended("response.incomplete", `,"incomplete_details":{"reason":"max_output_tokens"}`),
			map[string]string{"0/role": "assistant", "0/content": "x", "0/finish_reason": "length"},
		},
		"incomplete for content_filter": {
			messageDone("x") + ended("response.incomplete", `,"incomplete_details":{"reason":"content_filter"}`),
			map[string]string{"0/role": "assistant", "0/content": "x", "0/finish_reason": "content_filter"},
		},
		"incomplete for another reason": {
			messageDone("x") + ended("response.incomplete", `,"incomplete_details":{"reason":"interrupted"}`), nil,
		},
		"an annotation": {
			rev("response.output_item.added", messageAdded) + textDelta("x") +
				rev("response.output_text.annotation.added", `,"output_index":0,"content_index":0,"annotation":{}`) + completed, nil,
		},
		"a server tool's item": {
			rev("response.output_item.added", `,"output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}`) +
				completed, nil,
		},
		"a function call given only whole": {
			rev("response.output_item.done", `,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed",`+
				`"arguments":"{}","call_id":"call_9","name":"f"}`) + completed,
			map[string]string{
				"0/role": "assistant", "0/tool0/id": "call_9", "0/tool0/type": "function", "0/tool0/name": "f",
				"0/tool0/arguments": "{}", "0/finish_reason": "tool_calls",
			},
		},
		"a refusal": {
			rev("response.output_item.added", messageAdded) +
				rev("response.refusal.delta", `,"output_index":0,"content_index":0,"delta":"no"`) +
				rev("response.refusal.done", `,"output_index":0,"content_index":0,"refusal":"no"`) + completed,
			map[string]string{"0/role": "assistant", "0/refusal": "no", "0/finish_reason": "stop"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			stream := rev("response.created", responseObject) + c.stream
			got := relayTo(t, protocol.Chat, protocol.Responses, strings.NewReader(stream), protocol.UsageContext{},
				protocol.NewMemory(0), settled)
			checkConverted(t, got, c.want)
		})
	}
}

// FuzzConvertStream: a Messages or Responses stream its format's check accepts converts the same however it is cut
// into reads, into a stream Chat's check accepts, and fails only as the upstream's failure; a Messages stream that
// converts gives the client its text; aggregated, it is the completion that stream folds to (checkAggregate).
func FuzzConvertStream(f *testing.F) {
	for i, format := range formats[:2] {
		for _, dir := range must(fs.Glob(samples, format.dir+"/*")) {
			f.Add(uint8(i), sample(f, dir+"/upstream.sse"), []byte{5, 200, 3})
		}
	}
	f.Add(uint8(0), []byte(messageStart(`{}`)+thinkingBlock(0, "a", "S")+textBlockAt(1, "x")+stopWith("end_turn")), []byte{9})
	f.Add(uint8(1), []byte(rev("response.created", responseObject)+messageDone("w")+ended("response.completed", "")), []byte{7})
	f.Add(uint8(0), []byte(messageStart(`{}`)+startBlock(0, `{"type":"text_delta","text":"0"}`)+stopBlock(0)+stopWith("end_turn")), []byte{3})
	f.Fuzz(func(t *testing.T, which uint8, stream, cuts []byte) {
		format := formats[int(which)%2]
		if format.check(bytes.NewReader(stream)) != nil {
			return
		}
		whole := relayTo(t, protocol.Chat, format.f, bytes.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
		split := relayTo(t, protocol.Chat, format.f, &chunks{stream, cuts}, protocol.UsageContext{}, protocol.NewMemory(64<<20), settled)
		switch {
		case !bytes.Equal(whole.out, split.out):
			t.Fatalf("split %v:\n%s\nwhole:\n%s", cuts, split.out, whole.out)
		case whole.err != nil && !errors.Is(whole.err, protocol.ErrUpstream):
			t.Fatalf("error %v", whole.err)
		}
		if err := streamcheck.Chat(bytes.NewReader(whole.out)); err != nil {
			t.Fatalf("client stream: %v\n%s", err, whole.out)
		}
		if format.f == protocol.Messages && whole.err == nil && decodes(stream) {
			checkText(t, stream, whole.out)
		}
		checkAggregate(t, protocol.Chat, format.f, stream, cuts, whole)
	})
}

// checkText checks that a Chat client got the text of the Messages stream it was converted from.
func checkText(t *testing.T, stream, out []byte) {
	t.Helper()
	if got, want := contents(t, protocol.Chat, out)["0/content"], messagesText(t, stream); got != want {
		t.Fatalf("text %q, want %q", got, want)
	}
}

// messagesText is the text of a Messages stream's text blocks, in order.
func messagesText(t *testing.T, stream []byte) string {
	t.Helper()
	var text strings.Builder
	r := sse.NewReader(bytes.NewReader(stream), 32<<20)
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		type part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		var e struct {
			Type  string `json:"type"`
			Block part   `json:"content_block"`
			Delta part   `json:"delta"`
		}
		if jsonx.Unmarshal(data, &e) != nil {
			continue
		}
		if e.Type == "content_block_start" && e.Block.Type == "text" {
			text.WriteString(e.Block.Text)
		}
		if e.Type == "content_block_delta" && e.Delta.Type == "text_delta" {
			text.WriteString(e.Delta.Text)
		}
	}
	return text.String()
}

// TestConvertToolCallFields: a tool call the upstream starts without its id or name, or with either null or empty, is
// the upstream's failure, before any of it reaches the client: written as it is, the call would be invalid JSON.
func TestConvertToolCallFields(t *testing.T) {
	for _, format := range []struct {
		name, id string // the format's name and its id member
		stream   func(members string) string
		from     protocol.Format
	}{
		{"Messages", "id", func(members string) string {
			return messageStart(`{}`) + startBlock(0, `{"type":"tool_use","input":{}`+members+`}`) + stopBlock(0) + stopWith("tool_use")
		}, protocol.Messages},
		{"Responses", "call_id", func(members string) string {
			return rev("response.created", responseObject) + rev("response.output_item.added",
				`,"output_index":0,"item":{"type":"function_call","arguments":""`+members+`}`) + ended("response.completed", "")
		}, protocol.Responses},
	} {
		for _, spelling := range []struct{ name, value string }{{"missing", ""}, {"null", "null"}, {"empty", `""`}} {
			for _, key := range []string{format.id, "name"} {
				members := map[string]string{format.id: `,"` + format.id + `":"t1"`, "name": `,"name":"f"`}
				members[key] = ""
				if spelling.value != "" {
					members[key] = `,"` + key + `":` + spelling.value
				}
				t.Run(format.name+" "+key+" "+spelling.name, func(t *testing.T) {
					got := relayTo(t, protocol.Chat, format.from, strings.NewReader(format.stream(members[format.id]+members["name"])),
						protocol.UsageContext{}, protocol.NewMemory(0), settled)
					checkConverted(t, got, nil)
				})
			}
		}
	}
}

// TestConvertProgress: the meter has counted what the client was given by the time the event is written, whether the
// upstream gave it as deltas or only in a snapshot; a snapshot that repeats the deltas adds nothing, and an item that
// is done adds only what its pieces did not cover, so the stream counts as much as the same stream relayed to a
// Responses client. Each case ends right after the event, as a stream that breaks does.
func TestConvertProgress(t *testing.T) {
	functionAdded := func(args string) string {
		return rev("response.output_item.added", `,"output_index":0,"item":{"id":"fc_1","type":"function_call",`+
			`"call_id":"c1","name":"f","arguments":"`+args+`"}`)
	}
	reasoningAdded := rev("response.output_item.added", `,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}`)
	for name, c := range map[string]struct {
		stream string
		want   int64
	}{
		"a text only in its done event": {rev("response.output_item.added", messageAdded) + textDone("hello"), 5},
		"a text part only in its part": {rev("response.output_item.added", messageAdded) +
			rev("response.content_part.done", `,"output_index":0,"content_index":0,"part":{"type":"output_text","text":"héy"}`), 4},
		"a refusal only in its done event": {rev("response.output_item.added", messageAdded) +
			rev("response.refusal.done", `,"output_index":0,"content_index":0,"refusal":"no"`), 2},
		"arguments in the added item": {functionAdded(`{\"a\":1}`), 7},
		"arguments only in their done event": {functionAdded("") +
			rev("response.function_call_arguments.done", `,"output_index":0,"arguments":"{}"`), 2},
		"a summary only in its done event": {reasoningAdded +
			rev("response.reasoning_summary_text.done", `,"output_index":0,"summary_index":0,"text":"why"`), 3},
		"reasoning text only in its done event": {reasoningAdded +
			rev("response.reasoning_text.done", `,"output_index":0,"content_index":0,"text":"why"`), 3},
		"a snapshot past the deltas": {rev("response.output_item.added", messageAdded) + textDelta("ab") + textDone("abc"), 3},
		"a snapshot that repeats the deltas": {rev("response.output_item.added", messageAdded) + textDelta("ab") + textDelta("c") +
			textDone("abc") + textDone("abc"), 3},
	} {
		t.Run(name, func(t *testing.T) {
			stream := rev("response.created", responseObject) + c.stream
			if got := progressAfter(t, protocol.Chat, protocol.Responses, stream); got != c.want {
				t.Errorf("content bytes %d, want %d", got, c.want)
			}
		})
	}
	for name, stream := range map[string]string{
		"deltas, snapshots and the item": rev("response.output_item.added", messageAdded) + textDelta("ab") + textDone("abc") +
			messageDone("abc"),
		"only the item": messageDone("whole"),
	} {
		t.Run(name+" count what a Responses client's does", func(t *testing.T) {
			stream = rev("response.created", responseObject) + stream
			if conv, native := progressAfter(t, protocol.Chat, protocol.Responses, stream),
				progressAfter(t, protocol.Responses, protocol.Responses, stream); conv != native || conv == 0 {
				t.Errorf("content bytes %d converted, %d relayed", conv, native)
			}
		})
	}
}

// progressAfter relays the events of stream and returns the content bytes counted when the stream ends, or breaks.
func progressAfter(t *testing.T, client, upstream protocol.Format, stream string) int64 {
	t.Helper()
	o := options
	o.Memory = protocol.NewMemory(0)
	r := protocol.NewRelay(client, upstream, strings.NewReader(stream), protocol.UsageContext{}, o)
	for step, err := r.Next(); err == nil && step.Kind != protocol.StepUpstreamDone; step, err = r.Next() {
	}
	return r.Progress().ContentBytes
}
