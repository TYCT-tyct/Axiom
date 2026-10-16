package gateway

import (
	"bytes"
	"errors"
	"strconv"
	"testing"

	"axiom/internal/protocol"
)

// growth simulates the buffer of readUnannounced for a body of n bytes: the most charged at once, which is the old
// buffer and the new one together, and the size it ends at. The buffer grows whenever it is full, which includes the
// moment it holds exactly n bytes and has still to see the end.
func growth(n, limit int64) (peak, final int64) {
	for final <= n {
		next := min(max(2*final, minBody), limit+1)
		peak, final = max(peak, final+next), next
	}
	return peak, final
}

// readWith reads body as a body of unknown length with a budget of that many bytes.
func readWith(bp *Backpressure, budget, limit int64, body []byte) (h *Handler, got []byte, held int64, err error) {
	h = &Handler{}
	h.Limits, h.Memory = bp, protocol.NewMemory(budget)
	got, held, err = h.readUnannounced(bytes.NewReader(body), -1, limit)
	return h, got, held, err
}

// holdsExactly checks a read that had peak to spare: it got the body, ended with a buffer of final bytes and holds
// exactly that much of the budget.
func holdsExactly(t *testing.T, bp *Backpressure, limit int64, body []byte) {
	t.Helper()
	peak, final := growth(int64(len(body)), limit)
	h, got, held, err := readWith(bp, peak, limit, body)
	if err != nil || !bytes.Equal(got, body) || held != final || cap(got) != int(final) {
		t.Fatalf("budget %d: err %v, %d bytes held %d, want %d bytes held %d", peak, err, len(got), held, len(body), final)
	}
	if !h.Memory.Charge(peak - final) { // what the buffer left of the budget is all there is
		t.Errorf("the buffer holds more than the %d it was charged", final)
	}
	if h.Memory.Charge(1) {
		t.Errorf("the buffer holds less than the %d it was charged", final)
	}
}

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
			body := bytes.Repeat([]byte{'x'}, int(n))
			holdsExactly(t, bp, limit, body)
			peak, _ := growth(n, limit)
			h, _, _, err := readWith(bp, peak-1, limit, body)
			if free := h.Memory.Charge(peak - 1); !errors.Is(err, ErrOverloaded) || !free {
				t.Errorf("one byte under the peak: %v, and the whole budget is free: %v", err, free)
			}
		})
	}
}
