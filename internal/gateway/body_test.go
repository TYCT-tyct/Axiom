package gateway

import (
	"bytes"
	"errors"
	"strconv"
	"testing"

	"axiom/internal/protocol"
)

// TestUnannouncedBodyIsChargedBeforeItIsAllocated: the buffer of a body of unknown length is charged before it is made, so
// a budget that covers the two buffers of the largest growth is enough and one byte less is a 503 that leaves nothing
// charged; what stays charged is the buffer that holds the body. io.ReadAll held about twice the body and charged none
// of it but the limit up front.
func TestUnannouncedBodyIsChargedBeforeItIsAllocated(t *testing.T) {
	t.Parallel()
	const limit = 1 << 22
	bp, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{0, 1, minBody - 1, minBody, minBody + 1, 100_000, 1 << 20, limit} {
		t.Run(strconv.FormatInt(n, 10), func(t *testing.T) {
			t.Parallel()
			// The buffers grow while the body fills the one it has, which is full when it holds n bytes.
			var peak, final int64
			for final <= n {
				next := min(max(2*final, minBody), limit+1)
				peak, final = max(peak, final+next), next
			}
			body := bytes.Repeat([]byte{'x'}, int(n))
			for budget, wantErr := range map[int64]bool{peak: false, peak - 1: true} {
				h := &Handler{Deps: Deps{Limits: bp, Memory: protocol.NewMemory(budget)}}
				got, held, err := h.readUnannounced(bytes.NewReader(body), -1, limit)
				if wantErr {
					if !errors.Is(err, ErrOverloaded) || !h.Memory.Charge(budget) {
						t.Errorf("budget %d: %v, and the whole budget is free: %v", budget, err, !h.Memory.Charge(budget))
					}
					continue
				}
				if err != nil || !bytes.Equal(got, body) || held != final || cap(got) != int(final) {
					t.Fatalf("budget %d: err %v, %d bytes held %d, want %d bytes held %d", budget, err, len(got), held, n, final)
				}
				if !h.Memory.Charge(budget-final) || h.Memory.Charge(1) && budget > final {
					t.Errorf("after the read the budget left is not what the buffer left of it")
				}
			}
		})
	}
}
