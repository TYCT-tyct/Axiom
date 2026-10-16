package protocol_test

import (
	"bytes"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
)

// foldStream is the reference for the aggregators, written apart from them: it folds the stream a client of format f
// gets into the reply a client that did not ask for a stream gets, on decoded values and by the rules of
// README.md「非流式聚合」, so the two agree only if both fold the same way.
func foldStream(t testing.TB, f protocol.Format, stream []byte) map[string]any {
	t.Helper()
	var events []map[string]any
	r := sse.NewReader(bytes.NewReader(stream), 32<<20)
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		if string(data) == sse.Done {
			continue
		}
		var e map[string]any
		if err := jsonx.Unmarshal(data, &e); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		events = append(events, e)
	}
	switch f {
	case protocol.Messages:
		return foldMessages(events)
	case protocol.Chat:
		return foldChat(events)
	case protocol.Responses:
	}
	for _, e := range events {
		if typ, _ := e["type"].(string); typ == "response.completed" || typ == "response.incomplete" {
			return obj(e["response"])
		}
	}
	t.Fatal("no response.completed")
	return nil
}

// obj is v as an object, an empty one when it is not, which reads as one without members.
func obj(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

func objects(v any) []map[string]any {
	var out []map[string]any
	for _, item := range listOf(v) {
		out = append(out, obj(item))
	}
	return out
}

// messagesFold is a Messages stream as it folds: the message, its blocks, and the partial_json so far of each.
type messagesFold struct {
	msg     map[string]any
	blocks  []any
	partial map[int]string
}

func foldMessages(events []map[string]any) map[string]any {
	f := &messagesFold{msg: map[string]any{"stop_reason": nil, "stop_sequence": nil}, blocks: []any{}, partial: map[int]string{}}
	for _, e := range events {
		i, _ := e["index"].(float64)
		var b map[string]any
		if int(i) < len(f.blocks) {
			b = obj(f.blocks[int(i)])
		}
		switch e["type"] {
		case "message_start":
			maps.Copy(f.msg, obj(e["message"]))
			f.msg["usage"] = maps.Clone(obj(obj(e["message"])["usage"]))
			delete(f.msg, "content")
		case "content_block_start":
			f.blocks = append(f.blocks, maps.Clone(obj(e["content_block"])))
		case "content_block_delta":
			f.delta(int(i), b, obj(e["delta"]))
		case "content_block_stop":
			f.stop(int(i), b)
		case "message_delta":
			f.end(e)
		}
	}
	f.msg["content"] = f.blocks
	return f.msg
}

func (f *messagesFold) delta(i int, b, d map[string]any) {
	if b == nil {
		return
	}
	switch d["type"] {
	case "text_delta":
		b["text"] = text(b["text"]) + text(d["text"])
	case "thinking_delta":
		b["thinking"] = text(b["thinking"]) + text(d["thinking"])
	case "signature_delta":
		b["signature"] = text(b["signature"]) + text(d["signature"])
	case "input_json_delta":
		f.partial[i] += text(d["partial_json"])
	case "citations_delta":
		b["citations"] = append(slices.Clone(listOf(b["citations"])), d["citation"])
	}
}

func (f *messagesFold) stop(i int, b map[string]any) {
	if b == nil {
		return
	}
	if p := f.partial[i]; p != "" {
		var input any
		if err := jsonx.Unmarshal([]byte(p), &input); err == nil {
			b["input"] = input
		}
	}
	if _, has := b["input"]; strings.HasSuffix(text(b["type"]), "tool_use") && !has {
		b["input"] = map[string]any{}
	}
}

func (f *messagesFold) end(e map[string]any) {
	maps.Copy(f.msg, obj(e["delta"]))
	if f.msg["usage"] == nil {
		f.msg["usage"] = map[string]any{}
	}
	maps.Copy(obj(f.msg["usage"]), obj(e["usage"]))
	if e["axiom"] != nil {
		f.msg["axiom"] = e["axiom"]
	}
}

// foldedChoice is a Chat choice as the fold gathers it.
type foldedChoice struct {
	index                                         int
	content, refusal, reasoning, reasoningContent string
	details                                       []map[string]any
	annotations                                   []any
	tools                                         []map[string]any
	toolAt                                        map[int]int
	finish                                        any
	logs                                          [2][]any
	logSeen                                       [2]bool
}

func foldChat(chunks []map[string]any) map[string]any {
	out := map[string]any{"object": "chat.completion"}
	choices := map[int]*foldedChoice{}
	for _, c := range chunks {
		keepMembers(out, c)
		for _, item := range listOf(c["choices"]) {
			choice := obj(item)
			i, _ := choice["index"].(float64)
			if choices[int(i)] == nil {
				choices[int(i)] = &foldedChoice{index: int(i), toolAt: map[int]int{}}
			}
			choices[int(i)].add(choice)
		}
	}
	list := []any{}
	for _, fc := range slices.SortedFunc(maps.Values(choices), func(a, b *foldedChoice) int { return a.index - b.index }) {
		list = append(list, fc.choice(out["error"] != nil))
	}
	out["choices"] = list
	if _, has := out["created"]; !has {
		out["created"] = 0.0
	}
	return out
}

// keepMembers takes the members of a chunk that are not choices: the first chunk's identity, the last usage, error
// and axiom.
func keepMembers(out, chunk map[string]any) {
	for _, name := range []string{"id", "created", "model"} {
		if _, has := out[name]; !has && chunk[name] != nil {
			out[name] = chunk[name]
		}
	}
	for _, name := range []string{"usage", "error", "axiom"} {
		if chunk[name] != nil {
			out[name] = chunk[name]
		}
	}
}

// add folds a choice of a chunk into the choice.
func (fc *foldedChoice) add(choice map[string]any) {
	if choice["finish_reason"] != nil {
		fc.finish = choice["finish_reason"]
	}
	for j, name := range []string{"content", "refusal"} {
		if l, ok := obj(choice["logprobs"])[name].([]any); ok {
			fc.logSeen[j] = true
			fc.logs[j] = append(append([]any{}, fc.logs[j]...), l...)
		}
	}
	delta := obj(choice["delta"])
	fc.content += text(delta["content"])
	fc.refusal += text(delta["refusal"])
	fc.reasoning += text(delta["reasoning"])
	fc.reasoningContent += text(delta["reasoning_content"])
	fc.annotations = append(fc.annotations, listOf(delta["annotations"])...)
	fc.details = append(fc.details, objects(delta["reasoning_details"])...)
	for _, call := range objects(delta["tool_calls"]) {
		fc.addTool(call)
	}
}

func (fc *foldedChoice) addTool(call map[string]any) {
	i, _ := call["index"].(float64)
	at, seen := fc.toolAt[int(i)]
	if !seen {
		at = len(fc.tools)
		fc.toolAt[int(i)] = at
		fc.tools = append(fc.tools, map[string]any{"type": "function", "function": map[string]any{"name": "", "arguments": ""}})
	}
	tool, fn := fc.tools[at], obj(fc.tools[at]["function"])
	for _, name := range []string{"id", "type"} {
		if call[name] != nil {
			tool[name] = call[name]
		}
	}
	fn["name"] = text(fn["name"]) + text(obj(call["function"])["name"])
	fn["arguments"] = text(fn["arguments"]) + text(obj(call["function"])["arguments"])
}

// choice makes the choice of a completion from the pieces: those that were given, in the members OpenAI's message has.
func (fc *foldedChoice) choice(failed bool) map[string]any {
	finish := fc.finish
	if finish == nil && failed {
		finish = "error"
	}
	return map[string]any{
		"index": float64(fc.index), "message": fc.message(), "logprobs": fc.logprobs(), "finish_reason": finish,
	}
}

func (fc *foldedChoice) message() map[string]any {
	m := map[string]any{"role": "assistant", "content": nil, "refusal": nil}
	if fc.content != "" || len(fc.tools) == 0 {
		m["content"] = fc.content
	}
	for name, v := range map[string]string{"refusal": fc.refusal, "reasoning": fc.reasoning, "reasoning_content": fc.reasoningContent} {
		if v != "" {
			m[name] = v
		}
	}
	if d := mergeFolded(fc.details); len(d) > 0 {
		m["reasoning_details"] = d
	}
	if len(fc.annotations) > 0 {
		m["annotations"] = fc.annotations
	}
	if len(fc.tools) > 0 {
		m["tool_calls"] = fc.calls()
	}
	return m
}

// calls are the tool calls of the message, each with an ID.
func (fc *foldedChoice) calls() []any {
	calls := []any{}
	for _, tool := range fc.tools {
		if tool["id"] == nil {
			tool["id"] = ""
		}
		calls = append(calls, tool)
	}
	return calls
}

// logprobs is nil when no chunk had any, else the object with the entries of content and refusal, nil where none had.
func (fc *foldedChoice) logprobs() any {
	if !fc.logSeen[0] && !fc.logSeen[1] {
		return nil
	}
	logprobs := map[string]any{"content": nil, "refusal": nil}
	for j, name := range []string{"content", "refusal"} {
		if fc.logSeen[j] {
			logprobs[name] = fc.logs[j]
		}
	}
	return logprobs
}

// mergeFolded joins the entries of the same type and index into one (OpenRouter《Reasoning tokens》, read
// 2026-10-15): the pieces of text and summary in order, the last signature, data and ID, the first format.
func mergeFolded(entries []map[string]any) []any {
	var out []any
	at := map[string]map[string]any{}
	for _, e := range entries {
		key := text(e["type"]) + "/" + string(must(jsonx.MarshalCanonical(e["index"])))
		m := at[key]
		if m == nil {
			m = map[string]any{"type": e["type"]}
			at[key] = m
			out = append(out, m)
		}
		for name, v := range e {
			switch {
			case v == nil || name == "type":
			case name == "text" || name == "summary":
				m[name] = text(m[name]) + text(v)
			case name == "format" && m[name] != nil:
			default:
				m[name] = v
			}
		}
	}
	return out
}

// checkAggregate holds the aggregate of a stream to the stream relayed. However the stream is cut the body is the same.
// An aggregate fails when the relay of the stream does. The other way it may fail too: a stream the format's check
// accepts can still be one no reply is made of, such as a message_start without a message, which the relay passes on
// and the aggregate refuses as the upstream's failure. One that does not fail is a 200 whose body is what folding the
// client's stream says (the official shape is for the samples to show: the fuzzer's replies need not have the members a
// real one has).
func checkAggregate(t *testing.T, client, upstream protocol.Format, stream, cuts []byte, streamed relayed) {
	t.Helper()
	whole := aggregate(t, client, upstream, bytes.NewReader(stream), protocol.NewMemory(64<<20), unsettled)
	split := aggregate(t, client, upstream, &chunks{stream, cuts}, protocol.NewMemory(64<<20), unsettled)
	if !bytes.Equal(whole.body, split.body) || whole.status != split.status {
		t.Fatalf("split %v:\n%s\nwhole:\n%s", cuts, split.body, whole.body)
	}
	if err := jsonx.Validate(whole.body); err != nil {
		t.Fatalf("%v:\n%s", err, whole.body)
	}
	refused := whole.err != nil || whole.status != http.StatusOK // an ending that makes no reply is written as a failure
	switch {
	case streamed.err != nil && whole.err == nil:
		t.Fatalf("aggregating succeeded, relaying ended with %v", streamed.err)
	case streamed.err == nil && errors.Is(whole.err, protocol.ErrMemory):
		t.Fatalf("aggregating ran out of memory, relaying did not: %v", whole.err)
	case refused || !decodes(stream):
		return
	}
	if diff := cmp.Diff(foldStream(t, client, streamed.out), decode(t, whole.body)); diff != "" {
		t.Fatalf("the body is not what the client's stream says (-stream +body):\n%s\n%s", diff, stream)
	}
}
