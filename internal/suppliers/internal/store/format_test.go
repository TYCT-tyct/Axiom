package store

import (
	"errors"
	"testing"

	"axiom/internal/protocol"
)

// TestParseFormatsUnknown: a format a later release wrote is named as its own error, so Reload can leave out that one
// endpoint rather than the supplier, while the names this node knows still parse.
func TestParseFormatsUnknown(t *testing.T) {
	got, err := ParseFormats([]string{"messages", "embeddings"})
	if err != nil || len(got) != 2 || got[0] != protocol.Messages || got[1] != protocol.Embeddings {
		t.Fatalf("ParseFormats = %v, %v", got, err)
	}
	if _, err := ParseFormats([]string{"messages", "telepathy"}); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("unknown format error = %v, want ErrUnsupportedFormat", err)
	}
}
