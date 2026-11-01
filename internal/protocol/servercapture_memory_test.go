package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// longToolTurn is a Chat tool hop whose reasoning comes in chunks of 1 KiB, n of them, followed by a web search call.
// No one event is large, so only what the capture keeps grows with n.
func longToolTurn(n int) string {
	var b strings.Builder
	chunk := `data: {"id":"g","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"reasoning_content":"` +
		strings.Repeat("r", 1<<10) + `"},"finish_reason":null}]}` + "\n\n"
	for range n {
		b.WriteString(chunk)
	}
	b.WriteString(`data: {"id":"g","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"openrouter_web_search","arguments":"{\"query\":\"q\"}"}}]},"finish_reason":"tool_calls"}]}

data: {"id":"g","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}

data: [DONE]

`)
	return b.String()
}

// TestServerTurnIsChargedToTheBudget: what the loop keeps of a tool hop, the captured turn and the turn built from it,
// is the node's memory like any buffer of the relay. It is drawn before it is held, refused when the budget is spent,
// and given back whole when the relay is released.
func TestServerTurnIsChargedToTheBudget(t *testing.T) {
	const chunks = 256 // 256 KiB of reasoning, past the relay's base allowance
	const reasoning = chunks << 10
	read := func(mem *protocol.Memory) (*protocol.Relay, error) {
		r := protocol.NewRelay(protocol.Chat, protocol.Chat, strings.NewReader(longToolTurn(chunks)), protocol.UsageContext{},
			protocol.RelayOptions{ID: "g", Model: "m", Created: 1, Memory: mem, ServerTool: true})
		for {
			st, err := r.Next()
			if err != nil {
				return r, err
			}
			if st.Kind == protocol.StepUpstreamDone {
				_, err = r.ServerTurn()
				return r, err
			}
		}
	}

	if _, err := read(protocol.NewMemory(reasoning)); !errors.Is(err, protocol.ErrMemory) {
		t.Fatalf("a turn past the budget = %v, want ErrMemory", err)
	}
	const budget = 64 << 20
	mem := protocol.NewMemory(budget)
	r, err := read(mem)
	if err != nil {
		t.Fatal(err)
	}
	if held := budget - mem.Left(); held < 2*reasoning {
		t.Fatalf("the relay holds %d bytes of the budget for a %d-byte turn, want the capture and the turn charged", held, reasoning)
	}
	r.Release()
	if mem.Left() != budget {
		t.Fatalf("after the release %d bytes are still held", budget-mem.Left())
	}
}
