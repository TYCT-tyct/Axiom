package protocol_test

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/streamcheck"
)

// Chat chunks to build streams from, in the shape of the official example (testdata/chat/text).
const (
	chatHead  = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":7,"model":"gpt-upstream","choices":`
	chatRole  = chatHead + `[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n"
	chatHi    = chatHead + `[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}` + "\n\n"
	chatStop  = chatHead + `[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	chatDone  = "data: [DONE]\n\n"
	chatHello = chatRole + chatHi + chatStop + chatDone
)

func chatUsage(usage string) string { return chatHead + `[],"usage":` + usage + "}\n\n" }

// TestRelayChat: the chunk that finishes a choice is held until [DONE], so a stream that breaks off after it ends
// with an error rather than a success; every chunk names the call ID and the client's model; a stream without a
// usage chunk gets one with the usage and cost of the settlement; a relay's provider and cost do not reach the client.
func TestRelayChat(t *testing.T) {
	usage := `{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11,"cost":0.5,"is_byok":false}`
	for _, c := range []struct {
		name, stream string
		err          error
		// The client's chunks in order, as the type of each: role, content, finish, usage, done or error.
		want []string
	}{
		{
			"usage chunk", chatHello[:len(chatHello)-len(chatDone)] + chatUsage(usage) + chatDone, nil,
			[]string{"role", "content", "finish", "usage", "done"},
		},
		{"no usage chunk", chatHello, nil, []string{"role", "content", "finish", "usage", "done"}},
		{"broken after the finish", chatRole + chatHi + chatStop, protocol.ErrUpstream, []string{"role", "content", "error"}},
		{"[DONE] before a finish", chatRole + chatHi + chatDone, protocol.ErrUpstream, []string{"role", "content", "error"}},
		{
			"content, finish and usage in one chunk",
			chatRole + chatHead + `[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}],"usage":` + usage + "}\n\n" + chatDone,
			nil,
			[]string{"role", "content", "finish", "usage", "done"},
		},
		{
			"error chunk", chatRole + chatHi + `data: {"error":{"code":502,"message":"x"}}` + "\n\n", protocol.ErrUpstream,
			[]string{"role", "content", "error"},
		},
		{
			"provider", strings.ReplaceAll(chatHello, `"model":"gpt-upstream",`, `"model":"gpt-upstream","provider":"Somewhere",`), nil,
			[]string{"role", "content", "finish", "usage", "done"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := relay(t, protocol.Chat, strings.NewReader(c.stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if !errors.Is(got.err, c.err) || (c.err == nil) != (got.err == nil) {
				t.Fatalf("error %v, want %v", got.err, c.err)
			}
			if diff := cmp.Diff(c.want, chatKinds(t, got.out)); diff != "" {
				t.Errorf("chunks (-want +got):\n%s\n%s", diff, got.out)
			}
			if err := streamcheck.Chat(bytes.NewReader(got.out)); err != nil {
				t.Error(err)
			}
		})
	}
}

// chatMixed has two choices: the first finishes in the chunk where the second starts a tool call, and the second
// finishes in a chunk that also carries content and the usage.
const chatMixed = chatHead + `[{"index":0,"delta":{"role":"assistant","content":"x"},"finish_reason":null},` +
	`{"index":1,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
	chatHead + `[{"index":0,"delta":{},"finish_reason":"stop"},{"index":1,"delta":{"content":"A","tool_calls":` +
	`[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":""}}]},"finish_reason":null}]}` + "\n\n" +
	chatHead + `[{"index":1,"delta":{"content":"B","tool_calls":[{"index":0,"function":{"arguments":"{}"}}]},` +
	`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}` + "\n\n" + chatDone

// TestRelayChatChoices: when one choice finishes in a chunk that carries another's content, the content goes on in
// place and only the finish signal is held, so every choice keeps its order, tool calls included; a chunk with content
// is the commit point even when it finishes a choice and carries the usage.
func TestRelayChatChoices(t *testing.T) {
	got := relay(t, protocol.Chat, strings.NewReader(chatMixed), protocol.UsageContext{}, protocol.NewMemory(0), settled)
	want := map[string]string{"0/content": "x", "1/content": "AB", "1/tool0/name": "f", "1/tool0/arguments": "{}"}
	if diff := cmp.Diff(want, contents(t, protocol.Chat, got.out)); diff != "" || got.err != nil {
		t.Fatalf("error %v; contents (-want +got):\n%s\n%s", got.err, diff, got.out)
	}
	if err := streamcheck.Chat(bytes.NewReader(got.out)); err != nil {
		t.Error(err)
	}
	if u := got.r.Usage().Segments[0].Usage; u.Count(catalog.ItemInput) != 3 || u.Count(catalog.ItemOutput) != 2 {
		t.Errorf("usage taken from the content chunk: input %d, output %d", u.Count(catalog.ItemInput), u.Count(catalog.ItemOutput))
	}
	last := chatHead + `[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3}}` + "\n\n"
	if got := relay(t, protocol.Chat, strings.NewReader(last+chatDone), protocol.UsageContext{}, protocol.NewMemory(0),
		settled); !slices.Contains(got.kinds, protocol.StepCommit) {
		t.Errorf("the one chunk with content did not commit: %v", got.kinds)
	}
}

// chatKinds names each chunk of a client stream, checking that the chunks of a stream carry the call ID and the
// client's model and no provider, and that a usage carries our cost only.
func chatKinds(t *testing.T, stream []byte) []string {
	t.Helper()
	var kinds []string
	for block := range strings.SplitSeq(strings.TrimSuffix(string(stream), "\n\n"), "\n\n") {
		if block == "data: [DONE]" {
			kinds = append(kinds, "done")
			continue
		}
		e := events(t, []byte(block))[0]
		checkChunk(t, block, e)
		choices, _ := e["choices"].([]any)
		switch {
		case e["error"] != nil:
			kinds = append(kinds, "error")
		case e["usage"] != nil:
			kinds = append(kinds, "usage")
		case strings.Contains(block, `"finish_reason":"stop"`):
			kinds = append(kinds, "finish")
		case len(choices) > 0 && strings.Contains(block, `"role"`):
			kinds = append(kinds, "role")
		default:
			kinds = append(kinds, "content")
		}
	}
	return kinds
}

// checkChunk checks that a chunk names the call ID and the client's model and no provider, and that its usage has
// our cost alone.
func checkChunk(t *testing.T, block string, e map[string]any) {
	t.Helper()
	if e["error"] != nil { // the error chunk of a settlement not written has no cost
		return
	}
	if e["id"] != options.ID || e["model"] != options.Model || e["provider"] != nil {
		t.Errorf("chunk %s: id, model or provider not ours", block)
	}
	if u, ok := e["usage"].(map[string]any); ok && (u["cost"] != 3.703701 || u["is_byok"] != nil) {
		t.Errorf("usage %v: not our cost alone", u)
	}
}

// TestRelayChatContent: the dialects' reasoning, refusals and tool calls are content, counted as they arrive; a
// chunk with only a role is not the commit point.
func TestRelayChatContent(t *testing.T) {
	delta := `[{"index":0,"delta":{"reasoning_content":"ab","reasoning":"c","refusal":"d",` +
		`"reasoning_details":[{"type":"reasoning.text","text":"e"}],"tool_calls":[{"index":0,"id":"call_1","type":"function",` +
		`"function":{"name":"f","arguments":"{}"}}]},"finish_reason":null}]}` + "\n\n"
	o := options
	o.Memory = protocol.NewMemory(0)
	r := protocol.NewRelay(protocol.Chat, protocol.Chat, strings.NewReader(chatRole+chatHead+delta), protocol.UsageContext{}, o)
	stepTo(t, r, protocol.StepBuffered)
	step, err := r.Next()
	want := int64(len("ab") + len("c") + len("d") + len(`[{"type":"reasoning.text","text":"e"}]`) + len("f") + len("{}"))
	if p := r.Progress(); err != nil || step.Kind != protocol.StepCommit || p.ContentBytes != want {
		t.Errorf("step %d, %v; content %d, want %d", step.Kind, err, p.ContentBytes, want)
	}
}

// Responses events to build streams from, in the shape of the official example (testdata/responses/text).
func responsesEvent(typ string, seq int, rest string) string {
	return `data: {"type":"` + typ + `","sequence_number":` + strconv.Itoa(seq) + rest + "}\n\n"
}

const responsesObject = `,"response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-upstream","output":[],"usage":null}`

// TestRelayResponses: a failure after response.created is response.failed, numbered after the last event and naming
// the call ID; before it, the error event; response.incomplete is a normal end.
func TestRelayResponses(t *testing.T) {
	created := responsesEvent("response.created", 0, responsesObject)
	added := responsesEvent("response.output_item.added", 1, `,"output_index":0,"item":{"id":"msg_1","type":"message","content":[]}`)
	incomplete := responsesEvent("response.incomplete", 1, strings.Replace(responsesObject, `"usage":null`,
		`"usage":{"input_tokens":5,"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1},"cost":9}`, 1))
	for _, c := range []struct {
		name, stream, last string
		err                error
	}{
		{"broken after response.created", created + added, "response.failed", protocol.ErrUpstream},
		{"broken before it", "", "error", protocol.ErrUpstream},
		{"incomplete", created + incomplete, "response.incomplete", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := relay(t, protocol.Responses, strings.NewReader(c.stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if !errors.Is(got.err, c.err) || (c.err == nil) != (got.err == nil) {
				t.Fatalf("error %v, want %v", got.err, c.err)
			}
			es := events(t, got.out)
			last := es[len(es)-1]
			response, _ := last["response"].(map[string]any)
			if last["type"] != c.last || response != nil && (response["id"] != options.ID || response["model"] != options.Model) {
				t.Errorf("last event %v", last)
			}
			if err := streamcheck.Responses(bytes.NewReader(got.out)); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestRelayResponsesContent: deltas count as they arrive, and an output item that completes counts only what it adds
// to its deltas, so text is never counted twice and an item without deltas is still counted; a web search call
// counts as a search.
func TestRelayResponsesContent(t *testing.T) {
	item := `"item":{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"Hello"}]}`
	created := responsesEvent("response.created", 0, responsesObject)
	added := responsesEvent("response.output_item.added", 1, `,"output_index":0,"item":{"id":"msg_1","type":"message"}`)
	delta := responsesEvent("response.output_text.delta", 2, `,"output_index":0,"content_index":0,"item_id":"msg_1","delta":"Hello"`)
	done := responsesEvent("response.output_item.done", 3, `,"output_index":0,`+item)
	search := responsesEvent("response.output_item.added", 4, `,"output_index":1,"item":{"id":"ws_1","type":"web_search_call"}`)
	itemBytes := int64(len(item) - len(`"item"`)) // the item and the colon before it
	for _, c := range []struct {
		name, stream    string
		bytes, searches int64
	}{
		{"deltas and the item", created + added + delta + done, itemBytes, 0},
		{"the item alone", created + added + done, itemBytes, 0},
		{"a web search", created + added + done + search, itemBytes, 1},
	} {
		o := options
		o.Memory = protocol.NewMemory(0)
		r := protocol.NewRelay(protocol.Responses, protocol.Responses, strings.NewReader(c.stream), protocol.UsageContext{}, o)
		for _, err := r.Next(); err == nil; _, err = r.Next() {
		}
		if p := r.Progress(); p.ContentBytes != c.bytes || p.Searches != c.searches {
			t.Errorf("%s: content %d, searches %d; want %d, %d", c.name, p.ContentBytes, p.Searches, c.bytes, c.searches)
		}
	}
}

// TestRelayOpenAIUsage maps both OpenAI formats' usage as catalog README.md「计费项与用量」 tabulates: the input total
// less its cached, cache-written and audio parts is input; the output total less reasoning and audio is output.
func TestRelayOpenAIUsage(t *testing.T) {
	type counts = map[catalog.Item]int64
	for _, c := range []struct {
		name, usage string
		tier        string // the reply's service_tier member, if any
		want        counts
		st          catalog.ServiceTier
		unknown     []catalog.UnknownUsage
		invalid     bool
	}{
		{
			name: "parts", usage: `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":30,"cache_write_tokens":20,"audio_tokens":10},` +
				`"completion_tokens":50,"completion_tokens_details":{"reasoning_tokens":15,"audio_tokens":5},"total_tokens":150}`,
			want: counts{
				catalog.ItemInput: 40, catalog.ItemCacheRead: 30, catalog.ItemCacheWrite: 20, catalog.ItemAudioIn: 10,
				catalog.ItemOutput: 30, catalog.ItemReasoning: 15, catalog.ItemAudioOut: 5,
			},
		},
		{
			name: "no details", usage: `{"prompt_tokens":9,"completion_tokens":2,"cost":0.5,"is_byok":false,"cost_details":{}}`,
			want: counts{catalog.ItemInput: 9, catalog.ItemCacheRead: 0, catalog.ItemCacheWrite: 0, catalog.ItemOutput: 2, catalog.ItemReasoning: 0},
		},
		{name: "parts above the total", usage: `{"prompt_tokens":9,"prompt_tokens_details":{"cached_tokens":10}}`, invalid: true},
		{name: "a negative part", usage: `{"prompt_tokens":9,"prompt_tokens_details":{"audio_tokens":-1}}`, invalid: true},
		{name: "a negative total", usage: `{"completion_tokens":-2}`, invalid: true},
		{name: "parts that overflow", usage: `{"prompt_tokens":9,"prompt_tokens_details":{"cached_tokens":9223372036854775807,"cache_write_tokens":9223372036854775807}}`, invalid: true},
		{
			name: "unknown counts", usage: `{"completion_tokens":2,"image_tokens":4,"completion_tokens_details":{"accepted_prediction_tokens":1,"x_tokens":3}}`,
			want:    counts{catalog.ItemOutput: 2, catalog.ItemReasoning: 0},
			unknown: []catalog.UnknownUsage{{Field: "usage.completion_tokens_details.x_tokens", Value: "3"}, {Field: "usage.image_tokens", Value: "4"}},
		},
		{name: "flex", usage: `{}`, tier: "flex", want: counts{}, st: catalog.ServiceTierFlex},
		{name: "priority", usage: `{}`, tier: "priority", want: counts{}, st: catalog.ServiceTierPriority},
		{name: "default", usage: `{}`, tier: "default", want: counts{}},
		{name: "unknown tier", usage: `{}`, tier: "scale", want: counts{}, unknown: []catalog.UnknownUsage{{Field: "service_tier", Value: "scale"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tier := ""
			if c.tier != "" {
				tier = `"service_tier":"` + c.tier + `",`
			}
			stream := chatRole + chatHi + chatStop + strings.Replace(chatUsage(c.usage), `"choices"`, tier+`"choices"`, 1) + chatDone
			got := relay(t, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			u := got.r.Usage()
			if got.err != nil || (u.Invalid != nil) != c.invalid {
				t.Fatalf("error %v, invalid %v", got.err, u.Invalid)
			}
			if c.invalid {
				return
			}
			if diff := cmp.Diff([]protocol.Segment{segment(t, c.want, c.st, c.unknown)}, u.Segments, cmp.AllowUnexported(catalog.Usage{})); diff != "" {
				t.Errorf("segments (-want +got):\n%s", diff)
			}
		})
	}
}
