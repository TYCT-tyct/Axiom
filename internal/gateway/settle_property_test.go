package gateway

import (
	"testing"

	"axiom/internal/billing"
	"axiom/internal/platform/textshape"
	"axiom/internal/protocol"
)

// FuzzToolCallsAddToEstimate: a client tool call adds a non-negative framework to the output estimate, never takes it
// below the estimate without calls, and never pushes it past the cap the attempt was quoted at.
func FuzzToolCallsAddToEstimate(f *testing.F) {
	f.Add(int64(0), int64(1000))
	f.Add(int64(1), int64(1000))
	f.Add(int64(2), int64(500))
	f.Add(int64(10), int64(100))
	f.Add(int64(-50), int64(1001))
	f.Add(int64(62), int64(-19))
	f.Fuzz(func(t *testing.T, calls, cap int64) {
		const text = "The quick brown fox jumps over the lazy dog. "
		var shape textshape.Shape
		shape.Add([]byte(text))
		calls %= 100
		if calls < 0 {
			calls = -calls
		}
		p := protocol.Progress{ContentBytes: int64(len(text)), Text: shape, ToolCalls: calls}
		cap %= 2000
		if cap < 0 {
			cap = -cap
		}
		cap++
		base := estimateOutput(billing.Segment{}, p, cap, billing.TokenizerClaude47)
		p.ToolCalls = 0
		plain := estimateOutput(billing.Segment{}, p, cap, billing.TokenizerClaude47)
		if base < plain || base > cap {
			t.Fatalf("calls %d, cap %d: estimate %d, without calls %d", calls, cap, base, plain)
		}
	})
}
