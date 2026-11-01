package protocol

import "sync/atomic"

// baseMemory is what each stream may hold without drawing on the node's budget; the gateway counts it per stream
// (internal/gateway/README.md「并发、背压与共享状态」).
const baseMemory = 64 << 10

// meterMemory is what the meter may hold, at its bounds (meter.go), charged when the relay starts: the meter lives
// until the settlement, past every buffer. TestRelayMemoryHeld measures it.
const meterMemory = 16 << 10

// Memory is the node's memory budget for streams. It is safe for concurrent use.
type Memory struct{ left atomic.Int64 }

// NewMemory returns a budget of size bytes.
func NewMemory(size int64) *Memory {
	m := &Memory{}
	m.left.Store(size)
	return m
}

// Charge draws n bytes from the budget and reports whether it had them; Refund gives them back. The gateway charges
// the request body it holds, the relay what its buffers hold past their base allowance.
func (m *Memory) Charge(n int64) bool {
	for {
		left := m.left.Load()
		if left < n {
			return false
		}
		if m.left.CompareAndSwap(left, left-n) {
			return true
		}
	}
}

// Refund returns n bytes Charge drew.
func (m *Memory) Refund(n int64) { m.left.Add(n) }
