package persist

import (
	"slices"
	"testing"

	"axiom/internal/protocol"
)

// TestWritableFormatsGate: with the gate closed the projection writes only the three generation formats a pre-P18 node
// reads; opening it writes every format this release serves, embeddings and rerank included.
func TestWritableFormatsGate(t *testing.T) {
	var every []protocol.Format
	for f := protocol.Messages; f <= protocol.Rerank; f++ {
		every = append(every, f)
	}
	closed := writableFormats(slices.Clone(every), false)
	if want := []protocol.Format{protocol.Messages, protocol.Responses, protocol.Chat}; !slices.Equal(closed, want) {
		t.Errorf("closed gate writes %v, want %v", closed, want)
	}
	open := writableFormats(slices.Clone(every), true)
	if want := []protocol.Format{protocol.Messages, protocol.Responses, protocol.Chat, protocol.Embeddings, protocol.Rerank}; !slices.Equal(open, want) {
		t.Errorf("open gate writes %v, want %v", open, want)
	}
	if closed := writableFormats([]protocol.Format{protocol.Rerank + 1}, false); len(closed) != 0 {
		t.Errorf("closed gate writes a format added after rerank: %v", closed)
	}
}
