package protocol_test

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // preserve the client assistant message for its continuation
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

const hiddenThinkingToolStream = `event: message_start
data: {"type":"message_start","message":{"id":"m","model":"m","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"f","input":{}}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}

event: message_stop
data: {"type":"message_stop"}

`

const hiddenChatToolStream = `data: {"id":"up","object":"chat.completion.chunk","model":"up","created":1,"choices":[{"index":0,"delta":{"role":"assistant","reasoning":"private","reasoning_details":[{"type":"reasoning.text","text":"private","signature":"opaque","format":"anthropic-claude-v1","index":0},{"type":"reasoning.encrypted","data":"cipher","format":"anthropic-claude-v1","index":1},{"type":"reasoning.summary","summary":"private summary","index":2}],"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":15}}}

data: [DONE]

`

func TestHiddenChatToolResultReplaysManualThinking(t *testing.T) {
	for _, tc := range []struct {
		up     protocol.Format
		stream string
	}{
		{protocol.Messages, hiddenThinkingToolStream},
		{protocol.Chat, hiddenChatToolStream},
	} {
		for _, aggregate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/aggregate=%t", tc.up, aggregate), func(t *testing.T) {
				checkHiddenToolContinuation(t, tc.up, tc.stream, aggregate, protocol.Messages, `"thinking":"","signature":"opaque"`)
			})
		}
	}
}

func checkHiddenToolContinuation(t *testing.T, up protocol.Format, stream string, aggregate bool, target protocol.Format, state string) {
	t.Helper()
	out, _, _ := reasoningReply(t, protocol.Chat, up, []byte(stream), true, aggregate)
	if !aggregate {
		checkReasoningWire(t, protocol.Chat, out, false)
		var err error
		out, err = jsonx.MarshalCanonical(foldStream(t, protocol.Chat, out))
		if err != nil {
			t.Fatal(err)
		}
	}
	var reply struct {
		Choices []struct {
			Message jsontext.Value `json:"message"`
		} `json:"choices"`
	}
	if err := jsonx.Unmarshal(out, &reply); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"m","reasoning":{"effort":"high","exclude":true},"messages":[{"role":"user","content":"go"},` + string(reply.Choices[0].Message) + `,{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`
	got, err := convertBody(target, body, func(tg *protocol.Target) { tg.Thinking = catalog.ThinkingManual })
	if err != nil {
		t.Fatalf("hidden tool continuation failed: %v\n%s", err, out)
	}
	if bytes.Contains(out, []byte("private")) || !bytes.Contains(got, []byte(state)) {
		t.Fatalf("lost hidden thinking state: %s\n%s", out, got)
	}
}

func TestHiddenChatReasoningBufferIsBounded(t *testing.T) {
	for _, tc := range []struct {
		delta string
		bytes int
	}{
		{`"reasoning_content":"private"`, len("private")},
		{`"reasoning":"private"`, len("private")},
		{`"reasoning_details":[{"type":"reasoning.text","text":"private"}]`, len(`[{"type":"reasoning.text","text":"private"}]`)},
		{`"reasoning_details":[{"type":"reasoning.summary","summary":"private"}]`, len(`[{"type":"reasoning.summary","summary":"private"}]`)},
	} {
		t.Run(tc.delta, func(t *testing.T) { checkHiddenChatBuffer(t, tc.delta, tc.bytes) })
	}
}

func checkHiddenChatBuffer(t *testing.T, delta string, bytesPerChunk int) {
	t.Helper()
	const chunks = 20000
	stream := `data: {"choices":[{"index":0,"delta":{"role":"assistant"}}]}` + "\n\n" +
		strings.Repeat(`data: {"choices":[{"index":0,"delta":{`+delta+`}}]}`+"\n\n", chunks) +
		`data: {"choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":20001,"completion_tokens_details":{"reasoning_tokens":20000}}}` + "\n\ndata: [DONE]\n\n"
	mem := protocol.NewMemory(64 << 10)
	r := protocol.NewRelay(protocol.Chat, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{HidesReasoning: true}, protocol.RelayOptions{Memory: mem})
	defer r.Release()
	for {
		step, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if step.Kind == protocol.StepCommit && len(step.Out) > 2048 {
			t.Fatalf("precommit output grew to %d bytes", len(step.Out))
		}
		if step.Kind == protocol.StepUpstreamDone {
			break
		}
	}
	if got := r.Progress().ContentBytes; got != int64(chunks*bytesPerChunk+len("answer")) {
		t.Fatalf("lost hidden progress: %d", got)
	}
	if r.Usage().Segments[0].Usage.Count(catalog.ItemReasoning) != chunks {
		t.Fatal("lost billed reasoning")
	}
	r.Finish(settled(r.Usage()))
	if mem.Left() != 64<<10 {
		t.Fatal("hidden buffer was not released")
	}
}

func TestHiddenChatEncryptedContinuation(t *testing.T) {
	stream, err := os.ReadFile("testdata/assembled/responses-reasoning-tool.sse")
	if err != nil {
		t.Fatal(err)
	}
	for _, aggregate := range []bool{false, true} {
		t.Run(fmt.Sprint(aggregate), func(t *testing.T) {
			checkHiddenToolContinuation(t, protocol.Responses, string(stream), aggregate, protocol.Responses, `"encrypted_content":"gAAAAABo_encrypted_reasoning_1"`)
		})
	}
}

func TestHiddenChatDetailsKeepOpaqueState(t *testing.T) {
	const suffix = `,"format":"anthropic-claude-v1","index":0}`
	const encrypted = `{"type":"reasoning.encrypted","data":"cipher","format":"anthropic-claude-v1","index":1}`
	for _, detail := range []string{
		`{"type":"reasoning.text","text":"private","signature":"opaque"` + suffix,
		`{"signature":"opaque","type":"reasoning.text"` + suffix,
		`{"signature":"op\u0061que","text":"private","type":"reasoning.t\u0065xt"` + suffix,
	} {
		t.Run(detail, func(t *testing.T) {
			stream := `data: {"id":"up","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"answer","reasoning_details":[` + detail + `,` + encrypted + `,{"type":"reasoning.summary","summary":"private"},{"type":"reasoning.text","text":"private"}]},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
			out, _, _ := reasoningReply(t, protocol.Chat, protocol.Chat, []byte(stream), true, true)
			var reply struct {
				Choices []struct {
					Message struct {
						Details jsontext.Value `json:"reasoning_details"`
					} `json:"message"`
				} `json:"choices"`
			}
			if err := jsonx.Unmarshal(out, &reply); err != nil {
				t.Fatal(err)
			}
			sameJSON(t, `[{"type":"reasoning.text","text":"","signature":"opaque","format":"anthropic-claude-v1","index":0},`+encrypted+`]`, reply.Choices[0].Message.Details)
		})
	}
}

func TestHiddenResponsesPartMatchesSnapshots(t *testing.T) {
	const part = `{"type":"reasoning_text","text":"private"}`
	const item = `{"id":"rs","type":"reasoning","summary":[],"content":[` + part + `],"encrypted_content":"opaque"}`
	source := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"r","model":"m","output":[]}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"rs","type":"reasoning","summary":[],"content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":2,"output_index":0,"content_index":0,"item_id":"rs","part":` + part + `}`,
		`{"type":"response.content_part.done","sequence_number":3,"output_index":0,"content_index":0,"item_id":"rs","part":` + part + `}`,
		`{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":` + item + `}`,
		`{"type":"response.completed","sequence_number":5,"response":{"id":"r","model":"m","output":[` + item + `],"usage":{"input_tokens":10,"output_tokens":20,"output_tokens_details":{"reasoning_tokens":15}}}}`,
	}
	stream := []byte("data: " + strings.Join(source, "\n\ndata: ") + "\n\n")
	wire, _, _ := reasoningReply(t, protocol.Responses, protocol.Responses, stream, true, false)
	checkReasoningWire(t, protocol.Responses, wire, false)
	var streamed map[string]any
	for _, event := range events(t, wire) {
		var content any
		switch event["type"] {
		case "response.content_part.added", "response.content_part.done":
			streamed = obj(event["part"])
			if streamed["text"] != "" {
				t.Fatal("reasoning part was not hidden")
			}
		case "response.output_item.done":
			content = obj(event["item"])["content"]
		case "response.completed":
			content = obj(obj(event["response"])["output"].([]any)[0])["content"]
		}
		if content != nil {
			if diff := cmp.Diff([]any{streamed}, content); diff != "" {
				t.Fatalf("snapshot disagrees with streamed index: %s", diff)
			}
		}
	}
}
