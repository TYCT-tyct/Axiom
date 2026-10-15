// Package streamcheck checks that an event stream follows the structure its call format documents: which event
// comes first, the indices that tie each delta to an open part, and exactly one ending. It reads the stream through
// sse.Reader, so every event is already strict JSON, and it never judges content: the text, arguments and usage
// carried by the events are the business of the code that relays them.
//
// Event types a format does not list are accepted wherever the format's generic rules hold: Anthropic's versioning
// policy adds event types without notice and asks clients to tolerate them, and the OpenAI formats get the same
// tolerance, so a newer API version is not taken for a broken upstream.
package streamcheck

import (
	"errors"
	"fmt"
	"io"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// maxEvent bounds one event: the ceiling for the largest kind, a Responses snapshot that echoes the request and the
// whole output. The limits per kind of event are for the code that relays the stream to enforce.
const maxEvent = 32 << 20

// ErrInvalid reports a stream that breaks its format's structure. Errors from reading the stream itself, such as
// sse.ErrInvalid or io.ErrUnexpectedEOF, are returned as they are.
var ErrInvalid = errors.New("streamcheck: stream breaks its format")

// format holds one stream's state. event sees each event's data in order; end is called at a clean end of stream.
type format interface {
	event(data []byte) error
	end() error
}

func check(src io.Reader, f format) error {
	r := sse.NewReader(src, maxEvent)
	for n := 0; ; n++ {
		data, err := r.Next()
		switch {
		case errors.Is(err, io.EOF):
			if err := f.end(); err != nil {
				return fmt.Errorf("at the end: %w", err)
			}
			return nil
		case err != nil:
			return err
		}
		if err := f.event(data); err != nil {
			return fmt.Errorf("event %d: %w", n, err)
		}
	}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

// decode reads an event's data as a JSON object into v.
func decode(data []byte, v any) error {
	if err := jsonx.Unmarshal(data, v); err != nil {
		return invalid("data is not an event object: %v", err)
	}
	return nil
}

// at returns the index i points to when it is present and below n, the number of parts started so far. Indices are
// only ever compared with what has been seen, never used to size anything, so a hostile index costs nothing.
func at(i *int64, n int) (int, bool) {
	if i == nil || *i < 0 || *i >= int64(n) {
		return 0, false
	}
	return int(*i), true
}

// next reports whether i is present and starts the next part after n parts.
func next(i *int64, n int) bool { return i != nil && *i == int64(n) }
