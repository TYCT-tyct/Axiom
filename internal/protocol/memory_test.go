package protocol_test

import (
	"testing"

	"axiom/internal/protocol"
)

// TestMemoryChargeRefund: a charge the budget cannot cover draws nothing, and what a charge drew a refund returns.
func TestMemoryChargeRefund(t *testing.T) {
	m := protocol.NewMemory(100)
	if !m.Charge(60) || m.Charge(41) || m.Left() != 40 {
		t.Fatalf("after charging 60 and refusing 41, %d is left, want 40", m.Left())
	}
	m.Refund(60)
	if m.Left() != 100 || !m.Charge(100) {
		t.Fatalf("after the refund %d is left", m.Left())
	}
}
