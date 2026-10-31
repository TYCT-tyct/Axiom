package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// TestAggregateCitationReservesMemoryBeforeSeal: citation annotations are generated after the aggregator seals, when
// its own growth checks are off, so the working set they need is drawn while the content is being aggregated. A node
// without it fails the step with ErrMemory rather than allocating through the answer and past the seal.
func TestAggregateCitationReservesMemoryBeforeSeal(t *testing.T) {
	stream := `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"See [example.com](https://example.com/one)."}}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	mem := protocol.NewMemory(0)
	o := protocol.RelayOptions{
		ID: "gen-1", Model: "m", Created: 1, Aggregate: true,
		Memory: mem,
		Citations: []protocol.Citation{{
			URL: "https://example.com/one", Title: "One", Content: "first",
		}},
	}
	r := protocol.NewRelay(protocol.Chat, protocol.Chat, strings.NewReader(stream), protocol.UsageContext{}, o)
	var err error
	for {
		if _, err = r.Next(); err != nil {
			break
		}
	}
	if !errors.Is(err, protocol.ErrMemory) {
		t.Fatalf("Next() error = %v, want ErrMemory before the citation is generated", err)
	}
	if left := mem.Left(); left != 0 {
		t.Fatalf("the budget has %d bytes left, want none", left)
	}
}
