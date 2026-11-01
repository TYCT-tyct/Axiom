package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"axiom/internal/platform/jsonx"
)

func TestServerCaptureChat(t *testing.T) {
	c := newServerCapture(Chat)
	for _, event := range []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"openrouter_web_search","arguments":"{\"query\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"axiom\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"keep","arguments":"{}"}}]}}]}`,
	} {
		if err := c.event([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	calls := c.serverCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Arguments != `{"query":"axiom"}` {
		t.Fatalf("calls = %+v", calls)
	}
	if len(c.calls) != 2 || !c.calls[1].Client {
		t.Fatalf("the client tool was not kept as part of the turn: %+v", c.calls)
	}
}

func TestServerCaptureMessages(t *testing.T) {
	c := newServerCapture(Messages)
	for _, event := range []string{
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"openrouter_web_search","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"axiom\"}"}}`,
	} {
		if err := c.event([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	if calls := c.serverCalls(); len(calls) != 1 || calls[0].ID != "toolu_1" || calls[0].Arguments != `{"query":"axiom"}` {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestServerCaptureResponses(t *testing.T) {
	c := newServerCapture(Responses)
	for _, event := range []string{
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"openrouter_web_search","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"query\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"\"axiom\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":2,"arguments":"{\"query\":\"axiom\"}"}`,
	} {
		if err := c.event([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	if calls := c.serverCalls(); len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Arguments != `{"query":"axiom"}` {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestServerCaptureIgnoresOtherTools(t *testing.T) {
	c := newServerCapture(Chat)
	if err := c.event([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"keep","arguments":"{}"}}]}}]}`)); err != nil {
		t.Fatal(err)
	}
	if len(c.serverCalls()) != 0 || !c.client || len(c.calls) != 1 {
		t.Fatalf("calls = %+v, client = %v", c.calls, c.client)
	}
}

func TestAppendCapturedResponsesTurnFlattensItems(t *testing.T) {
	c := newServerCapture(Responses)
	for _, event := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"openrouter_web_search","arguments":"{}"}}`,
	} {
		if err := c.event([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	turn, err := c.turn()
	if err != nil {
		t.Fatal(err)
	}
	body, err := AppendServerTurn(Responses, []byte(`{"model":"m","input":[{"role":"user","content":"q"}]}`), turn, []string{"result"})
	if err != nil {
		t.Fatal(err)
	}
	var input []struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
	}
	if err := jsonx.Unmarshal(mustTop(t, body, "input"), &input); err != nil {
		t.Fatalf("input is not a flat array: %v: %s", err, body)
	}
	if len(input) != 4 || input[1].Type != "message" || input[2].Type != "function_call" || input[3].Type != "function_call_output" {
		t.Fatalf("input = %+v: %s", input, body)
	}
}

func TestAppendCapturedMessagesRedactedThinking(t *testing.T) {
	c := newServerCapture(Messages)
	for _, event := range []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"ciphertext"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"openrouter_web_search","input":{}}}`,
	} {
		if err := c.event([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	turn, err := c.turn()
	if err != nil || !bytes.Contains(turn.Assistant, []byte(`"data":"ciphertext"`)) {
		t.Fatalf("redacted_thinking.data was lost: %s", turn.Assistant)
	}
	body, err := AppendServerTurn(Messages, []byte(`{"model":"m","messages":[{"role":"user","content":"q"}]}`), turn, []string{"result"})
	if err != nil || !bytes.Contains(body, []byte(`"data":"ciphertext"`)) {
		t.Fatalf("append = %s, %v", body, err)
	}
}

func mustTop(t *testing.T, body []byte, member string) []byte {
	t.Helper()
	value, _, _, ok, err := topMember(body, member)
	if err != nil || !ok {
		t.Fatalf("missing %s: %s, %v", member, body, err)
	}
	return value
}

// shortBlocks is a Messages tool hop of n one-character text blocks, then a web search call.
func shortBlocks(n int) string {
	var b strings.Builder
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	for i := range n {
		fmt.Fprintf(&b, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":\"text\",\"text\":\"x\"}}\n\n", i)
		fmt.Fprintf(&b, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", i)
	}
	fmt.Fprintf(&b, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":\"tool_use\",\"id\":\"c1\",\"name\":\"openrouter_web_search\",\"input\":{\"query\":\"q\"}}}\n\n", n)
	fmt.Fprintf(&b, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", n)
	b.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n\n")
	b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return b.String()
}

// readCapture reads a tool hop to its end or its first error.
func readCapture(stream string, mem *Memory) (*Relay, error) {
	r := NewRelay(Messages, Messages, strings.NewReader(stream), UsageContext{},
		RelayOptions{ID: "g", Model: "m", Created: 1, Memory: mem, ServerTool: true})
	for {
		st, err := r.Next()
		if err != nil || st.Kind == StepUpstreamDone {
			return r, err
		}
	}
}

// TestCaptureChargesItsContainers: many short blocks hold little text but a block, a slice slot and an index entry
// each; the budget is charged for those, not for the text alone.
func TestCaptureChargesItsContainers(t *testing.T) {
	const blocks = 4000
	const budget = 64 << 20
	mem := NewMemory(budget)
	r, err := readCapture(shortBlocks(blocks), mem)
	if err != nil {
		t.Fatal(err)
	}
	containers := int64(blocks) * int64(unsafe.Sizeof(capturedBlock{})+mapEntry)
	if held := budget - mem.Left(); held < containers-baseMemory {
		t.Fatalf("the relay draws %d bytes for %d blocks, want at least their %d bytes of containers", held, blocks, containers)
	}
	if r.srv.held != r.srv.footprint() {
		t.Fatalf("charged %d for a capture of %d", r.srv.held, r.srv.footprint())
	}
	r.Release()
	if mem.Left() != budget {
		t.Fatalf("after the release %d bytes are still held", budget-mem.Left())
	}
}

// TestCaptureRefusesGrowthPastTheBudget: when the budget cannot cover what an event may add, the event is not read
// into the capture: the refusal comes before the allocation, and nothing is held that was not charged.
func TestCaptureRefusesGrowthPastTheBudget(t *testing.T) {
	r := NewRelay(Messages, Messages, strings.NewReader(""), UsageContext{},
		RelayOptions{ID: "g", Model: "m", Created: 1, Memory: NewMemory(0), ServerTool: true})
	r.held = r.free + r.charged // the base allowance is spent and the node has nothing left
	start := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"x"}}`)
	if err := r.captureEvent(start); !errors.Is(err, ErrMemory) {
		t.Fatalf("an event past the budget = %v, want ErrMemory", err)
	}
	if len(r.srv.blocks) != 0 || r.srv.footprint() != 0 || r.srv.held != 0 {
		t.Fatalf("after the refusal: %d blocks, holding %d, charged %d; want the event unread", len(r.srv.blocks), r.srv.footprint(), r.srv.held)
	}
}
