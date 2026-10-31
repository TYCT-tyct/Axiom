package persist

import (
	"slices"
	"testing"

	"axiom/internal/protocol"
)

// TestWritableFormatsGate: with the gate closed the projection writes only the formats a pre-P18 node reads; opening it
// lets the direct formats through.
func TestWritableFormatsGate(t *testing.T) {
	in := []protocol.Format{protocol.Messages, protocol.Embeddings, protocol.Chat, protocol.Rerank, protocol.Completions}
	closed := writableFormats(slices.Clone(in), false)
	if want := []protocol.Format{protocol.Messages, protocol.Chat, protocol.Completions}; !slices.Equal(closed, want) {
		t.Fatalf("closed gate writes %v, want %v", closed, want)
	}
	open := writableFormats(slices.Clone(in), true)
	if !slices.Equal(open, in) {
		t.Fatalf("open gate writes %v, want %v", open, in)
	}
}
