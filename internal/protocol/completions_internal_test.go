package protocol

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/platform/alloctest"
)

// completionsBudgetChunk is one chunk whose only piece is a logprobs token of size bytes: the other arrays are left
// out so that filling the budget is not undone by their tiny elements.
func completionsBudgetChunk(size int) []byte {
	return []byte(`{"id":"x","object":"text_completion","created":1,"model":"m","choices":[{"index":0,"text":"",` +
		`"logprobs":{"tokens":["` + strings.Repeat("t", size) + `"]},"finish_reason":null}]}`)
}

// TestCompletionsLogprobsBudget: once the reply holds 16 MiB, a chunk that would pass the cap fails before its buffer
// is made, while a chunk that fits still succeeds; so the stream is bounded by the reply's size and not by a temporary
// copy of it (docs/domains/gateway.md「决策与理由」).
//
//nolint:cyclop,gocognit // one budget, a chunk too large and one that fits, and the allocations measured
func TestCompletionsLogprobsBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("15 MiB of logprobs")
	}
	// Fifteen 1 MiB chunks grow the token buffer to 16 MiB but its contents to 15 MiB: the doubling leaves less than one
	// more chunk's room, so the next 1 MiB chunk cannot be charged, and a 64-byte one can.
	prefix := make([][]byte, 15)
	for i := range prefix {
		prefix[i] = completionsBudgetChunk(1 << 20)
	}
	small, large := completionsBudgetChunk(64), completionsBudgetChunk(1<<20)
	feed := func(last []byte) (held, drew int64, err error) {
		mem := NewMemory(1 << 30)
		a := newAggregator(Completions, func(n int) error {
			if !mem.Charge(int64(n)) {
				return ErrMemory
			}
			return nil
		}).(*completionsAggregate)
		for _, chunk := range prefix {
			if err := a.event(chunk); err != nil {
				t.Fatalf("a prefix chunk failed: %v", err)
			}
		}
		before := mem.Left()
		err = a.event(last)
		return a.held, before - mem.Left(), err
	}
	held, drew, err := feed(large)
	if !errors.Is(err, ErrMemory) || errors.Is(err, ErrUpstream) {
		t.Fatalf("error %v, want a memory error", err)
	}
	if held > aggregateLimit {
		t.Errorf("%d bytes held of a %d limit", held, aggregateLimit)
	}
	if drew != 0 {
		t.Errorf("the failed chunk drew %d bytes", drew)
	}
	if _, _, err := feed(small); err != nil {
		t.Fatalf("the 64-byte chunk failed with %v, want it to fit in the room left", err)
	}
	got := alloctest.Measure(t,
		alloctest.Function{Name: "small last chunk", F: func() {
			if _, _, err := feed(small); err != nil {
				panic(err)
			}
		}},
		alloctest.Function{Name: "large last chunk", F: func() {
			if _, _, err := feed(large); !errors.Is(err, ErrMemory) {
				panic(err)
			}
		}},
	)
	if got["large last chunk"].Bytes > got["small last chunk"].Bytes+4096 {
		t.Errorf("the 1 MiB chunk allocates more than the 64-byte one before failing: %v and %v",
			got["large last chunk"], got["small last chunk"])
	}
}
