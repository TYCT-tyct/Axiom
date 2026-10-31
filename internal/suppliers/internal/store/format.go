package store

import (
	"errors"
	"fmt"
	"slices"

	"axiom/internal/protocol"
)

// formatNames are the call formats under the names the formats columns store, in protocol's enumeration order. The
// columns' CHECK lists exactly these, so a name added here without a migration is refused by the database.
var formatNames = [...]string{"messages", "responses", "chat", "embeddings", "rerank"}

// FormatNames writes call formats as the columns store them: no repeats, in enumeration order, so that the same set
// written twice is the same array and a sync that reads what it wrote finds nothing to change. The columns hold at least
// one format, so a caller that has none to write skips the row rather than passing an empty array.
func FormatNames(formats []protocol.Format) ([]string, error) {
	var present [len(formatNames)]bool
	for _, f := range formats {
		if int(f) >= len(formatNames) {
			return nil, fmt.Errorf("store: %d is not a call format", f)
		}
		present[f] = true
	}
	out := make([]string, 0, len(formatNames))
	for f, has := range present {
		if has {
			out = append(out, formatNames[f])
		}
	}
	return out, nil
}

// ErrUnsupportedFormat is a formats column with a name this node does not know. A reader must leave that endpoint out,
// not the supplier: a newer node may have written a format this release cannot serve.
var ErrUnsupportedFormat = errors.New("store: a stored call format is not known to this node")

// ParseFormats reads the names a formats column holds, as FormatNames wrote them.
func ParseFormats(names []string) ([]protocol.Format, error) {
	out := make([]protocol.Format, 0, len(names))
	for _, name := range names {
		i := slices.Index(formatNames[:], name)
		if i < 0 {
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, name)
		}
		out = append(out, protocol.Format(i)) //nolint:gosec // the index of a name, so below the number of formats
	}
	return out, nil
}
